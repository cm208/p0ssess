package jail

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"p0ssess/internal/config"
)

// Go and Node toolchains from the host are borrowed read-only; what cannot run is said up front
// (1.0.4 item E).
func TestToolchains(t *testing.T) {
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("HOME", filepath.Join(tmp, "home"))
	goRoot := filepath.Join(tmp, "sdk", "go")
	os.MkdirAll(filepath.Join(goRoot, "bin"), 0o755)
	nvm := filepath.Join(tmp, "home", ".nvm", "versions", "node", "v24", "bin")
	os.MkdirAll(nvm, 0o755)
	os.WriteFile(filepath.Join(nvm, "node"), nil, 0o755)
	SetLookTool(func(name string) (string, error) {
		switch name {
		case "go":
			return filepath.Join(goRoot, "bin", "go"), nil
		case "node":
			return filepath.Join(nvm, "node"), nil
		}
		return "", errors.New("not found")
	}, func(string, ...string) (string, error) { return goRoot + "\n" + filepath.Join(tmp, "gomod"), nil })
	defer SetLookTool(nil, nil)
	os.MkdirAll(filepath.Join(tmp, "gomod"), 0o755)

	repo := filepath.Join(tmp, "repo")
	os.MkdirAll(repo, 0o755)
	os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module x\n"), 0o644)
	web := filepath.Join(tmp, "web")
	os.MkdirAll(web, 0o755)
	os.WriteFile(filepath.Join(web, "package.json"), []byte(`{"scripts":{"test":"vitest"}}`), 0o644)
	rs := filepath.Join(tmp, "rs")
	os.MkdirAll(rs, 0o755)
	os.WriteFile(filepath.Join(rs, "Cargo.toml"), nil, 0o644)

	found, missing := Toolchains(repo, web, rs)
	if len(found) != 2 || found[0].Kind != "go" || found[1].Kind != "node" || len(missing) != 1 || !strings.Contains(missing[0], "Rust") {
		t.Fatalf("found %+v missing %v", found, missing)
	}
	args := strings.Join(toolArgs(config.Config{SELinux: "disable"}, found), " ")
	for _, want := range []string{goRoot + ":" + toolRoot + "/go:ro", "GOPROXY=off", "PATH=" + toolRoot + "/go/bin:" + toolRoot + "/node/bin:", "GOMODCACHE="} {
		if !strings.Contains(args, want) {
			t.Errorf("args lack %q: %s", want, args)
		}
	}
	// A system install under /usr cannot be shared: said, not mounted.
	SetLookTool(func(string) (string, error) { return "/usr/bin/env", nil }, nil)
	if _, missing := Toolchains(web); len(missing) != 1 || !strings.Contains(missing[0], "cannot be shared") {
		t.Errorf("system node: %v", missing)
	}
}

// The check after an edit runs the tests with no network on a throwaway overlay of the copy.
func TestRunTests(t *testing.T) {
	old := runTests
	defer func() { runTests = old }()
	var got []string
	runTests = func(_ context.Context, args []string) (string, int, error) {
		got = args
		return "--- FAIL: TestX\nFAIL", 1, nil
	}
	tr := RunTests(context.Background(), config.Config{}, "/w/copy", Toolchain{Kind: "go", Test: "go test ./..."}, nil, time.Minute)
	if tr.Passed || !strings.Contains(tr.Tail, "FAIL") {
		t.Errorf("%+v", tr)
	}
	a := strings.Join(got, " ")
	if !strings.Contains(a, "--network none") || !strings.Contains(a, "/w/copy:/w/copy:O") || !strings.Contains(a, "go test ./...") {
		t.Errorf("args %s", a)
	}
}

// Every repository of a run is mounted read-only with other sessions' worktrees hidden, and its own
// worktree read-write.
func TestRepoMountArgs(t *testing.T) {
	tmp := t.TempDir()
	os.MkdirAll(filepath.Join(tmp, ".p0ssess"), 0o755)
	os.MkdirAll(filepath.Join(tmp, ".git", "worktrees"), 0o755)
	a := strings.Join(repoMountArgs(config.Config{SELinux: "disable"}, Mount{Repo: tmp, Worktree: filepath.Join(tmp, ".p0ssess/wt/w1")}), " ")
	for _, want := range []string{tmp + ":" + tmp + ":ro", "destination=" + filepath.Join(tmp, ".p0ssess"), filepath.Join(tmp, ".p0ssess/wt/w1") + ":", ".git/worktrees/w1:"} {
		if !strings.Contains(a, want) {
			t.Errorf("lacks %q: %s", want, a)
		}
	}
}
