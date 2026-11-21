package rescue

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"p0ssess/internal/budget"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/usage"
)

var t0 = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

// reading is a status line reading as Claude Code passes it: both windows, with the time of the reading.
func reading(five, week float64, fiveReset, weekReset time.Time, at time.Time) usage.State {
	raw := `{"five_hour":{"used_percentage":` + ftoa(five) + `,"resets_at":` + itoa(fiveReset.Unix()) +
		`},"seven_day":{"used_percentage":` + ftoa(week) + `,"resets_at":` + itoa(weekReset.Unix()) + `}}`
	var rl usage.RateLimits
	if err := json.Unmarshal([]byte(raw), &rl); err != nil {
		panic(err)
	}
	return usage.State{RateLimits: &rl, ReadingAt: at}
}

func ftoa(f float64) string { b, _ := json.Marshal(f); return string(b) }
func itoa(n int64) string   { b, _ := json.Marshal(n); return string(b) }

func TestDetect(t *testing.T) {
	fiveReset := t0.Add(90 * time.Minute)
	weekReset := t0.Add(3 * 24 * time.Hour)
	cases := []struct {
		name       string
		five, week float64
		now        time.Time
		thr        float64
		window     string
	}{
		{"calm", 20, 30, t0, 0.85, ""},
		{"five hour at 91", 91, 30, t0, 0.85, "5-hour"},
		{"weekly at 88", 40, 88, t0, 0.85, "weekly"},
		{"the worse window wins", 90, 95, t0, 0.85, "weekly"},
		{"threshold is configurable", 70, 10, t0, 0.65, "5-hour"},
		{"a higher threshold waits", 60, 10, t0, 0.95, ""},
		// 60% used with 3.5 of 5 hours gone: projected 60/0.7 = 86%, not out. 60% after 1h of 5: 3x.
		{"projected to run out early", 60, 10, t0, 0.95, "5-hour"},
		{"reset since the reading", 91, 30, t0.Add(2 * time.Hour), 0.85, ""},
	}
	for _, c := range cases {
		st := reading(c.five, c.week, fiveReset, weekReset, t0)
		if c.name == "projected to run out early" {
			st = reading(c.five, c.week, t0.Add(4*time.Hour), weekReset, t0)
		}
		got, ok := Detect(st, c.now, c.thr)
		if c.window == "" && ok || c.window != "" && (!ok || got.Window != c.window) {
			t.Errorf("%s: got %+v %v, want %q", c.name, got, ok, c.window)
		}
	}
	if _, ok := Detect(usage.State{}, t0, 0.85); ok {
		t.Error("no reading is not near")
	}
	// An early-window blip is not extrapolated into a rescue: 40% used is under half.
	if _, ok := Detect(reading(40, 5, t0.Add(4*time.Hour+50*time.Minute), weekReset, t0), t0, 0.85); ok {
		t.Error("40% early in the window should not trigger")
	}
}

func dirs(t *testing.T, optIn bool) paths.Dirs {
	t.Helper()
	t.Setenv("P0SSESS_HOME", t.TempDir())
	t.Setenv("P0SSESS_FUNNEL", "")
	d := paths.Default()
	b := budget.Default()
	b.Funnel.Send = optIn
	os.MkdirAll(d.Config, 0o700)
	if err := budget.Save(d.BudgetFile(), b); err != nil {
		t.Fatal(err)
	}
	return d
}

func input(d paths.Dirs, now time.Time, five float64, pro bool) Input {
	return Input{D: d, Now: now, Usage: reading(five, 10, t0.Add(90*time.Minute), t0.Add(72*time.Hour), now),
		Pro: pro, Enabled: true, Ready: true, Threshold: 0.85, Room: true, Routable: true,
		Runs: func(time.Time) int { return 0 }}
}

func funnelFile(d paths.Dirs) string {
	b, _ := os.ReadFile(d.State + "/funnel.json")
	return string(b)
}

func TestProEntersOnceAndHandsBackOnce(t *testing.T) {
	d := dirs(t, true)
	want := "Claude's 5-hour window is at 91%: p0ssess is taking routable work until it resets at 1:30 PM."
	if got := Tick(input(d, t0, 91, true)); got != want {
		t.Fatalf("enter: %q", got)
	}
	if _, on := Active(d, t0.Add(time.Minute)); !on {
		t.Fatal("not active")
	}
	for i := 1; i < 4; i++ {
		if got := Tick(input(d, t0.Add(time.Duration(i)*10*time.Minute), 93, true)); got != "" {
			t.Fatalf("repeated line: %q", got)
		}
	}
	after := t0.Add(2 * time.Hour)
	in := input(d, after, 5, true)
	in.Usage = reading(5, 10, after.Add(5*time.Hour), t0.Add(72*time.Hour), after)
	if got := Tick(in); got != "Claude's 5-hour window has reset: p0ssess handed routable work back to Claude." {
		t.Fatalf("exit: %q", got)
	}
	if got := Tick(in); got != "" {
		t.Fatalf("exit repeated: %q", got)
	}
	if _, on := Active(d, after); on {
		t.Fatal("still active")
	}
	if s := Load(d); len(s.Done) != 1 || s.Done[0].Window != "5-hour" {
		t.Fatalf("periods: %+v", s.Done)
	}
}

func TestWeeklyLineNamesTheDay(t *testing.T) {
	d := dirs(t, false)
	in := input(d, t0, 10, true)
	in.Usage = reading(10, 90, t0.Add(time.Hour), t0.Add(24*time.Hour), t0)
	got := Tick(in)
	if !strings.Contains(got, "weekly window is at 90%") || !strings.Contains(got, "resets at Sat 12:00 PM.") {
		t.Fatalf("%q", got)
	}
}

func TestFreeSeesOneLinePerWindow(t *testing.T) {
	d := dirs(t, true)
	in := input(d, t0, 91, false)
	in.Routable = false
	if got := Tick(in); got != "" {
		t.Fatalf("said something with nothing routable seen: %q", got)
	}
	in = input(d, t0.Add(time.Minute), 91, false)
	in.SavedEach = func() float64 { return 0.20 }
	got := Tick(in)
	for _, w := range []string{"Claude's 5-hour window is at 91%", "Rescue is a Pro feature", "1:30 PM", "1 routable task", "an estimate: about $0.20 saved", "/p0ssess:pro"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in %q", w, got)
		}
	}
	if got := Tick(input(d, t0.Add(2*time.Minute), 91, false)); got != "" {
		t.Fatalf("offer repeated in the same window: %q", got)
	}
	if _, on := Active(d, t0.Add(2*time.Minute)); on {
		t.Fatal("Free never enters rescue")
	}
	// The next window gets its own line, with the count of that window.
	n := t0.Add(6 * time.Hour)
	in = input(d, n, 90, false)
	in.Usage = reading(90, 10, n.Add(time.Hour), t0.Add(72*time.Hour), n)
	if got := Tick(in); !strings.Contains(got, "Rescue is a Pro feature") {
		t.Fatalf("next window: %q", got)
	}
}

func TestFreeEstimateWithoutReceipts(t *testing.T) {
	d := dirs(t, false)
	got := Tick(input(d, t0, 91, false))
	if !strings.Contains(got, "(an estimate)") || strings.Contains(got, "$") {
		t.Fatalf("%q", got)
	}
}

func TestRescueOffSavedThresholdAndEnvelope(t *testing.T) {
	d := dirs(t, false)
	in := input(d, t0, 91, true)
	in.Enabled = false
	if got := Tick(in); got != "" {
		t.Fatalf("rescue off still spoke: %q", got)
	}
	in = input(d, t0, 91, false)
	in.Enabled = false
	if got := Tick(in); got != "" {
		t.Fatalf("rescue off, Free still got the offer: %q", got)
	}
	in = input(d, t0, 91, true)
	in.Room = false
	if got := Tick(in); got != "" {
		t.Fatalf("entered with the envelope used up: %q", got)
	}
	in = input(d, t0, 91, true)
	in.Ready = false
	if got := Tick(in); got != "" {
		t.Fatalf("entered with no key: %q", got)
	}
	// Active, then the envelope runs out: say so once and stop.
	if got := Tick(input(d, t0, 91, true)); got == "" {
		t.Fatal("did not enter")
	}
	in = input(d, t0.Add(time.Minute), 92, true)
	in.Room = false
	if got := Tick(in); !strings.Contains(got, "paused") || !strings.Contains(got, "limit is used up") {
		t.Fatalf("envelope exit: %q", got)
	}
	if _, on := Active(d, t0.Add(time.Minute)); on {
		t.Fatal("still active with no room")
	}
	if got := Tick(in); got != "" {
		t.Fatalf("paused line repeated: %q", got)
	}
}

func TestLapsedProAndTurnedOffEndSilently(t *testing.T) {
	d := dirs(t, false)
	Tick(input(d, t0, 91, true))
	in := input(d, t0.Add(time.Minute), 91, true)
	in.Enabled = false
	if got := Tick(in); got != "" {
		t.Fatalf("turning rescue off said %q", got)
	}
	if _, on := Active(d, t0.Add(time.Minute)); on {
		t.Fatal("still active")
	}
}

func TestFunnelEventsOnlyWhenOptedIn(t *testing.T) {
	for _, opt := range []bool{false, true} {
		d := dirs(t, opt)
		Tick(input(d, t0, 91, true))
		in := input(d, t0.Add(time.Minute), 91, true)
		in.Runs = func(time.Time) int { return 2 }
		Tick(in)
		Tick(in)
		f := funnelFile(d)
		if !opt {
			if f != "" {
				t.Fatalf("counted while off: %s", f)
			}
			continue
		}
		if !strings.Contains(f, `"rescue_offered":1`) || !strings.Contains(f, `"rescue_used":1`) {
			t.Fatalf("counts: %s", f)
		}
	}
	// A Free offer counts as offered, not used.
	d := dirs(t, true)
	Tick(input(d, t0, 91, false))
	if f := funnelFile(d); !strings.Contains(f, `"rescue_offered":1`) || strings.Contains(f, "rescue_used") {
		t.Fatalf("free counts: %s", f)
	}
}
