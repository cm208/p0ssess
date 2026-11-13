package cloudsandbox

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func gitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(gitEnv(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-q")
	for p, body := range files {
		full := filepath.Join(dir, p)
		os.MkdirAll(filepath.Dir(full), 0o755)
		if strings.HasPrefix(body, "->") {
			if err := os.Symlink(strings.TrimPrefix(body, "->"), full); err != nil {
				t.Fatal(err)
			}
			continue
		}
		mode := os.FileMode(0o644)
		if strings.HasPrefix(body, "#!") {
			mode = 0o755
		}
		os.WriteFile(full, []byte(body), mode)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "x")
	return dir
}

func readUpload(t *testing.T, b []byte) map[string]*tar.Header {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	out := map[string]*tar.Header{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out[h.Name] = h
	}
}

func names(m map[string]*tar.Header) []string {
	var s []string
	for k := range m {
		s = append(s, k)
	}
	sort.Strings(s)
	return s
}

func TestUploadTrackedFilesOnly(t *testing.T) {
	dir := gitRepo(t, map[string]string{
		"main.go":            "package main",
		"run.sh":             "#!/bin/sh\necho hi",
		"docs/a.md":          "# a",
		".env":               "TOKEN=x",
		"config/secrets.yml": "x",
		"keys.go":            "package keys",
		"secretary.go":       "package s",
		"link":               "->docs/a.md",
		"bad":                "->../outside",
		"abs":                "->/etc/passwd",
	})
	os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("no"), 0o644)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main // edited"), 0o644)

	var buf bytes.Buffer
	m, err := WriteUpload(dir, &buf)
	if err != nil {
		t.Fatal(err)
	}
	got := readUpload(t, buf.Bytes())
	want := []string{"docs/a.md", "keys.go", "link", "main.go", "run.sh", "secretary.go"}
	if strings.Join(names(got), ",") != strings.Join(want, ",") {
		t.Fatalf("uploaded %v, want %v", names(got), want)
	}
	if got["run.sh"].Mode != 0o755 || got["main.go"].Mode != 0o644 {
		t.Errorf("modes: run.sh %o main.go %o", got["run.sh"].Mode, got["main.go"].Mode)
	}
	if got["main.go"].Size != int64(len("package main // edited")) {
		t.Error("a read upload carries the checkout's contents")
	}
	if got["link"].Typeflag != tar.TypeSymlink || got["link"].Linkname != "docs/a.md" {
		t.Errorf("symlink: %+v", got["link"])
	}
	if strings.Join(m.Secrets, ",") != ".env,config/secrets.yml" {
		t.Errorf("secrets: %v", m.Secrets)
	}
	if len(m.Skipped) != 2 || !strings.Contains(m.Note(), "look like secrets") {
		t.Errorf("skipped %v note %q", m.Skipped, m.Note())
	}
}

func TestUploadRefusesPrivateRepo(t *testing.T) {
	dir := gitRepo(t, map[string]string{"a.go": "x", "sub/.p0ssess-private": ""})
	var buf bytes.Buffer
	if _, err := WriteUpload(dir, &buf); !errors.Is(err, ErrPrivate) {
		t.Fatalf("err = %v", err)
	}
	if buf.Len() != 0 {
		t.Error("nothing may be written for a private repository")
	}
	dir = gitRepo(t, map[string]string{"a.go": "x"})
	os.WriteFile(filepath.Join(dir, PrivateMarker), nil, 0o644) // untracked marker at the top counts too
	if _, err := WriteUpload(dir, &buf); !errors.Is(err, ErrPrivate) {
		t.Fatalf("untracked marker: err = %v", err)
	}
}

func TestUploadDoesNotFollowSymlinkedDirectory(t *testing.T) {
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "id"), []byte("PRIVATE"), 0o644)
	dir := gitRepo(t, map[string]string{"d/id": "public"})
	os.RemoveAll(filepath.Join(dir, "d"))
	os.Symlink(outside, filepath.Join(dir, "d"))
	var buf bytes.Buffer
	if _, err := WriteUpload(dir, &buf); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(buf.Bytes(), []byte("PRIVATE")) {
		t.Fatal("a file outside the repository was uploaded through a symlinked directory")
	}
	gz, _ := gzip.NewReader(&buf)
	raw, _ := io.ReadAll(gz)
	if bytes.Contains(raw, []byte("PRIVATE")) {
		t.Fatal("a file outside the repository was uploaded through a symlinked directory")
	}
}

func TestUploadTooLarge(t *testing.T) {
	dir := gitRepo(t, map[string]string{"a": "x"})
	f, _ := os.Create(filepath.Join(dir, "big.bin"))
	f.Truncate(MaxUploadBytes + 1)
	f.Close()
	cmd := exec.Command("git", "-C", dir, "add", "big.bin")
	cmd.Env = gitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	var buf bytes.Buffer
	var tl *TooLargeError
	if _, err := WriteUpload(dir, &buf); !errors.As(err, &tl) {
		t.Fatalf("err = %v", err)
	}
	if buf.Len() != 0 {
		t.Error("an oversized upload must fail before writing")
	}
}

func TestSecretPath(t *testing.T) {
	for _, p := range []string{".env", ".env.local", ".ENVRC", "id_ed25519.pub", "server.pem", "tls/server.key",
		"client-secret.json", "secrets.yaml", "a/.ssh/config", ".aws/credentials", "credentials.json", ".npmrc", "htpasswd"} {
		if !SecretPath(p) {
			t.Errorf("%s should look like a secret", p)
		}
	}
	for _, p := range []string{"secretary.go", "keys.go", "monkey.py", "environment.ts", "env/README", "keyboard.c"} {
		if SecretPath(p) {
			t.Errorf("%s should not look like a secret", p)
		}
	}
}
