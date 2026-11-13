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
		{"summarize demo.mp4 for the changelog", "video-understand"},
		{"claude watch this video ~/Videos/bug.webm", "video-understand"},
		{"what's said in https://www.youtube.com/watch?v=abc", "download the link first"},
		{"transcribe interview.m4a", "audio-transcribe"},
		{"listen to this and tell me the mood: ~/memo.ogg", "audio-tone"},
		{"transcribe the scanner traffic in radio.mp3", "audio-transcribe-noisy for noisy or hard audio such as radio, phone or field recordings"},
		{"generate an image of a lighthouse for the README", "image-generate"},
		{"make a picture of a ghost holding a lantern", "Present the file"},
		{"translate every string in locales/en.json", "bulk one-shot"},
		{strings.Repeat("log line\n", 5000), "bulk one-shot"},
	}
	for _, tc := range cases {
		got := hint(Input{Prompt: tc.prompt, Cwd: proj}, d, now, true)
		if tc.want == "" && got != "" || tc.want != "" && !strings.Contains(got, tc.want) {
			t.Errorf("%.40q: got %q", tc.prompt, got)
		}
		if strings.Contains(got, "\n") {
			t.Errorf("hint must be one line: %q", got)
		}
	}
	if got := hint(Input{Prompt: "watch this video: demo.mp4", Cwd: proj}, d, now, false); !strings.Contains(got, "no OpenRouter key") {
		t.Errorf("no key: %q", got)
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
	got = hint(Input{Prompt: "what happens at 2:10 in bug.mov?", Cwd: proj}, d, now, true)
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

// TestRoutedToolsBelongToTheAgent: the routed tools are refused from the main conversation with a reason
// that sends Claude to the p0ssess agent, pass inside a subagent, and pass in the main conversation only
// when the user's latest prompt asked for that.
func TestRoutedToolsBelongToTheAgent(t *testing.T) {
	d, proj := dirs(t)
	main := `{"session_id":"s1","tool_name":"mcp__plugin_p0ssess_p0ssess__direct","tool_input":{}}`
	out := Tool([]byte(main), d)
	if !strings.Contains(out, `"permissionDecision":"deny"`) || !strings.Contains(out, "p0ssess:p0ssess") {
		t.Fatalf("main thread: %s", out)
	}
	if out := Tool([]byte(`{"session_id":"s1","agent_type":"reviewer","tool_name":"mcp__plugin_p0ssess_p0ssess__run"}`), d); out == "" {
		t.Error("agent_type alone is a main thread started with --agent")
	}
	if out := Tool([]byte(`{"session_id":"s1","agent_id":"a1","agent_type":"p0ssess:p0ssess","tool_name":"mcp__plugin_p0ssess_p0ssess__direct"}`), d); out != "" {
		t.Errorf("subagent refused: %s", out)
	}
	if out := Tool([]byte(`{"session_id":"s1","tool_name":"mcp__plugin_p0ssess_p0ssess__budget_status"}`), d); out != "" {
		t.Errorf("budget_status is not routed work: %s", out)
	}
	raw, _ := json.Marshal(Input{SessionID: "s1", Cwd: proj, Prompt: "call direct yourself in the main session, no subagent"})
	Run(raw, d, time.Now())
	if out := Tool([]byte(main), d); out != "" {
		t.Errorf("explicitly asked for the main session: %s", out)
	}
	raw, _ = json.Marshal(Input{SessionID: "s1", Cwd: proj, Prompt: "now watch clip.mp4"})
	Run(raw, d, time.Now())
	if out := Tool([]byte(main), d); out == "" {
		t.Error("the next ordinary prompt goes back to the agent")
	}
}

func TestHandOffEndsTheTurnQuietly(t *testing.T) {
	d, _ := dirs(t)
	in := `{"hook_event_name":"PostToolUse","tool_name":"Agent","tool_input":{"subagent_type":"p0ssess:p0ssess"}}`
	if out := Tool([]byte(in), d); !strings.Contains(out, `"additionalContext"`) || !strings.Contains(out, "End this turn now") {
		t.Errorf("after the hand-off: %s", out)
	}
	if out := Tool([]byte(`{"hook_event_name":"PostToolUse","tool_name":"Agent","tool_input":{"subagent_type":"Explore"}}`), d); out != "" {
		t.Errorf("other agents are left alone: %s", out)
	}
}

func TestRunCarriesLapseNoticeOnce(t *testing.T) {
	d, proj := dirs(t)
	os.MkdirAll(d.State, 0o700)
	os.WriteFile(filepath.Join(d.State, "account.json"), []byte(`{"plan":"free","status":"lapsed","ends_at":"2026-11-12T00:00:00Z","renew":"https://example.test/pro","lapse_pending":true}`), 0o600)
	in := []byte(`{"cwd":"` + proj + `","prompt":"what does this do?"}`)
	got := Run(in, d, time.Now())
	if !strings.Contains(got, "Pro ended on Nov 12") || !strings.Contains(got, "https://example.test/pro") || strings.Contains(got, "\n") {
		t.Errorf("first prompt: %q", got)
	}
	if got := Run(in, d, time.Now()); got != "" {
		t.Errorf("second prompt: %q", got)
	}
}
