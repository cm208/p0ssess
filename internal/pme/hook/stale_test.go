package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"p0ssess/internal/proc"
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

// Without /proc (macOS, Windows), the server registry finds this session's older server by the Claude
// Code process both run under.
func TestStaleServerRegistry(t *testing.T) {
	d, _ := dirs(t)
	old := procRoot
	procRoot = filepath.Join(t.TempDir(), "none")
	defer func() { procRoot = old }()
	t.Setenv("CLAUDE_PID", "4242")
	undo := RegisterServer(d, "1.0.3")
	write := func(pid, claude int, v string) {
		b, _ := json.Marshal(serverEntry{PID: pid, Claude: claude, Version: v})
		os.WriteFile(filepath.Join(serversDir(d), strconv.Itoa(pid)+".json"), b, 0o600)
	}
	write(7001, 9999, "1.0.1") // another session's
	write(7002, 4242, "1.0.0") // this session's, but gone
	gone := map[int]bool{7002: true}
	alive = func(pid int) bool { return !gone[pid] }
	defer func() { alive = proc.Alive }()
	if got := registeredStale(d, 4242, "1.1.0"); got != "1.0.3" {
		t.Errorf("stale %q", got)
	}
	if _, err := os.Stat(filepath.Join(serversDir(d), "7002.json")); err == nil {
		t.Error("a gone server's entry stays")
	}
	Version, selfPID = "1.1.0", func() int { return 300 }
	defer func() { Version, selfPID = "", os.Getpid }()
	if n := updateNotice(d, "s9"); !strings.Contains(n, "still runs 1.0.3") {
		t.Errorf("notice %q", n)
	}
	undo()
	if got := registeredStale(d, 4242, "1.1.0"); got != "" {
		t.Errorf("after the server exits: %q", got)
	}
}
