package hook

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"p0ssess/internal/pme/paths"
)

// Version is this binary's version; main sets it.
var Version string

// procRoot is where process information lives, and selfPID this process; tests fake both.
var (
	procRoot = "/proc"
	selfPID  = os.Getpid
)

var binVersionRe = regexp.MustCompile(`/bin/(\d+\.\d+\.\d+[0-9A-Za-z.+-]*)/p0ssess-[a-z0-9]+-[a-z0-9]+`)

func ppidOf(pid int) int {
	b, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0
	}
	// pid (comm) state ppid ...; comm may hold spaces and parentheses.
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(f[1])
	return n
}

// staleServer returns the version of a p0ssess MCP server of this Claude Code session (a child of one of
// this process's ancestors) that is older than version, or "". Only Linux has the process table to look.
func staleServer(self int, version string) string {
	if version == "" {
		return ""
	}
	anc := map[int]bool{}
	for p, i := ppidOf(self), 0; p > 1 && i < 6; p, i = ppidOf(p), i+1 {
		anc[p] = true
	}
	if len(anc) == 0 {
		return ""
	}
	ents, err := os.ReadDir(procRoot)
	if err != nil {
		return ""
	}
	oldest := ""
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self || !anc[ppidOf(pid)] {
			continue
		}
		cmd, err := os.ReadFile(filepath.Join(procRoot, e.Name(), "cmdline"))
		if err != nil {
			continue
		}
		argv := strings.Split(strings.TrimRight(string(cmd), "\x00"), "\x00")
		if len(argv) < 2 || argv[1] != "mcp" {
			continue
		}
		exe, _ := os.Readlink(filepath.Join(procRoot, e.Name(), "exe"))
		m := binVersionRe.FindStringSubmatch(exe)
		if m == nil {
			m = binVersionRe.FindStringSubmatch(argv[0])
		}
		if m != nil && older(m[1], version) && (oldest == "" || older(m[1], oldest)) {
			oldest = m[1]
		}
	}
	return oldest
}

// older reports whether semantic version a comes before b.
func older(a, b string) bool {
	pa, pb := semver(a), semver(b)
	for i := 0; i < 3; i++ {
		if pa.n[i] != pb.n[i] {
			return pa.n[i] < pb.n[i]
		}
	}
	return pa.pre != "" && (pb.pre == "" || pa.pre < pb.pre)
}

type ver struct {
	n   [3]int
	pre string
}

func semver(s string) ver {
	var v ver
	s, _, _ = strings.Cut(s, "+")
	s, v.pre, _ = strings.Cut(s, "-")
	for i, p := range strings.SplitN(s, ".", 3) {
		v.n[i], _ = strconv.Atoi(p)
	}
	return v
}

// updateNotice is said once per session when a server older than the installed plugin still serves it:
// /reload-plugins leaves the running server on the old binary.
func updateNotice(d paths.Dirs, session string) string {
	if session == "" {
		return ""
	}
	old := staleServer(selfPID(), Version)
	if old == "" {
		return ""
	}
	mark := filepath.Join(filepath.Dir(d.SessionRulesFile(session)), "stale-"+old)
	if _, err := os.Stat(mark); err == nil {
		return ""
	}
	touch(mark)
	return "p0ssess updated to " + Version + " (this session still runs " + old + "): restart this Claude Code session to use it."
}
