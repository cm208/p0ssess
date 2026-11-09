package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// launcherPlugin lays out a plugin with the real launcher and a stand-in binary in dist/.
func launcherPlugin(t *testing.T, sums func(sum string) string, ver string) (root, home string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the launcher is a POSIX shell script")
	}
	if _, err := os.Stat("/usr/local/go/bin/go"); err == nil {
		t.Skip("the launcher would find Go in /usr/local/go")
	}
	root, home = t.TempDir(), t.TempDir()
	b, err := os.ReadFile("../../bin/p0ssess")
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(root, "bin"), 0o755)
	os.WriteFile(filepath.Join(root, "bin", "p0ssess"), b, 0o755)
	name := "p0ssess-" + runtime.GOOS + "-" + runtime.GOARCH
	fake := []byte("#!/bin/sh\necho verified-binary-ran \"$@\"\n")
	os.MkdirAll(filepath.Join(root, "dist"), 0o755)
	os.WriteFile(filepath.Join(root, "dist", name), fake, 0o755)
	h := sha256.Sum256(fake)
	if sums != nil {
		os.WriteFile(filepath.Join(root, "dist", "SHA256SUMS"), []byte(sums(hex.EncodeToString(h[:]))+"  "+name+"\n"), 0o644)
	}
	os.WriteFile(filepath.Join(root, "dist", "VERSION"), []byte(ver+"\n"), 0o644)
	return root, home
}

// runLauncher runs it with no Go anywhere it looks, so only a checked binary can run.
func runLauncher(t *testing.T, root, home string, args ...string) (string, error) {
	cmd := exec.Command(filepath.Join(root, "bin", "p0ssess"), args...)
	path := []string{}
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if _, err := os.Stat(filepath.Join(d, "go")); err != nil {
			path = append(path, d)
		}
	}
	cmd.Env = []string{"HOME=" + home, "P0SSESS_HOME=" + filepath.Join(home, "p"), "PATH=" + strings.Join(path, string(os.PathListSeparator))}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestLauncherRunsOnlyCheckedBinaries(t *testing.T) {
	root, home := launcherPlugin(t, func(s string) string { return s }, version)
	out, err := runLauncher(t, root, home, "version")
	if err != nil || !strings.Contains(out, "verified-binary-ran version") {
		t.Fatalf("a binary matching dist/SHA256SUMS should run: %v\n%s", err, out)
	}
	cached, _ := filepath.Glob(filepath.Join(home, "p", "cache", "bin", version, "p0ssess-*"))
	if len(cached) != 1 {
		t.Errorf("the checked binary was not cached: %v", cached)
	}

	root, home = launcherPlugin(t, func(string) string { return strings.Repeat("0", 64) }, version)
	out, err = runLauncher(t, root, home, "version")
	if err == nil || strings.Contains(out, "verified-binary-ran") || !strings.Contains(out, "does not match dist/SHA256SUMS; refusing to run it") {
		t.Fatalf("a mismatching binary must not run: %v\n%s", err, out)
	}
	for _, want := range []string{"cannot start: no verified p0ssess " + version, "checksum mismatch (refused)",
		"Go is not installed", "install Go 1.27 or later", "P0SSESS_BIN"} {
		if !strings.Contains(out, want) {
			t.Errorf("failure message lacks %q:\n%s", want, out)
		}
	}

	// Checksums for another version do not count.
	root, home = launcherPlugin(t, func(s string) string { return s }, "0.0.1")
	out, err = runLauncher(t, root, home, "version")
	if err == nil || strings.Contains(out, "verified-binary-ran") || !strings.Contains(out, "no checksum for") {
		t.Fatalf("checksums for another version were trusted: %v\n%s", err, out)
	}

	// No sums at all, no Go, no release URL.
	root, home = launcherPlugin(t, nil, version)
	os.Remove(filepath.Join(root, "dist", "p0ssess-"+runtime.GOOS+"-"+runtime.GOARCH))
	out, err = runLauncher(t, root, home, "version")
	if err == nil || !strings.Contains(out, "not in this plugin") || !strings.Contains(out, "no dist/release-url") {
		t.Fatalf("missing everything: %v\n%s", err, out)
	}
}
