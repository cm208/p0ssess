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
		{"translate every string in locales/en.json", "runs by default"},
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

func toolCall(name string) string {
	return `{"session_id":"s1","tool_name":"mcp__plugin_p0ssess_p0ssess__` + name + `","tool_input":{}}`
}

// A paused run can always be answered: the denial names the command line that does the same call, and a
// second attempt in the same session goes through for the tools that finish a paused run.
func TestPausedRunCanBeAnsweredFromTheMainConversation(t *testing.T) {
	d, _ := dirs(t)
	out := Tool([]byte(toolCall("answer_permission")), d)
	for _, want := range []string{`"permissionDecision":"deny"`, "p0ssess answer --session", "p0ssess abort --session"} {
		if !strings.Contains(out, want) {
			t.Fatalf("denial lacks %q: %s", want, out)
		}
	}
	if out := Tool([]byte(toolCall("answer_permission")), d); out != "" {
		t.Errorf("the retry should pass: %s", out)
	}
	if out := Tool([]byte(toolCall("abort")), d); !strings.Contains(out, "deny") {
		t.Errorf("abort has its own first denial: %s", out)
	}
	if out := Tool([]byte(toolCall("abort")), d); out != "" {
		t.Errorf("the abort retry should pass: %s", out)
	}
	if out := Tool([]byte(toolCall("direct")), d); !strings.Contains(out, "deny") {
		t.Errorf("direct stays with the agent: %s", out)
	}
	if out := Tool([]byte(toolCall("direct")), d); !strings.Contains(out, "deny") {
		t.Errorf("direct is not released by retrying: %s", out)
	}
}

// When starting the p0ssess agent fails (its type is gone after a restart or resume), every routed tool
// is open to the main conversation for that session.
func TestAgentTypeGoneOpensTheTools(t *testing.T) {
	d, _ := dirs(t)
	fail := `{"hook_event_name":"PostToolUseFailure","session_id":"s1","tool_name":"Agent","tool_input":{"subagent_type":"p0ssess:p0ssess"},"error":"Agent type 'p0ssess:p0ssess' not found"}`
	if out := Tool([]byte(fail), d); out != "" {
		t.Errorf("failure hook prints nothing: %s", out)
	}
	for _, name := range []string{"direct", "run", "wait", "abort", "answer_permission"} {
		if out := Tool([]byte(toolCall(name)), d); out != "" {
			t.Errorf("%s still refused: %s", name, out)
		}
	}
	other := `{"hook_event_name":"PostToolUseFailure","session_id":"s2","tool_name":"Agent","tool_input":{"subagent_type":"Explore"}}`
	Tool([]byte(other), d)
	if out := Tool([]byte(strings.Replace(toolCall("run"), "s1", "s2", 1)), d); !strings.Contains(out, "deny") {
		t.Errorf("another agent's failure opens nothing: %s", out)
	}
}

func TestLimitOffersTheAgentRoute(t *testing.T) {
	d, proj := dirs(t)
	now := time.Now()
	used, reset := 85.0, now.Add(time.Hour).Unix()
	usage.Save(d.UsageFile(), usage.State{ReadingAt: now, Subscriber: true,
		RateLimits: &usage.RateLimits{FiveHour: &usage.Window{UsedPercentage: &used, ResetsAt: &reset}}})
	for _, p := range []string{"spawn a Sonnet subagent to refactor the parser", "use an opus agent for the big review", "delegate the migration to a Claude agent", "run this with a subagent"} {
		got := hint(Input{Prompt: p, Cwd: proj}, d, now, true)
		if !strings.Contains(got, "Route by default") || !strings.Contains(got, "one line") || strings.Contains(got, "\n") {
			t.Errorf("%q: %q", p, got)
		}
	}
	got := Hint(Input{Prompt: "refactor the parser", Cwd: proj}, d, now)
	if strings.Contains(got, "Route by default") || !strings.Contains(got, "limit is 85% used") {
		t.Errorf("no delegation in the prompt: %q", got)
	}
	low := 10.0
	usage.Save(d.UsageFile(), usage.State{ReadingAt: now, Subscriber: true,
		RateLimits: &usage.RateLimits{FiveHour: &usage.Window{UsedPercentage: &low, ResetsAt: &reset}}})
	if got := Hint(Input{Prompt: "spawn a Sonnet subagent to refactor the parser", Cwd: proj}, d, now); got != "" {
		t.Errorf("low pressure: %q", got)
	}
}

func TestUseCaseFor(t *testing.T) {
	for p, want := range map[string]string{
		"fix the failing test in the parser": "code-debug-fix",
		"write unit tests for the cache":     "test-generation",
		"summarize this changelog":           "summarize-long-doc",
		"fix the typo in README":             "small-scoped-task",
		"hello there":                        "",
	} {
		if got := UseCaseFor(p); got != want {
			t.Errorf("%q: %q, want %q", p, got, want)
		}
	}
}

// The owner's research prompt and its kin are recognized and routed by default (1.0.4 finding 1).
func TestResearchRoutesByDefault(t *testing.T) {
	d, proj := dirs(t)
	now := time.Now()
	owner := "give me a summary and news insight and brief market trend insight using consumer data and news headlines from the past 6 months about consumer trends among AI users who don't code"
	if uc := UseCaseFor(owner); uc != "deep-web-research" {
		t.Errorf("use case %q", uc)
	}
	if !NeedsCurrentInfo(owner) {
		t.Error("the owner's prompt needs current information")
	}
	for _, p := range []string{owner, "what are the latest news headlines about EV battery prices", "market research on note-taking apps",
		"summarize this report.pdf for me", "classify these 300 support tickets by urgency"} {
		got := hint(Input{Prompt: p, Cwd: proj}, d, now, true)
		if !strings.Contains(got, "runs by default") || !strings.Contains(got, "p0ssess:p0ssess") || !strings.Contains(got, "do not do the work yourself") {
			t.Errorf("%.50q: %q", p, got)
		}
		if strings.Contains(got, "\n") {
			t.Errorf("one line: %q", got)
		}
	}
	if got := hint(Input{Prompt: owner, Cwd: proj}, d, now, true); !strings.Contains(got, "current web data") {
		t.Errorf("web: %q", got)
	}
	for _, p := range []string{"fix the off-by-one in parse.go", "what does this function do?", "explain the price calculation in cost.go",
		"summarize the latest commit", "keep the docs up-to-date", "is this a breaking change?", "what changed this week in the repo"} {
		if NeedsCurrentInfo(p) {
			t.Errorf("%q does not need the web", p)
		}
	}
	for _, p := range []string{"what are the latest releases of htmx", "bitcoin price today", "consumer trends over the past 6 months"} {
		if !NeedsCurrentInfo(p) {
			t.Errorf("%q needs the web", p)
		}
	}
	// "keep this on Claude" overrides.
	if got := hint(Input{Prompt: owner + ", keep this on Claude", Cwd: proj}, d, now, true); got != "" {
		t.Errorf("keep on Claude: %q", got)
	}
	// Without a key there is nothing to route to.
	if got := hint(Input{Prompt: owner, Cwd: proj}, d, now, false); got != "" {
		t.Errorf("no key: %q", got)
	}
}

// Asking for a Claude agent while Claude's limit runs low gets route-by-default wording (finding 2).
func TestClaudeAgentAtHighUsageRoutesByDefault(t *testing.T) {
	d, proj := dirs(t)
	now := time.Now()
	used, reset := 94.0, now.Add(time.Hour).Unix()
	usage.Save(d.UsageFile(), usage.State{ReadingAt: now, Subscriber: true,
		RateLimits: &usage.RateLimits{FiveHour: &usage.Window{UsedPercentage: &used, ResetsAt: &reset}}})
	for _, p := range []string{"send a sonnet agent to refactor the parser", "spawn a claude agent to port the CLI flags"} {
		got := hint(Input{Prompt: p, Cwd: proj}, d, now, true)
		if !strings.Contains(got, "Route by default") || !strings.Contains(got, "instead of a Claude agent") || strings.Contains(got, "let the user pick") {
			t.Errorf("%q: %q", p, got)
		}
	}
}

// A run that fails tells Claude to retry through p0ssess, never to do the work itself (finding 3d).
func TestHandedOffSaysRetryThroughP0ssess(t *testing.T) {
	d, _ := dirs(t)
	out := Tool([]byte(`{"hook_event_name":"PostToolUse","session_id":"s1","tool_name":"Agent","tool_input":{"subagent_type":"p0ssess:p0ssess"}}`), d)
	if !strings.Contains(out, "send the agent the request again") || !strings.Contains(out, "never do the work yourself") || !strings.Contains(out, "receipt") {
		t.Errorf("%s", out)
	}
}
