package tour

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"p0ssess/internal/pme/paths"
)

func env(kv ...string) func(string) string {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return func(k string) string { return m[k] }
}

var person = env("CLAUDE_CODE_ENTRYPOINT", "cli")

func dirs(t *testing.T) paths.Dirs {
	root := t.TempDir()
	return paths.Dirs{Config: root + "/config", Cache: root + "/cache", State: root + "/state"}
}

func TestInteractive(t *testing.T) {
	cases := []struct {
		name string
		env  func(string) string
		want bool
	}{
		{"terminal", person, true},
		{"attended", env("CLAUDE_CODE_ENTRYPOINT", "cli", "CLAUDE_CODE_SESSION_ATTENDED", "1"), true},
		{"unattended", env("CLAUDE_CODE_ENTRYPOINT", "cli", "CLAUDE_CODE_SESSION_ATTENDED", "0"), false},
		{"print mode", env("CLAUDE_CODE_ENTRYPOINT", "sdk-cli"), false},
		{"sdk", env("CLAUDE_CODE_ENTRYPOINT", "sdk-ts"), false},
		{"ci", env("CLAUDE_CODE_ENTRYPOINT", "cli", "CI", "true"), false},
		{"child session", env("CLAUDE_CODE_ENTRYPOINT", "cli", "CLAUDE_CODE_CHILD_SESSION", "1"), false},
		{"baseline run", env("CLAUDE_CODE_ENTRYPOINT", "cli", "P0SSESS_BASELINE", "alone"), false},
		{"turned off", env("CLAUDE_CODE_ENTRYPOINT", "cli", "P0SSESS_TOUR", "off"), false},
		{"not claude code", env(), false},
		{"ide", env("CLAUDE_CODE_ENTRYPOINT", "claude-vscode"), true},
	}
	for _, tc := range cases {
		if got := Interactive(tc.env); got != tc.want {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
}

// A new install: the first plain message of the first interactive session starts the tour, once.
func TestFirstRunStartsOnceAndNeverRepeats(t *testing.T) {
	d := dirs(t)
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	if got := Hook(d, "fix the flaky test", env("CLAUDE_CODE_ENTRYPOINT", "sdk-cli"), now); got != "" {
		t.Fatalf("headless: %q", got)
	}
	if _, ok := Load(d); ok {
		t.Fatal("a headless session must not use up the tour")
	}
	if got := Hook(d, "/clear", person, now); got != "" {
		t.Fatalf("a slash command defers it: %q", got)
	}
	got := Hook(d, "hi", person, now)
	if !strings.Contains(got, `skill "p0ssess:tour"`) || !strings.Contains(got, "p0ssess tour skip") {
		t.Fatalf("start: %q", got)
	}
	if st, ok := Load(d); !ok || st.State != Started {
		t.Fatalf("state %+v", st)
	}
	for i := 0; i < 3; i++ {
		if got := Hook(d, "another message", person, now.Add(time.Minute)); got != "" {
			t.Fatalf("repeat %d: %q", i, got)
		}
	}
	// A new session the next day does not bring it back either.
	if got := Hook(d, "good morning", person, now.Add(24*time.Hour)); got != "" {
		t.Fatalf("next session: %q", got)
	}
}

func TestSkipAndDone(t *testing.T) {
	d := dirs(t)
	now := time.Now()
	Hook(d, "hello", person, now)
	if err := Set(d, Skipped, now); err != nil {
		t.Fatal(err)
	}
	if st, _ := Load(d); st.State != Skipped {
		t.Fatalf("%+v", st)
	}
	if got := Hook(d, "hello again", person, now); got != "" {
		t.Fatalf("after skip: %q", got)
	}
	Set(d, Done, now)
	if got := Hook(d, "x", person, now); got != "" {
		t.Fatalf("after done: %q", got)
	}
}

// Typing /p0ssess:tour first counts as the tour starting: no second start afterwards.
func TestTourCommandFirst(t *testing.T) {
	d := dirs(t)
	if got := Hook(d, "/p0ssess:tour", person, time.Now()); got != "" {
		t.Fatalf("%q", got)
	}
	if st, _ := Load(d); st.State != Started {
		t.Fatalf("%+v", st)
	}
	if got := Hook(d, "thanks", person, time.Now()); got != "" {
		t.Fatalf("%q", got)
	}
}

// An install that used p0ssess before gets one offer line, once, and no tour.
func TestExistingUserOfferedOnce(t *testing.T) {
	d := dirs(t)
	os.MkdirAll(d.State, 0o700)
	os.WriteFile(d.HistoryFile(), []byte(`{"ts":"2026-11-01T00:00:00Z","type":"run"}`+"\n"), 0o600)
	got := Hook(d, "hi", person, time.Now())
	if !strings.Contains(got, "/p0ssess:tour runs it") || strings.Contains(got, "Skill tool") {
		t.Fatalf("offer: %q", got)
	}
	if st, _ := Load(d); st.State != Offered {
		t.Fatalf("%+v", st)
	}
	if got := Hook(d, "hi", person, time.Now()); got != "" {
		t.Fatalf("offered twice: %q", got)
	}
	d2 := dirs(t)
	os.MkdirAll(d2.Config, 0o700)
	os.WriteFile(d2.BudgetFile(), []byte("monthly_usd = 5\n"), 0o600)
	if !Existing(d2) {
		t.Error("a budget file means an existing user")
	}
}

// Two sessions starting at the same moment start the tour once between them.
func TestConcurrentStartClaimsOnce(t *testing.T) {
	d := dirs(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	starts := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if Hook(d, "hi", person, time.Now()) != "" {
				mu.Lock()
				starts++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if starts != 1 {
		t.Fatalf("started %d times", starts)
	}
}

func TestShowNewUser(t *testing.T) {
	out := Show(Facts{Envelope: Defaults(), New: true, KeyReady: true, StatusLine: true})
	for _, want := range []string{
		"step 1 what", "step 2 envelope  the defaults for a new install: $1 a day (warns) · $10 a month (stops paid runs) · runs without asking up to $0.25 a task",
		"Keep these: $1 a day, $10 a month, $0.25 a task -> p0ssess tour envelope --daily 1 --monthly 10 --task 0.25",
		"Lower: $0.50 a day, $5 a month, $0.10 a task", "Higher: $2 a day, $25 a month, $0.50 a task",
		"capped at $0.01 -> p0ssess tour demo", "status line: p0ssess $0.42/$1 · 1 running", "/p0ssess:p0ssess-me",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "/p0ssess:team") {
		t.Error("team line for a solo user")
	}
	if n := strings.Count(out, "step "); n != 4 {
		t.Errorf("%d steps", n)
	}
}

func TestShowExistingNoKeyTeam(t *testing.T) {
	out := Show(Facts{Envelope: Envelope{Daily: 0, Monthly: 20, Task: 0.02}, KeyFile: "/k", Team: true})
	for _, want := range []string{"your limits: a daily limit paced from the month · $20 a month", "Keep these: no change",
		"needs an OpenRouter key first: put it in /k", "not set up yet (p0ssess setup statusline", "your team: /p0ssess:team"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
}

// The demo runs one tiny task capped at a cent, through the runner it is given (a fake here: never a
// paid call in tests), and says what it cost.
func TestDemo(t *testing.T) {
	var gotMax float64
	var gotTask string
	fake := func(_ context.Context, task, uc string, max float64) (DemoResult, error) {
		gotTask, gotMax = task, max
		return DemoResult{Model: "xiaomi/mimo-v2.6-flash", Answer: "p0ssess hands heavy work\n to cheaper models.", Receipt: "(mimo-v2.6-flash, $0.0001)", CostUSD: 0.00012}, nil
	}
	out, err := Demo(context.Background(), fake, true, "")
	if err != nil || gotMax != 0.01 || gotTask != DemoTask {
		t.Fatalf("%v %v %q", err, gotMax, gotTask)
	}
	for _, want := range []string{"asked    summarize a paragraph about p0ssess", "model    xiaomi/mimo-v2.6-flash", "answer   p0ssess hands heavy work to cheaper models.", "receipt  (mimo-v2.6-flash, $0.0001)", "cost     $0.0001, under a cent"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %s", want, out)
		}
	}
	called := false
	out, _ = Demo(context.Background(), func(context.Context, string, string, float64) (DemoResult, error) {
		called = true
		return DemoResult{}, nil
	}, false, "/home/u/.config/p0ssess/secrets/openrouter_key")
	if called || !strings.HasPrefix(out, "skip") {
		t.Errorf("no key: %v %q", called, out)
	}
	if _, err := Demo(context.Background(), func(context.Context, string, string, float64) (DemoResult, error) {
		return DemoResult{}, errors.New("no model fits")
	}, true, ""); err == nil {
		t.Error("error lost")
	}
	var ucs []string
	if _, err := Demo(context.Background(), func(_ context.Context, _, uc string, _ float64) (DemoResult, error) {
		ucs = append(ucs, uc)
		if uc == DemoUseCase {
			return DemoResult{}, errors.New("no model fits summarize-long-doc within your rules and limits")
		}
		return DemoResult{Answer: "p0ssess routes work.", Receipt: "(deepseek-v4.1-flash, $0.0001)"}, nil
	}, true, ""); err != nil || len(ucs) != 2 {
		t.Errorf("a table that keeps the question on Claude: %v %v", err, ucs)
	}
	if DemoCost(0) != "nothing (a free route)" || DemoCost(0.00001) != "under a hundredth of a cent" {
		t.Error(DemoCost(0), DemoCost(0.00001))
	}
}
