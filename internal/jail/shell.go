package jail

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// This file is the jailed shell wrapper. OpenCode is configured with `shell: ServerExePath`, so for every
// command the model runs it spawns `<ServerExePath> -c <cmd>`; the host binary dispatches that to
// RunJailedShell. The wrapper does two things before the command runs:
//
//  1. Builds a clean environment: it drops OPENCODE_* and anything that looks like a key, token, secret or
//     password (so `echo $OPENCODE_PASSWORD` and the proxy's variables leak nothing) and points HOME and
//     TMPDIR at the writable private /tmp rather than the masked /state.
//  2. Enters fresh user, network, mount and pid namespaces (`unshare -U -r -n -m -p --fork`). The new
//     network namespace removes all connectivity, so the OpenCode API, the key proxy and the internet are
//     unreachable from a command. The new user namespace makes /proc/1/environ (the server's own
//     environment) unreadable and blocks ptrace of the server. In the new mount namespace the wrapper
//     overmounts /state (OpenCode's database) with an empty tmpfs, and for a read-mode task remounts every
//     edit worktree read-only, so a read task has nothing writable under the repository.
//
// Commands therefore have no network: pip/npm/go module downloads fail. Offline, vendored builds and tests
// still work. The repository is mounted read-only and edit worktrees are writable only for edit-mode tasks.

// jailShellInner runs inside the new namespaces as `/bin/sh -c <inner> p0ssess-shell <cmd> <lockflag>`.
// $1 is the command, $2 is "lock" for a read-mode task. It masks /state, optionally locks worktrees, then
// execs the command. Everything is best-effort with `|| true` so a command still runs if a mount is absent.
const jailShellInner = `mount -t tmpfs -o size=1M,mode=000 tmpfs /state 2>/dev/null || true
if [ "$2" = lock ]; then
  for m in $(awk '$5 ~ /\/\.p0ssess\/wt$|\/\.git\/worktrees$/ {print $5}' /proc/self/mountinfo); do
    mount -o remount,bind,ro "$m" 2>/dev/null || true
  done
fi
exec /bin/sh -c "$1"`

// sensitiveEnv reports whether an environment variable must be dropped before running a command.
func sensitiveEnv(name string) bool {
	up := strings.ToUpper(name)
	if strings.HasPrefix(up, "OPENCODE") {
		return true
	}
	for _, s := range []string{"KEY", "TOKEN", "SECRET", "PASSWORD", "PASSWD", "CREDENTIAL"} {
		if strings.Contains(up, s) {
			return true
		}
	}
	return false
}

// CleanShellEnv returns the environment for a jailed command: the parent environment minus OPENCODE_* and
// any secret-looking variable, with HOME and TMPDIR forced to the writable /tmp (because /state is masked).
func CleanShellEnv(parent []string) []string {
	out := make([]string, 0, len(parent))
	home := ""
	hasPath := false
	for _, kv := range parent {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || sensitiveEnv(k) {
			continue
		}
		switch k {
		case "HOME":
			home = v
		case "TMPDIR":
			// re-added below
		default:
			if k == "PATH" {
				hasPath = true
			}
			out = append(out, kv)
		}
	}
	if home == "" || strings.HasPrefix(home, "/state") {
		home = "/tmp"
	}
	if !hasPath {
		out = append(out, "PATH=/usr/local/bin:/usr/bin:/bin")
	}
	return append(out, "HOME="+home, "TMPDIR=/tmp")
}

func unshareBinary() string {
	if p, err := exec.LookPath("unshare"); err == nil {
		return p
	}
	return "/usr/bin/unshare"
}

// IsJailedShellInvocation reports whether argv is OpenCode calling the configured shell ("<bin> -c <cmd>").
func IsJailedShellInvocation(args []string) bool {
	return len(args) >= 1 && args[0] == "-c"
}

// RunJailedShell is the entry point for `<ServerExePath> -c <cmd>`. It never returns on success: it execs
// unshare, which forks the command and propagates its exit status.
func RunJailedShell(args []string) error {
	var cmd string
	switch {
	case len(args) >= 2 && args[0] == "-c":
		cmd = args[1]
	case len(args) == 1 && args[0] != "-c":
		cmd = args[0]
	default:
		return errors.New("jailed shell: expected -c <command>")
	}
	lock := "nolock"
	if wd, err := os.Getwd(); err != nil || !strings.Contains(wd, "/"+WorktreeDir+"/") {
		// A read-mode session runs in the repository (or a subdirectory); an edit session runs inside its
		// own worktree under .p0ssess/wt/. Only read-mode tasks lock the worktrees read-only.
		lock = "lock"
	}
	ub := unshareBinary()
	argv := []string{ub, "-U", "-r", "-n", "-m", "-p", "--fork", "--kill-child", "--",
		"/bin/sh", "-c", jailShellInner, "p0ssess-shell", cmd, lock}
	return syscall.Exec(ub, argv, CleanShellEnv(os.Environ()))
}
