package hook

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func fakeProc(t *testing.T, root string, pid, ppid int, argv string, exe string) {
	t.Helper()
	d := filepath.Join(root, strconv.Itoa(pid))
	os.MkdirAll(d, 0o755)
	os.WriteFile(filepath.Join(d, "stat"), []byte(strconv.Itoa(pid)+" (some (name)) S "+strconv.Itoa(ppid)+" 1 1\n"), 0o644)
	os.WriteFile(filepath.Join(d, "cmdline"), []byte(argv), 0o644)
	if exe != "" {
		os.Symlink(exe, filepath.Join(d, "exe"))
	}
}

// A server of this session older than the installed plugin is noticed once (1.0.4 item 5).
func TestStaleServer(t *testing.T) {
	d, _ := dirs(t)
	root := t.TempDir()
	old := procRoot
	procRoot = root
	defer func() { procRoot = old }()
	// claude (100) -> sh (200) -> hook (300); claude -> mcp server 1.0.3 (400); another session's 1.0.1 (500).
	fakeProc(t, root, 100, 1, "claude\x00", "")
	fakeProc(t, root, 200, 100, "sh\x00-c\x00", "")
	fakeProc(t, root, 300, 200, "p0ssess\x00hook\x00prompt\x00", "")
	fakeProc(t, root, 400, 100, "/home/u/.cache/p0ssess/bin/1.0.3/p0ssess-linux-amd64\x00mcp\x00", "/home/u/.cache/p0ssess/bin/1.0.3/p0ssess-linux-amd64")
	fakeProc(t, root, 500, 900, "/home/u/.cache/p0ssess/bin/1.0.1/p0ssess-linux-amd64\x00mcp\x00", "")
	fakeProc(t, root, 900, 1, "claude\x00", "")
	if got := staleServer(300, "1.1.0"); got != "1.0.3" {
		t.Errorf("stale %q", got)
	}
	if got := staleServer(300, "1.0.3"); got != "" {
		t.Errorf("same version: %q", got)
	}
	if !older("1.1.0-rc.1", "1.1.0") || older("1.1.0", "1.0.9") || !older("1.0.3", "1.0.10") {
		t.Error("semver order")
	}
	Version, selfPID = "1.1.0", func() int { return 300 }
	defer func() { Version, selfPID = "", os.Getpid }()
	if n := updateNotice(d, "s1"); n == "" || !strings.Contains(n, "restart this Claude Code session") || !strings.Contains(n, "1.0.3") {
		t.Errorf("notice %q", n)
	}
	if n := updateNotice(d, "s1"); n != "" {
		t.Errorf("said twice: %q", n)
	}
}
