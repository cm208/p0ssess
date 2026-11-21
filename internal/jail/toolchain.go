package jail

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"p0ssess/internal/config"
)

// toolRoot is where host toolchains appear inside a jail, read-only.
const toolRoot = "/opt/p0ssess-tc"

// Toolchain is a host toolchain a jail borrows read-only, so a project's own tests run inside it.
type Toolchain struct {
	Kind string `json:"kind"`
	// Mounts maps host directories to their place under toolRoot.
	Mounts map[string]string `json:"mounts,omitempty"`
	Env    []string          `json:"env,omitempty"`
	Path   []string          `json:"path,omitempty"`
	// Test is the project's test command, for the check after an edit run.
	Test string `json:"test,omitempty"`
}

// lookTool finds a host program; tests replace it.
var lookTool = exec.LookPath

// toolOutput runs a host program for its output; tests replace it.
var toolOutput = func(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	return strings.TrimSpace(string(out)), err
}

func fileIn(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

// borrowable refuses host directories a jail must not see: the root, /usr, a home directory or anything
// with credentials at its top.
func borrowable(dir string) bool {
	c, err := Canonical(dir)
	if err != nil || c == "/" || c == "/usr" || c == "/usr/local" || c == "/opt" {
		return false
	}
	if h, err := os.UserHomeDir(); err == nil {
		if home, err := Canonical(h); err == nil && (c == home || inside(c, home)) {
			return false
		}
	}
	for _, n := range secretNames {
		if _, err := os.Lstat(filepath.Join(c, n)); err == nil {
			return false
		}
	}
	return true
}

// Toolchains finds what the repositories' projects need to build and test (go.mod, package.json,
// Cargo.toml, pyproject.toml) and the host toolchain the jail can borrow for each. missing says, one
// line each, what cannot run in the jail and why, so the caller knows before anything is spent.
func Toolchains(repos ...string) (found []Toolchain, missing []string) {
	kinds := map[string]bool{}
	tests := map[string]string{}
	for _, r := range repos {
		switch {
		case fileIn(r, "go.mod"):
			kinds["go"] = true
		case fileIn(r, "Cargo.toml"):
			kinds["rust"] = true
		case fileIn(r, "package.json"):
			kinds["node"] = true
			if b, err := os.ReadFile(filepath.Join(r, "package.json")); err == nil && bytes.Contains(b, []byte(`"test"`)) {
				tests["node"] = "npm test --silent"
			}
		case fileIn(r, "pyproject.toml") || fileIn(r, "setup.py") || fileIn(r, "pytest.ini"):
			kinds["python"] = true
		}
	}
	if kinds["go"] {
		if tc, why := goToolchain(); why == "" {
			found = append(found, tc)
		} else {
			missing = append(missing, why)
		}
	}
	if kinds["node"] {
		if tc, why := nodeToolchain(tests["node"]); why == "" {
			found = append(found, tc)
		} else {
			missing = append(missing, why)
		}
	}
	if kinds["rust"] {
		missing = append(missing, "Rust tests cannot run in the sandbox (it has no C linker for cargo)")
	}
	if kinds["python"] {
		// python3 is in the jail image; packages outside the repository are not.
		found = append(found, Toolchain{Kind: "python", Test: "python3 -m pytest -q 2>/dev/null || python3 -m unittest discover -q"})
	}
	return found, missing
}

func goToolchain() (Toolchain, string) {
	bin, err := lookTool("go")
	if err != nil {
		for _, c := range []string{"/usr/local/go/bin/go", filepath.Join(os.Getenv("HOME"), ".local", "go", "bin", "go")} {
			if fileIn(filepath.Dir(c), "go") {
				bin, err = c, nil
				break
			}
		}
	}
	if err != nil {
		return Toolchain{}, "Go tests cannot run in the sandbox: Go is not installed on this machine"
	}
	out, err := toolOutput(bin, "env", "GOROOT", "GOMODCACHE")
	lines := strings.Split(out, "\n")
	if err != nil || len(lines) < 1 || lines[0] == "" {
		return Toolchain{}, "Go tests cannot run in the sandbox: `go env` failed on this machine"
	}
	root := strings.TrimSpace(lines[0])
	if !borrowableTool(root) {
		return Toolchain{}, "Go tests cannot run in the sandbox: the Go install at " + root + " cannot be shared read-only"
	}
	tc := Toolchain{Kind: "go", Mounts: map[string]string{root: toolRoot + "/go"}, Path: []string{toolRoot + "/go/bin"},
		Env:  []string{"GOTOOLCHAIN=local", "GOCACHE=/tmp/go-build", "GOPATH=/tmp/go", "GOPROXY=off", "GOSUMDB=off", "GOTELEMETRY=off"},
		Test: "go test ./..."}
	if len(lines) > 1 {
		if mc := strings.TrimSpace(lines[1]); mc != "" && borrowableTool(mc) {
			tc.Mounts[mc] = toolRoot + "/gomod"
			tc.Env = append(tc.Env, "GOMODCACHE="+toolRoot+"/gomod")
		}
	}
	return tc, ""
}

func nodeToolchain(test string) (Toolchain, string) {
	bin, err := lookTool("node")
	if err != nil {
		return Toolchain{}, "Node tests cannot run in the sandbox: Node is not installed on this machine"
	}
	real, err := filepath.EvalSymlinks(bin)
	if err != nil {
		return Toolchain{}, "Node tests cannot run in the sandbox: " + err.Error()
	}
	prefix := filepath.Dir(filepath.Dir(real))
	if !borrowableTool(prefix) {
		return Toolchain{}, "Node tests cannot run in the sandbox: the system Node at " + real + " cannot be shared (a version manager install such as nvm can)"
	}
	if test == "" {
		test = "npm test --silent"
	}
	return Toolchain{Kind: "node", Mounts: map[string]string{prefix: toolRoot + "/node"}, Path: []string{toolRoot + "/node/bin"},
		Env: []string{"npm_config_cache=/tmp/npm", "npm_config_offline=true", "npm_config_update_notifier=false"}, Test: test}, ""
}

// borrowableTool is borrowable, except that a toolchain inside the home directory (~/.local/go,
// ~/go/pkg/mod, ~/.nvm/...) is fine as long as it is not the home directory itself.
func borrowableTool(dir string) bool {
	c, err := Canonical(dir)
	if err != nil {
		return false
	}
	if h, err := os.UserHomeDir(); err == nil {
		if home, err := Canonical(h); err == nil && inside(home, c) {
			for _, n := range secretNames {
				if _, err := os.Lstat(filepath.Join(c, n)); err == nil {
					return false
				}
			}
			return true
		}
	}
	return borrowable(c)
}

const basePath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// toolArgs are the podman arguments that mount the toolchains and set their environment.
func toolArgs(cfg config.Config, tcs []Toolchain) []string {
	var args, path []string
	for _, tc := range tcs {
		for host, at := range tc.Mounts {
			args = append(args, "-v", host+":"+at+":"+mountOpt(cfg, "ro"))
		}
		for _, e := range tc.Env {
			args = append(args, "-e", e)
		}
		path = append(path, tc.Path...)
	}
	if len(path) > 0 {
		args = append(args, "-e", "PATH="+strings.Join(path, ":")+":"+basePath)
	}
	return args
}

// TestRun is the check after an edit run: the project's tests in a fresh container with no network.
type TestRun struct {
	Command string `json:"command"`
	Passed  bool   `json:"passed"`
	Tail    string `json:"tail,omitempty"`
	Skipped string `json:"skipped,omitempty"`
}

// runTests runs podman for RunTests; tests replace it.
var runTests = func(ctx context.Context, args []string) (string, int, error) {
	cmd := exec.CommandContext(ctx, "podman", args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		return out.String(), ee.ExitCode(), nil
	}
	return out.String(), 0, err
}

// RunTests runs tc's test command in dir, in a container of the jail image with no network, the
// toolchains read-only and dir as a throwaway overlay, so the tests change nothing that is reported.
func RunTests(ctx context.Context, cfg config.Config, dir string, tc Toolchain, all []Toolchain, timeout time.Duration) TestRun {
	tr := TestRun{Command: tc.Test}
	if tc.Test == "" {
		tr.Skipped = "no test command for " + tc.Kind
		return tr
	}
	image := cfg.Image
	if image == "" {
		image = DefaultImage
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	inner := "cd " + shq(dir) + " && { " + tc.Test + " ; } >/tmp/p0ssess-test.out 2>&1; s=$?; tail -n 25 /tmp/p0ssess-test.out; exit $s"
	args := []string{"run", "--rm", "--network", "none", "--userns=keep-id", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--security-opt", "label=disable", "--pids-limit", "2048", "--tmpfs", "/tmp:rw,size=2g,mode=1777",
		"-e", "HOME=/tmp", "-v", dir + ":" + dir + ":O"}
	args = append(args, toolArgs(config.Config{SELinux: "disable"}, all)...)
	args = append(args, "--label", "p0ssess=1", image, "sh", "-c", inner)
	out, code, err := runTests(ctx, args)
	switch {
	case ctx.Err() != nil:
		tr.Skipped = fmt.Sprintf("the tests did not finish within %s", timeout)
	case err != nil:
		tr.Skipped = "the test container did not start"
	default:
		tr.Passed = code == 0
		tr.Tail = strings.TrimSpace(out)
		if len(tr.Tail) > 1500 {
			tr.Tail = "…" + tr.Tail[len(tr.Tail)-1500:]
		}
	}
	return tr
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// SetLookTool replaces how host toolchains are found, for tests; nil restores the defaults.
func SetLookTool(look func(string) (string, error), out func(string, ...string) (string, error)) {
	if look == nil {
		look = exec.LookPath
	}
	if out == nil {
		out = func(name string, args ...string) (string, error) {
			b, err := exec.Command(name, args...).Output()
			return strings.TrimSpace(string(b)), err
		}
	}
	lookTool, toolOutput = look, out
}
