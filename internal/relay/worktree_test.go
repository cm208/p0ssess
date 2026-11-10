package relay

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// TestWorktreeDiffIgnoresTamperedGitdir checks that a worktree whose .git pointer, commondir and
// config.worktree have been rewritten to attacker-controlled config (as a delegated edit task could,
// the worktree being model-writable) cannot make the host relay run a program while it diffs. A
// core.fsmonitor / diff.external set in that config must not fire.
func TestWorktreeDiffIgnoresTamperedGitdir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q", repo)
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-qm", "base")
	base, err := git(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	base = trim(base)

	wt, _, err := addWorktree(repo)
	if err != nil {
		t.Fatal(err)
	}

	// A fired marker proves host code execution.
	marker := filepath.Join(root, "FIRED")
	payload := "touch " + marker + "; false"

	// 1. Redirect the worktree's .git pointer to an attacker dir with a malicious config.
	fake := filepath.Join(wt, "fake")
	if err := os.MkdirAll(fake, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "[core]\n\tfsmonitor = \"" + payload + "\"\n[diff]\n\texternal = \"" + payload + "\"\n"
	must(t, os.WriteFile(filepath.Join(fake, "config"), []byte(cfg), 0o644))
	must(t, os.WriteFile(filepath.Join(fake, "HEAD"), []byte("ref: refs/heads/none\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+fake+"\n"), 0o644))

	// 2. Tamper the real per-worktree admin dir too (it is model-writable): a hostile commondir and a
	//    config.worktree that turns on worktreeConfig and sets an external diff.
	admin := filepath.Join(repo, ".git", "worktrees", filepath.Base(wt))
	evil := filepath.Join(root, "evilcommon")
	must(t, os.MkdirAll(evil, 0o755))
	must(t, os.WriteFile(filepath.Join(evil, "config"), []byte("[diff]\n\texternal = \""+payload+"\"\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(admin, "commondir"), []byte(evil+"\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(admin, "config.worktree"),
		[]byte("[extensions]\n\tworktreeConfig = true\n[diff]\n\texternal = \""+payload+"\"\n"), 0o644))

	// A real change so the diff has something to do.
	must(t, os.WriteFile(filepath.Join(wt, "tracked.txt"), []byte("changed\n"), 0o644))

	stat, patch, err := worktreeDiff(repo, wt, base)
	if err != nil {
		t.Fatalf("worktreeDiff: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("host code executed: the tampered worktree git config fired")
	}
	if stat == "" && patch == "" {
		t.Fatal("expected a non-empty diff for the changed file")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func trim(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	return s
}

// TestWorktreeFilesListsOnlyRealMediaInside: an edit run's new images and documents are listed for
// Claude to present; source files, and symlinks the model planted to reach files outside, are not.
func TestWorktreeFilesListsOnlyRealMediaInside(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	os.MkdirAll(repo, 0o755)
	runGit(t, repo, "init", "-q", repo)
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a\n"), 0o644)
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-qm", "base")
	base, _ := git(repo, "rev-parse", "HEAD")
	base = trim(base)
	wt, _, err := addWorktree(repo)
	if err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(root, "secret.png")
	os.WriteFile(secret, []byte("x"), 0o600)
	os.MkdirAll(filepath.Join(wt, "out"), 0o755)
	os.WriteFile(filepath.Join(wt, "out", "chart.png"), []byte("png"), 0o644)
	os.WriteFile(filepath.Join(wt, "report.docx"), []byte("docx"), 0o644)
	os.WriteFile(filepath.Join(wt, "main.go"), []byte("package main\n"), 0o644)
	os.Symlink(secret, filepath.Join(wt, "leak.png"))
	if _, _, err := worktreeDiff(repo, wt, base); err != nil {
		t.Fatal(err)
	}
	got := worktreeFiles(repo, wt, base)
	if len(got) != 2 || got[0].Path != filepath.Join(wt, "out", "chart.png") || got[0].Kind != "image" ||
		got[1].Path != filepath.Join(wt, "report.docx") || got[1].Kind != "document" {
		t.Errorf("files = %+v", got)
	}
}
