package relay

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"p0ssess/internal/config"
	"p0ssess/internal/jail"
	"p0ssess/internal/opencode"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/usage"
)

func lastMatch(rules []opencode.Rule, action, resource string) string {
	eff := "ask"
	for _, r := range rules {
		if (r.Action == "*" || r.Action == action) && globMatch(r.Resource, resource) {
			eff = r.Effect
		}
	}
	return eff
}

// globMatch is the subset of OpenCode's wildcard matching the rules use: '*' matches any run.
func globMatch(pat, s string) bool {
	if pat == "*" {
		return true
	}
	parts := strings.Split(pat, "*")
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for i, p := range parts[1:] {
		if i == len(parts)-2 {
			return strings.HasSuffix(s, p)
		}
		j := strings.Index(s, p)
		if j < 0 {
			return false
		}
		s = s[j+len(p):]
	}
	return s == ""
}

func TestRules(t *testing.T) {
	read, edit := Rules("read", false), Rules("edit", false)
	cases := []struct {
		rules            []opencode.Rule
		action, resource string
		want             string
	}{
		{read, "edit", "calc.py", "deny"},
		{read, "read", "calc.py", "allow"},
		{read, "shell", "git status", "allow"},
		{read, "shell", "python3 x.py", "ask"},
		{read, "shell", "curl https://x", "ask"},
		{read, "webfetch", "https://x", "ask"},
		{read, "subagent", "general", "deny"},
		{read, "external_directory", "/etc/*", "deny"},
		{read, "something_new", "x", "ask"},
		{edit, "edit", "calc.py", "allow"},
		{edit, "shell", "python3 -m unittest", "allow"},
		{edit, "shell", "curl https://x", "ask"},
		{edit, "question", "x", "deny"},
	}
	for _, c := range cases {
		if got := lastMatch(c.rules, c.action, c.resource); got != c.want {
			t.Errorf("%s %q: got %s, want %s", c.action, c.resource, got, c.want)
		}
	}
	net := Rules("edit", true)
	if lastMatch(net, "shell", "pip install requests") != "allow" || lastMatch(edit, "shell", "pip install requests") != "ask" ||
		lastMatch(Rules("read", true), "shell", "pip install requests") != "ask" {
		t.Error("installs are allowed only in edit mode with shell internet")
	}
	if Rules("read", false)[0] != (opencode.Rule{Action: "*", Resource: "*", Effect: "ask"}) {
		t.Error("rules must start with a catch-all ask")
	}
}

func TestParseModel(t *testing.T) {
	for in, want := range map[string]string{
		"deepseek/deepseek-v4.1-flash":            "openrouter/deepseek/deepseek-v4.1-flash",
		"openrouter/z-ai/glm-5.3-flash":           "openrouter/z-ai/glm-5.3-flash",
		" google/gemini-3.8-flash ":               "openrouter/google/gemini-3.8-flash",
		"anthropic/claude-haiku-5-5#thinking-low": "openrouter/anthropic/claude-haiku-5-5",
	} {
		m, err := ParseModel(in)
		if err != nil || m.String() != want {
			t.Errorf("%q: %v %v", in, m, err)
		}
	}
	for _, bad := range []string{"", "deepseek flash", "gpt"} {
		if _, err := ParseModel(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestFinalAnswer(t *testing.T) {
	msgs := []opencode.Message{
		{ID: "msg_1", Type: "user", Text: "first"},
		{Type: "assistant", Content: []opencode.ContentPart{{Type: "text", Text: "old answer"}}},
		{ID: "msg_2", Type: "user", Text: "second"},
		{Type: "assistant", Content: []opencode.ContentPart{{Type: "text", Text: "let me look"}, {Type: "tool", Name: "read"}}},
		{Type: "assistant", Content: []opencode.ContentPart{{Type: "text", Text: "new "}, {Type: "text", Text: "answer"}}},
		{Type: "assistant", Content: []opencode.ContentPart{{Type: "tool", Name: "read"}}},
		{Type: "idle"},
	}
	if got := finalAnswer(msgs, "msg_2"); got != "new answer" {
		t.Errorf("got %q", got)
	}
	if got := finalAnswer(msgs[:2], "msg_1"); got != "old answer" {
		t.Errorf("got %q", got)
	}
}

func TestClipUTF8(t *testing.T) {
	s, cut := clip("ab✓cd", 3, "…")
	if !cut || s != "ab…" {
		t.Errorf("%q %v", s, cut)
	}
}

func TestAttachments(t *testing.T) {
	d := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(d, name)
		os.WriteFile(p, data, 0o600)
		return p
	}
	txt := write("notes.md", []byte("# hi\n"))
	png := write("x.png", []byte("\x89PNG\r\n\x1a\n0000"))
	got, err := attachments([]string{txt, png})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got[0].URI, "data:text/plain;base64,") || got[0].Name != "notes.md" {
		t.Errorf("text: %+v", got[0])
	}
	if !strings.HasPrefix(got[1].URI, "data:image/png;base64,") {
		t.Errorf("png: %.40s", got[1].URI)
	}
	for _, secret := range []string{".env", ".env.local", "id_ed25519", "server.pem", "prod-secrets.yaml"} {
		if _, err := attachments([]string{write(secret, []byte("x"))}); err == nil {
			t.Errorf("%s attached", secret)
		}
	}
	if _, err := attachments([]string{write("clip.mp3", []byte("ID3\x03\x00\x00\x00\x00\x00\x00"))}); err == nil {
		t.Error("audio attached")
	}
	if _, err := attachments([]string{write("ok.txt", []byte("secretary notes"))}); err != nil {
		t.Errorf("plain file refused: %v", err)
	}
}

func TestWatchReplaysRecordedRun(t *testing.T) {
	f, err := os.Open("../opencode/testdata/sse/edit-run.sse")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ctx, cancel := context.WithCancel(context.Background())
	w := &watch{ctx: ctx, cancel: cancel, done: make(chan struct{}), poke: make(chan struct{}, 1),
		toolNames: map[string]string{}, seen: map[string]bool{}, seq: -1, rec: &SessionRecord{}}
	opencode.ReadFrames(f, func(fr opencode.Frame) bool {
		ev, ok := opencode.ParseEvent(fr)
		if !ok {
			return true
		}
		if ev.Type == "session.created" {
			w.rec.SessionID = ev.SessionID()
		}
		if ev.SessionID() == w.rec.SessionID {
			w.handle(ev)
		}
		return true
	})
	if !w.finished() || w.outcome != "succeeded" || w.steps != 4 || len(w.tools) != 5 {
		t.Errorf("finished=%v outcome=%q steps=%d tools=%v", w.finished(), w.outcome, w.steps, w.tools)
	}
}

// Claude Code started in the home directory made the home directory the jail's repository (0.5.3 and
// earlier). The session's recorded directory is used when known, and a run is refused before any jail
// exists unless that directory is inside a git repository other than the home directory.
func TestRunRefusesHomeAndNonRepositories(t *testing.T) {
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	home := filepath.Join(tmp, "home")
	os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	t.Setenv("HOME", home)
	t.Setenv("P0SSESS_HOME", filepath.Join(tmp, "p0"))
	t.Setenv("GIT_CEILING_DIRECTORIES", tmp)
	t.Setenv("CLAUDE_PROJECT_DIR", home)
	repo := filepath.Join(home, "proj")
	os.MkdirAll(repo, 0o755)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("needs git")
	}
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}

	const sid = "ed59eeff-0000-4000-8000-000000000002"
	t.Setenv("CLAUDE_CODE_SESSION_ID", sid)
	if got := defaultCwd(); got != home {
		t.Errorf("no session record: cwd %s, want the project directory", got)
	}
	usage.Update(paths.Default().UsageFile(), func(s *usage.State) {
		s.TouchSession(usage.Session{ID: "other", Cwd: filepath.Join(tmp, "elsewhere")})
		s.TouchSession(usage.Session{ID: sid, Cwd: repo, Project: home})
	})
	if got := defaultCwd(); got != repo {
		t.Errorf("cwd %s, want the session's recorded %s", got, repo)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(cfg, "test", io.Discard)
	e.Unguarded = true
	plain := filepath.Join(tmp, "plain")
	os.MkdirAll(plain, 0o755)
	for _, cwd := range []string{home, plain, "/"} {
		_, err := e.Run(context.Background(), RunRequest{Task: "list the files", Cwd: cwd}, nil)
		var nm *jail.NotMountable
		if !errors.As(err, &nm) || !strings.Contains(err.Error(), "use the direct tool") {
			t.Errorf("cwd %s: %v", cwd, err)
		}
	}
	if ents, _ := os.ReadDir(jail.Dir()); len(ents) != 0 {
		t.Errorf("a jail was started: %v", ents)
	}
	// A session recorded with the home directory as its repository cannot continue either.
	rec := &SessionRecord{SessionID: "ses_old", Repo: home, Cwd: home, Jail: "p0ssess-000000000000"}
	if err := e.continuable(rec); err == nil || !strings.Contains(err.Error(), "home directory") {
		t.Errorf("old session: %v", err)
	}
}
