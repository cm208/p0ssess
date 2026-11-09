package jail

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"p0ssess/internal/egress"
)

// This file is the jailed shell wrapper. OpenCode is configured with `shell: ServerExePath`, so for every
// command the model runs it spawns `<ServerExePath> -c <cmd>`; the host binary dispatches that to
// RunJailedShell. The wrapper:
//
//  1. Builds a clean environment: it drops OPENCODE_* and anything that looks like a key, token, secret or
//     password (so `echo $OPENCODE_PASSWORD` and the proxy's variables leak nothing) and points HOME and
//     TMPDIR at the writable private /tmp rather than the masked /state.
//  2. Enters fresh user, network, mount and pid namespaces (`unshare -U -r -n -m -p --fork`). The new
//     network namespace has nothing but loopback, so the OpenCode API, the key proxy, the LAN and the
//     internet are unreachable from a command. The new user namespace makes /proc/1/environ (the server's
//     own environment) unreadable and blocks ptrace of the server. In the new mount namespace the wrapper
//     overmounts /state (OpenCode's database) with an empty tmpfs.
//  3. Only when the user opted the project in to shell internet (the jail then sets EgressSocketEnv), starts
//     a forwarder inside the command's namespaces that relays 127.0.0.1:3128 to the egress proxy's Unix
//     socket, and sets HTTP(S)_PROXY. The proxy connects only to public addresses on ports 80 and 443.
//
// Without the opt-in, commands have no network: pip/npm/go module downloads fail. Offline, vendored builds
// and tests still work. A read session has nothing writable under the repository; an edit session's own
// worktree is its only writable mount there.

// jailShellInner runs inside the new namespaces as `/bin/sh -c <inner> p0ssess-shell <cmd> <socket>`.
// It masks /state, starts the egress forwarder when $2 names a socket, then execs the command.
const jailShellInner = `mount -t tmpfs -o size=1M,mode=000 tmpfs /state 2>/dev/null || true
if [ -n "$2" ]; then
  ` + ServerExePath + ` ` + NetForwardArg + ` "$2" </dev/null >/dev/null 2>&1 || echo "p0ssess: shell internet is unavailable for this command" >&2
fi
exec /bin/sh -c "$1"`

// NetForwardArg and NetServeArg are the hidden subcommands that start the egress forwarder inside a
// jailed command's namespaces.
const (
	NetForwardArg = "jail-net-forward"
	NetServeArg   = "jail-net-serve"
)

// sensitiveEnv reports whether an environment variable must be dropped before running a command.
func sensitiveEnv(name string) bool {
	up := strings.ToUpper(name)
	if strings.HasPrefix(up, "OPENCODE") || strings.HasPrefix(up, "P0SSESS") {
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
	env := CleanShellEnv(os.Environ())
	sock := ""
	if s := os.Getenv(EgressSocketEnv); s != "" {
		if _, err := os.Stat(s); err == nil {
			sock = s
			env = append(env, egress.ProxyEnv()...)
		}
	}
	ub := unshareBinary()
	argv := []string{ub, "-U", "-r", "-n", "-m", "-p", "--fork", "--kill-child", "--",
		"/bin/sh", "-c", jailShellInner, "p0ssess-shell", cmd, sock}
	return syscall.Exec(ub, argv, env)
}
