package relay

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRewritePaths(t *testing.T) {
	cases := []struct{ in, want string }{
		{"write tests for /w/repo/src/a.py and /w/repo/src/b.py", "write tests for /w/wt/src/a.py and /w/wt/src/b.py"},
		{"run it in /w/repo.", "run it in /w/wt."},
		{"cd /w/repo && make", "cd /w/wt && make"},
		{"see `/w/repo/tests/`", "see `/w/wt/tests/`"},
		{"/w/repository/x and /x/w/repo/y stay", "/w/repository/x and /x/w/repo/y stay"},
		{"the worktree /w/repo/.p0ssess/wt/w1/a.py stays", "the worktree /w/repo/.p0ssess/wt/w1/a.py stays"},
		{"no paths here", "no paths here"},
	}
	for _, c := range cases {
		got, changed := rewritePaths(c.in, "/w/repo", "/w/wt")
		if got != c.want || changed != (c.in != c.want) {
			t.Errorf("%q: got %q (changed %v), want %q", c.in, got, changed, c.want)
		}
	}
}

func TestOrient(t *testing.T) {
	rec := &SessionRecord{Repo: "/w/repo", Worktree: "/w/repo/.p0ssess/wt/w1", Location: "/w/repo/.p0ssess/wt/w1", Mode: "edit"}
	got := orient(rec, nil, "write unit tests for every module in /w/repo/src")
	for _, want := range []string{"every module in /w/repo/.p0ssess/wt/w1/src", "your current directory is /w/repo/.p0ssess/wt/w1",
		"already point into this copy", "do not commit", "test command you ran", "Do not paste file contents"} {
		if !strings.Contains(got, want) {
			t.Errorf("edit orientation lacks %q:\n%s", want, got)
		}
	}
	ro := orient(&SessionRecord{Repo: "/w/repo", Location: "/w/repo", Mode: "read"}, nil, "summarize /w/repo/README.md")
	if !strings.HasPrefix(ro, "summarize /w/repo/README.md") || !strings.Contains(ro, "read-only") {
		t.Errorf("read orientation: %s", ro)
	}
}

func TestResultIsCompact(t *testing.T) {
	r := Result{Answer: "ok", Status: "done", Diff: strings.Repeat("+line\n", 1000), DiffPath: "/runs/s/diff.patch",
		DiffStat: "1 file changed", Apply: "git -C /w/repo apply /runs/s/diff.patch"}
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "+line") || !strings.Contains(string(b), `"apply":"git -C /w/repo apply /runs/s/diff.patch"`) {
		t.Errorf("tool result carries the patch or lacks the apply command: %.300s", b)
	}
	if shellQuote("/a b/it's") != `'/a b/it'\''s'` || shellQuote("/plain/path.patch") != "/plain/path.patch" {
		t.Error("shellQuote")
	}
}
