package relay

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"p0ssess/internal/jail"
)

// Target is where a run works: the repository its session starts in, the other repositories it needs,
// files it names outside every repository, and one line per choice made for the caller.
type Target struct {
	Cwd   string
	Repo  string
	Extra []string
	Files []string
	// Dest, with no Repo, is the directory the task names for files it writes: a named directory, or
	// the existing parent of a named file that does not exist yet.
	Dest  string
	Notes []string
	// Named is set when the repository came from a path the task names, not from the caller's directory.
	Named bool
}

// maxExtraRepos bounds how many more repositories one run mounts.
const maxExtraRepos = 3

var (
	pathRe    = regexp.MustCompile("(?:^|[\\s\"'`(<\\[=:,])((?:~|/)[^\\s\"'`<>()\\[\\],;]*)")
	wordRe    = regexp.MustCompile(`[A-Za-z0-9][A-Za-z0-9._-]{2,}`)
	repoWord  = regexp.MustCompile(`(?i)\b(repo|repos|repository|repositories|project|codebase|checkout|clone|module|package)\b`)
	textNamed = map[string]bool{".md": true, ".txt": true, ".toml": true, ".yaml": true, ".yml": true, ".json": true, "": true}
)

// taskPaths returns the absolute paths a text names, ~ expanded, in order, without trailing punctuation.
func taskPaths(s string) []string {
	home, _ := os.UserHomeDir()
	var out []string
	seen := map[string]bool{}
	for _, m := range pathRe.FindAllStringSubmatch(s, -1) {
		p := strings.TrimRight(m[1], ".:!?'\"")
		if p == "" || p == "/" {
			continue
		}
		if strings.HasPrefix(p, "~") {
			if home == "" || !(p == "~" || strings.HasPrefix(p, "~/")) {
				continue
			}
			p = filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
		p = filepath.Clean(p)
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// existing is p, or its nearest existing parent for a file still to be written, resolved.
func existing(p string) (string, bool) {
	for i := 0; i < 3 && p != "/" && p != "."; i++ {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r, true
		}
		p = filepath.Dir(p)
	}
	return "", false
}

// repoOf is the mountable git repository holding p, or "".
func repoOf(p, keyFile string) string {
	e, ok := existing(p)
	if !ok {
		return ""
	}
	dir := e
	if st, err := os.Stat(e); err == nil && !st.IsDir() {
		dir = filepath.Dir(e)
	}
	r, err := jail.RepoRoot(dir)
	if err != nil || jail.CheckMountable(r, keyFile) != nil {
		return ""
	}
	return r
}

// namedInFiles reads the small text files a task names (a spec, a plan) for the paths they mention.
func namedInFiles(files []string) []string {
	var out []string
	for _, f := range files {
		st, err := os.Stat(f)
		if err != nil || !st.Mode().IsRegular() || st.Size() > 256<<10 || !textNamed[strings.ToLower(filepath.Ext(f))] {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil || bytes.IndexByte(b, 0) >= 0 {
			continue
		}
		ps := taskPaths(string(b))
		if len(ps) > 40 {
			ps = ps[:40]
		}
		out = append(out, ps...)
	}
	return out
}

func isRepoDir(d string) bool {
	_, err := os.Stat(filepath.Join(d, ".git"))
	return err == nil
}

// nearbyRepos lists the git repositories next to cwd, under it and in the home directory (two levels),
// at most a few hundred directories looked at.
func nearbyRepos(cwd string) []string {
	home, _ := os.UserHomeDir()
	var roots []string
	for _, r := range []string{cwd, filepath.Dir(cwd), home} {
		if r != "" && r != "/" {
			roots = append(roots, r)
		}
	}
	seen := map[string]bool{}
	var out []string
	budget := 400
	var scan func(dir string, depth int)
	scan = func(dir string, depth int) {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range ents {
			if budget <= 0 {
				return
			}
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			budget--
			p := filepath.Join(dir, e.Name())
			if seen[p] {
				continue
			}
			seen[p] = true
			if isRepoDir(p) {
				out = append(out, p)
			} else if depth > 1 {
				scan(p, depth-1)
			}
		}
	}
	for _, r := range roots {
		d := 1
		if r == home {
			d = 2
		}
		scan(r, d)
	}
	return out
}

// namedRepos are nearby repositories whose directory name the task uses: as a distinctive word (with a
// digit, dot or dash) or next to "repo", "project" and the like.
func namedRepos(task, cwd, keyFile string) []string {
	words := map[string]int{}
	for _, loc := range wordRe.FindAllStringIndex(task, -1) {
		w := strings.ToLower(task[loc[0]:loc[1]])
		if _, ok := words[w]; !ok {
			words[w] = loc[0]
		}
	}
	var out []string
	for _, r := range nearbyRepos(cwd) {
		name := strings.ToLower(filepath.Base(r))
		at, ok := words[name]
		if !ok || len(name) < 4 {
			continue
		}
		distinct := strings.ContainsAny(name, "0123456789.-_")
		lo, hi := max(at-40, 0), min(at+len(name)+40, len(task))
		if !distinct && !repoWord.MatchString(task[lo:hi]) {
			continue
		}
		if c, err := jail.Canonical(r); err == nil && jail.CheckMountable(c, keyFile) == nil {
			out = append(out, c)
		}
	}
	return out
}

func addUnique(list []string, s ...string) []string {
	for _, v := range s {
		dup := v == ""
		for _, have := range list {
			if have == v {
				dup = true
			}
		}
		if !dup {
			list = append(list, v)
		}
	}
	return list
}

// Resolve picks where a run works. The repository of the first path the task names wins over the
// caller's directory, unless the caller passed cwd on purpose; every other repository the task names
// (directly, or in a spec file it names) is mounted too. With no path, the caller's repository is used,
// then a nearby repository the task names. Repo is "" when nothing fits: the caller then gets a scratch
// repository (or the direct path for read-only work), never a refusal.
func Resolve(task, cwd string, explicit bool, keyFile string, attachments []string) Target {
	t := Target{}
	if c, err := jail.Canonical(cwd); err == nil {
		cwd = c
	}
	named := append(taskPaths(task), attachments...)
	var direct []string
	for _, p := range named {
		if r := repoOf(p, keyFile); r != "" {
			direct = addUnique(direct, r)
		} else if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			t.Files = addUnique(t.Files, p)
		}
	}
	var second []string
	for _, p := range namedInFiles(named) {
		if r := repoOf(p, keyFile); r != "" {
			second = addUnique(second, r)
		}
	}
	cwdRepo := repoOf(cwd, keyFile)
	switch {
	case explicit && cwdRepo != "":
		t.Repo = cwdRepo
	case len(direct) > 0:
		t.Repo, t.Named = direct[0], true
		if cwdRepo != "" && cwdRepo != t.Repo {
			t.Notes = append(t.Notes, fmt.Sprintf("worked in %s, which the task names, not %s", t.Repo, cwdRepo))
		} else if cwdRepo == "" {
			t.Notes = append(t.Notes, fmt.Sprintf("worked in %s, which the task names (%s is not a git repository)", t.Repo, cwd))
		}
	case cwdRepo != "":
		t.Repo = cwdRepo
	case len(second) > 0:
		t.Repo, t.Named = second[0], true
		t.Notes = append(t.Notes, fmt.Sprintf("worked in %s, which a file in the task names (%s is not a git repository)", t.Repo, cwd))
	default:
		if rs := namedRepos(task, cwd, keyFile); len(rs) > 0 {
			t.Repo = rs[0]
			note := fmt.Sprintf("picked %s: the task names it", t.Repo)
			if len(rs) > 1 {
				note += " (also matched " + strings.Join(rs[1:], ", ") + ")"
			}
			t.Notes = append(t.Notes, note)
		}
	}
	if t.Repo == "" {
		t.Cwd = cwd
		t.Dest = scratchDest(named)
		return t
	}
	for _, r := range addUnique(append(append([]string{}, direct...), second...)) {
		if r != t.Repo && len(t.Extra) < maxExtraRepos && !inside(t.Repo, r) && !inside(r, t.Repo) {
			t.Extra = append(t.Extra, r)
		}
	}
	if len(t.Extra) > 0 {
		t.Notes = append(t.Notes, "also mounted "+strings.Join(t.Extra, ", "))
	}
	t.Cwd = t.Repo
	if cwdRepo == t.Repo && cwd != "" {
		t.Cwd = cwd
	}
	return t
}

// scratchDest is the first named directory outside any repository, or the existing parent of a named
// file still to be written; never / or the home directory itself.
func scratchDest(named []string) string {
	home, _ := os.UserHomeDir()
	for _, p := range named {
		d := p
		st, err := os.Stat(p)
		switch {
		case err == nil && st.IsDir():
		case errors.Is(err, os.ErrNotExist):
			d = filepath.Dir(p)
			if st, err := os.Stat(d); err != nil || !st.IsDir() {
				continue
			}
		default:
			continue
		}
		if c, err := jail.Canonical(d); err == nil {
			d = c
		}
		if d == "/" || d == home || home != "" && d == filepath.Clean(home) || isRepoDir(d) {
			continue
		}
		return d
	}
	return ""
}

// inside reports whether p is dir or below it.
func inside(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}
