package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"p0ssess/internal/account"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/rescue"
	"p0ssess/internal/usage"
)

// rescueSetup writes a Claude usage reading at five-hour percent and a key file, and returns the
// project dir; pro turns on a Pro account.
func rescueSetup(t *testing.T, now time.Time, five float64, pro bool) (paths.Dirs, string) {
	t.Helper()
	d, proj := dirs(t)
	key := filepath.Join(t.TempDir(), "k")
	os.WriteFile(key, []byte("x\n"), 0o600)
	t.Setenv("P0SSESS_OPENROUTER_KEY_FILE", key)
	t.Setenv("P0SSESS_FUNNEL", "")
	os.MkdirAll(d.Cache, 0o700)
	reset := now.Add(90 * time.Minute).Unix()
	week := now.Add(72 * time.Hour).Unix()
	rl := usage.RateLimits{FiveHour: &usage.Window{UsedPercentage: &five, ResetsAt: &reset}}
	ten := 10.0
	rl.SevenDay = &usage.Window{UsedPercentage: &ten, ResetsAt: &week}
	if err := usage.Save(d.UsageFile(), usage.State{RateLimits: &rl, ReadingAt: now}); err != nil {
		t.Fatal(err)
	}
	if pro {
		account.SaveToken(d, "p0d_test")
		os.MkdirAll(d.State, 0o700)
		os.WriteFile(filepath.Join(d.State, "account.json"), []byte(`{"plan":"pro","status":"active"}`), 0o600)
	}
	return d, proj
}

func run(d paths.Dirs, proj, prompt string, now time.Time) string {
	raw, _ := json.Marshal(Input{SessionID: "s", Cwd: proj, Prompt: prompt, Event: "UserPromptSubmit"})
	return Run(raw, d, now)
}

func TestProRescueRoutesAndSaysItOnce(t *testing.T) {
	now := time.Now()
	d, proj := rescueSetup(t, now, 91, true)
	out := run(d, proj, "write unit tests for the parser in /tmp/p/parse.go", now)
	if !strings.Contains(out, "is taking routable work until it resets at") || !strings.Contains(out, "window is at 91%") {
		t.Fatalf("no enter line: %q", out)
	}
	if !strings.Contains(out, "p0ssess rescue is on") || !strings.Contains(out, `use case test-generation`) || !strings.Contains(out, "edit mode") {
		t.Fatalf("no routing: %q", out)
	}
	again := run(d, proj, "write unit tests for the lexer in /tmp/p/lex.go", now.Add(time.Minute))
	if strings.Contains(again, "is taking routable work") || !strings.Contains(again, "p0ssess rescue is on") {
		t.Fatalf("second prompt: %q", again)
	}
}

func TestRescueLeavesCalmAndKeepOnClaude(t *testing.T) {
	now := time.Now()
	d, proj := rescueSetup(t, now, 30, true)
	if out := run(d, proj, "write unit tests for the parser", now); strings.Contains(out, "rescue") {
		t.Fatalf("calm window: %q", out)
	}
	d, proj = rescueSetup(t, now, 91, true)
	out := run(d, proj, "write unit tests for the parser, keep this on Claude", now)
	if strings.Contains(out, "p0ssess rescue is on") || strings.Contains(out, "use case") {
		t.Fatalf("keep on Claude was overridden: %q", out)
	}
}

func TestRescueAsksOnceForCriticalWork(t *testing.T) {
	now := time.Now()
	d, proj := rescueSetup(t, now, 91, true)
	out := run(d, proj, "implement the new login and session token auth", now)
	for _, w := range []string{"critical work (stakes 4)", "Ask the user once", "needs approval", "p0ssess can take this critical work on <model> for about $<cost>", "never run it before the yes", `"stakes 4"`} {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in %q", w, out)
		}
	}
	if strings.Contains(out, "Do not do it yourself, ask first") {
		t.Errorf("critical work is routed without asking: %q", out)
	}
}

func TestFreeGetsOneLineNoRouting(t *testing.T) {
	now := time.Now()
	d, proj := rescueSetup(t, now, 91, false)
	out := run(d, proj, "write unit tests for the parser", now)
	if !strings.Contains(out, "Rescue is a Pro feature") || strings.Contains(out, "p0ssess rescue is on") {
		t.Fatalf("free: %q", out)
	}
	if out := run(d, proj, "write unit tests for the lexer", now.Add(time.Minute)); strings.Contains(out, "Rescue is a Pro feature") {
		t.Fatalf("offer repeated: %q", out)
	}
	if _, on := rescue.Active(d, now); on {
		t.Fatal("free entered rescue")
	}
}

func TestRescueStaysInsideTheEnvelope(t *testing.T) {
	now := time.Now()
	d, proj := rescueSetup(t, now, 91, true)
	os.MkdirAll(d.Config, 0o700)
	os.WriteFile(d.BudgetFile(), []byte("monthly_usd = 10.0\nper_task_usd = 0.25\ndaily_usd = 1.0\n[rescue]\nenabled = true\nthreshold = 0.85\n"), 0o600)
	os.MkdirAll(d.State, 0o700)
	line, _ := json.Marshal(map[string]any{"ts": now.Add(-time.Minute).UTC(), "type": "run", "run_id": "r1", "cash_usd": 1.5, "model": "m/x"})
	os.WriteFile(d.HistoryFile(), append(line, '\n'), 0o600)
	out := run(d, proj, "write unit tests for the parser", now)
	if strings.Contains(out, "taking routable work") || strings.Contains(out, "p0ssess rescue is on") {
		t.Fatalf("rescued past the daily limit: %q", out)
	}
}

func TestRescueConfigOff(t *testing.T) {
	now := time.Now()
	d, proj := rescueSetup(t, now, 91, true)
	os.MkdirAll(d.Config, 0o700)
	os.WriteFile(d.BudgetFile(), []byte("[rescue]\nenabled = false\n"), 0o600)
	if out := run(d, proj, "write unit tests for the parser", now); strings.Contains(out, "rescue") {
		t.Fatalf("rescue off: %q", out)
	}
}

func TestRescueHandsBackWhenTheWindowResets(t *testing.T) {
	now := time.Now()
	d, proj := rescueSetup(t, now, 91, true)
	run(d, proj, "write unit tests for the parser", now)
	later := now.Add(2 * time.Hour)
	out := run(d, proj, "write unit tests for the lexer", later)
	if !strings.Contains(out, "5-hour window has reset: p0ssess handed routable work back to Claude.") || strings.Contains(out, "p0ssess rescue is on") {
		t.Fatalf("hand back: %q", out)
	}
	if out := run(d, proj, "write unit tests again", later.Add(time.Minute)); strings.Contains(out, "handed routable work back") {
		t.Fatalf("hand back repeated: %q", out)
	}
}
