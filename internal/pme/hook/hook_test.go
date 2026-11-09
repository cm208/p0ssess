package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"p0ssess/internal/history"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/usage"
)

func dirs(t *testing.T) (paths.Dirs, string) {
	home := t.TempDir()
	t.Setenv("P0SSESS_HOME", home)
	proj := filepath.Join(home, "proj")
	os.MkdirAll(filepath.Join(proj, ".git"), 0o755)
	return paths.Default(), proj
}

func TestHints(t *testing.T) {
	d, proj := dirs(t)
	now := time.Now()
	cases := []struct {
		prompt, want string
	}{
		{"what does this function do?", ""},
		{"fix the off-by-one in parse.go", ""},
		{"summarize demo.mp4 for the changelog", "video in this prompt"},
		{"what's said in https://www.youtube.com/watch?v=abc", "video in this prompt"},
		{"transcribe interview.m4a", "audio in this prompt"},
		{"generate an image of a lighthouse for the README", "cannot generate"},
		{"translate every string in locales/en.json", "bulk one-shot"},
		{strings.Repeat("log line\n", 5000), "bulk one-shot"},
	}
	for _, tc := range cases {
		got := Hint(Input{Prompt: tc.prompt, Cwd: proj}, d, now)
		if tc.want == "" && got != "" || tc.want != "" && !strings.Contains(got, tc.want) {
			t.Errorf("%.40q: got %q", tc.prompt, got)
		}
		if strings.Contains(got, "\n") {
			t.Errorf("hint must be one line: %q", got)
		}
	}
	os.WriteFile(filepath.Join(proj, ".p0ssess-private"), nil, 0o600)
	if got := Hint(Input{Prompt: "summarize demo.mp4", Cwd: proj}, d, now); got != "" {
		t.Errorf("private repo should get no hint: %q", got)
	}
}

func TestLimitAndFavored(t *testing.T) {
	d, proj := dirs(t)
	now := time.Now()
	used, reset := 85.0, now.Add(time.Hour).Unix()
	usage.Save(d.UsageFile(), usage.State{ReadingAt: now, Subscriber: true,
		RateLimits: &usage.RateLimits{FiveHour: &usage.Window{UsedPercentage: &used, ResetsAt: &reset}}})
	got := Hint(Input{Prompt: "refactor the parser", Cwd: proj}, d, now)
	if !strings.Contains(got, "5-hour limit is 85% used") {
		t.Errorf("limit hint = %q", got)
	}
	for i := 0; i < 4; i++ {
		history.Append(d.HistoryFile(), history.Entry{Type: history.TypeOutcome, Model: "google/gemini-3.8-flash", Kind: "media", Outcome: history.Accepted})
	}
	got = Hint(Input{Prompt: "what happens at 2:10 in bug.mov?", Cwd: proj}, d, now)
	if !strings.Contains(got, "You usually accept gemini-3.8-flash here (4/4).") {
		t.Errorf("favored = %q", got)
	}
}

func TestRunRecordsSessionAndIsFast(t *testing.T) {
	d, proj := dirs(t)
	raw, _ := json.Marshal(Input{SessionID: "sess-1", Cwd: proj, Prompt: "hello", Event: "UserPromptSubmit"})
	start := time.Now()
	for i := 0; i < 20; i++ {
		if out := Run(raw, d, time.Now()); out != "" {
			t.Fatalf("unexpected hint %q", out)
		}
	}
	if per := time.Since(start) / 20; per > 50*time.Millisecond {
		t.Errorf("hook took %v per call", per)
	}
	st, _ := usage.Load(d.UsageFile())
	if se, ok := st.SessionFor(proj); !ok || se.ID != "sess-1" {
		t.Errorf("session not recorded: %+v", st.Sessions)
	}
	if Run([]byte("not json"), d, time.Now()) != "" {
		t.Error("bad input should print nothing")
	}
}
