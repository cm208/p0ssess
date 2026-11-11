// Command launcher is bin/p0ssess.exe, the plugin's launcher on Windows, where Claude Code cannot run
// the shell script bin/p0ssess. It does what that script does: it starts the p0ssess binary for this
// version and platform, and runs only a binary it built from the plugin's own source or one whose
// SHA-256 matches dist/SHA256SUMS, found in this order:
//
//  1. $P0SSESS_BIN, if set (your own binary; not checked)
//  2. the cached copy under ~/.cache/p0ssess/bin/<version>/ (written only after a check or a build)
//  3. dist/p0ssess-<os>-<arch>.exe in the plugin (release archives ship these), checked
//  4. a release download from dist/release-url or $P0SSESS_RELEASE_URL, checked
//  5. a local build with Go (the source is the plugin; dependencies are vendored, so no network)
//
// It reads the version from .claude-plugin/plugin.json, so one launcher serves every release.
// "--ensure" prepares the binary and exits.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var tried []string

func note(s string) { tried = append(tried, s) }

func main() {
	if p := os.Getenv("P0SSESS_BIN"); p != "" {
		os.Exit(run(p, os.Args[1:]))
	}
	exe, err := os.Executable()
	if err != nil {
		fail(err)
	}
	root := filepath.Dir(filepath.Dir(exe))
	version, err := pluginVersion(root)
	if err != nil {
		fail(err)
	}
	name := "p0ssess-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	cache := cacheDir()
	bin := filepath.Join(cache, "bin", version, name)
	l := &launch{root: root, version: version, name: name, bin: bin}
	if !isFile(bin) {
		if err := l.ensure(); err != nil {
			os.Exit(1)
		}
	}
	if len(os.Args) > 1 && os.Args[1] == "--ensure" {
		return
	}
	os.Exit(run(bin, os.Args[1:]))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "p0ssess:", err)
	os.Exit(1)
}

type launch struct{ root, version, name, bin string }

func pluginVersion(root string) (string, error) {
	b, err := os.ReadFile(filepath.Join(root, ".claude-plugin", "plugin.json"))
	if err != nil {
		return "", fmt.Errorf("cannot read the plugin's version: %w", err)
	}
	var m struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(b, &m); err != nil || m.Version == "" {
		return "", errors.New("no version in .claude-plugin/plugin.json")
	}
	return m.Version, nil
}

func cacheDir() string {
	if h := os.Getenv("P0SSESS_HOME"); h != "" {
		return filepath.Join(h, "cache")
	}
	if v := os.Getenv("XDG_CACHE_HOME"); v != "" && filepath.IsAbs(v) {
		return filepath.Join(v, "p0ssess")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "p0ssess")
}

// firstLine reads a file's first line, without a trailing CR (a Windows checkout may add one).
func firstLine(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	s, _, _ := strings.Cut(string(b), "\n")
	return strings.TrimSpace(s)
}

// want returns the checksum dist/SHA256SUMS lists for this binary, when the file is for this version.
func (l *launch) want() (string, bool) {
	if firstLine(filepath.Join(l.root, "dist", "VERSION")) != l.version {
		return "", false
	}
	b, err := os.ReadFile(filepath.Join(l.root, "dist", "SHA256SUMS"))
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && (f[1] == l.name || f[1] == "*"+l.name) && len(f[0]) == 64 {
			if _, err := hex.DecodeString(f[0]); err == nil {
				return f[0], true
			}
		}
	}
	return "", false
}

func sha256File(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// installChecked copies src into the cache if its checksum matches; it never runs an unchecked file.
func (l *launch) installChecked(src, what string) bool {
	sum, ok := l.want()
	if !ok {
		note(what + ": no checksum for " + l.name + " " + l.version + " in dist/SHA256SUMS")
		return false
	}
	got, err := sha256File(src)
	if err != nil {
		note(what + ": " + err.Error())
		return false
	}
	if got != sum {
		fmt.Fprintf(os.Stderr, "p0ssess: %s does not match dist/SHA256SUMS; refusing to run it\n", what)
		note(what + ": checksum mismatch (refused)")
		return false
	}
	tmp := fmt.Sprintf("%s.tmp.%d", l.bin, os.Getpid())
	if err := copyFile(src, tmp); err != nil {
		note(what + ": " + err.Error())
		return false
	}
	if err := os.Rename(tmp, l.bin); err != nil {
		os.Remove(tmp)
		note(what + ": " + err.Error())
		return false
	}
	return true
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}

func (l *launch) fetchOrBuild() bool {
	if isFile(l.bin) {
		return true
	}
	local := filepath.Join(l.root, "dist", l.name)
	if isFile(local) {
		if l.installChecked(local, "dist/"+l.name) {
			return true
		}
	} else {
		note("dist/" + l.name + ": not in this plugin (a release archive ships it)")
	}
	url := os.Getenv("P0SSESS_RELEASE_URL")
	if url == "" {
		url = firstLine(filepath.Join(l.root, "dist", "release-url"))
	}
	url = strings.TrimRight(url, "/")
	switch _, ok := l.want(); {
	case url == "":
		note("release download: no dist/release-url in this plugin")
	case !ok:
		note("release download: no checksum for " + l.name + " " + l.version + " in dist/SHA256SUMS to check it against")
	default:
		src := url + "/v" + l.version + "/" + l.name
		tmp := fmt.Sprintf("%s.dl.%d", l.bin, os.Getpid())
		if curl, err := exec.LookPath("curl"); err != nil {
			note("release download: curl is not installed (Windows 10 and later ship it in System32)")
		} else if err := exec.Command(curl, "-fsSL", "-o", tmp, src).Run(); err != nil {
			note("release download: " + src + " could not be fetched")
		} else if l.installChecked(tmp, "the download from "+src) {
			os.Remove(tmp)
			return true
		}
		os.Remove(tmp)
	}
	if goBin := findGo(); goBin != "" {
		ver, _ := exec.Command(goBin, "env", "GOVERSION").Output()
		fmt.Fprintf(os.Stderr, "p0ssess: building %s %s with %s (first run only)\n", l.name, l.version, strings.TrimSpace(string(ver)))
		tmp := fmt.Sprintf("%s.tmp.%d", l.bin, os.Getpid())
		if runtime.GOOS == "windows" {
			tmp += ".exe"
		}
		cmd := exec.Command(goBin, "build", "-trimpath", "-buildvcs=false", "-mod=vendor",
			"-ldflags", "-s -w -buildid= -X main.version="+l.version, "-o", tmp, "./cmd/p0ssess")
		cmd.Dir = l.root
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		if cmd.Run() == nil && os.Rename(tmp, l.bin) == nil {
			return true
		}
		os.Remove(tmp)
		note("local build: " + goBin + " failed (p0ssess needs Go 1.27 or later)")
	} else {
		note("local build: Go is not installed (looked on PATH and in the usual install folders)")
	}
	fmt.Fprintf(os.Stderr, `p0ssess: cannot start: no verified p0ssess %s binary for %s/%s, and none could be built.
Tried:
  - %s
Fix one of these, then start a new Claude Code session:
  - install Go 1.27 or later (https://go.dev/dl/); the plugin builds itself offline in a few seconds
  - install a p0ssess release archive, which ships checked binaries in dist/
  - point P0SSESS_BIN at a p0ssess %s binary you built or checked yourself
See docs/INSTALL.md.
`, l.version, runtime.GOOS, runtime.GOARCH, strings.Join(tried, "\n  - "), l.version)
	return false
}

func findGo() string {
	if g, err := exec.LookPath("go"); err == nil {
		return g
	}
	home, _ := os.UserHomeDir()
	cands := []string{filepath.Join(home, ".local", "go", "bin", "go"), "/usr/local/go/bin/go"}
	if runtime.GOOS == "windows" {
		cands = []string{filepath.Join(os.Getenv("ProgramFiles"), "Go", "bin", "go.exe"), filepath.Join(home, "go", "bin", "go.exe")}
	}
	for _, c := range cands {
		if isFile(c) {
			return c
		}
	}
	return ""
}

// ensure prepares the binary under a lock directory, so two sessions starting at once do it once.
func (l *launch) ensure() error {
	if err := os.MkdirAll(filepath.Dir(l.bin), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "p0ssess:", err)
		return err
	}
	lock := l.bin + ".lock"
	for n := 0; os.Mkdir(lock, 0o700) != nil; n++ {
		if n > 600 {
			os.RemoveAll(lock)
			continue
		}
		time.Sleep(500 * time.Millisecond)
		if isFile(l.bin) {
			return nil
		}
	}
	defer os.RemoveAll(lock)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	go func() { <-stop; os.RemoveAll(lock); os.Exit(1) }()
	defer signal.Stop(stop)
	if !l.fetchOrBuild() {
		return errors.New("no binary")
	}
	return nil
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
