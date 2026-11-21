package statusline

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"p0ssess/internal/history"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/proc"
	"p0ssess/internal/usage"
)

func TestSegment(t *testing.T) {
	plain := Settings{}
	jobs := []usage.Job{{Model: "xiaomi/mimo-v2.6-flash"}, {Model: "google/gemini-3.1-pro"}}
	one := jobs[:1]
	cases := []struct {
		name string
		s    State
		room int
		want string
	}{
		{"idle", State{DailyLimit: 1, MonthlyCap: 10}, 0, "p0ssess $0/$1"},
		{"spent", State{SpentToday: 0.42, DailyLimit: 1, SpentMonth: 1, MonthlyCap: 10}, 0, "p0ssess $0.42/$1"},
		{"running, the spec's example", State{SpentToday: 0.42, DailyLimit: 1, MonthlyCap: 10, Running: one}, 28, "p0ssess $0.42/$1 · 1 running"},
		{"running wide names the models", State{SpentToday: 0.42, DailyLimit: 1, MonthlyCap: 10, Running: jobs}, 0,
			"p0ssess $0.42/$1 · 2 running (mimo-v2.6-flash, gemini-3.1-pro)"},
		{"tiny room keeps spend", State{SpentToday: 0.42, DailyLimit: 1, Running: jobs}, 10, "p0ssess $0.42/$1"},
		{"paced daily limit", State{SpentToday: 0.1, DailyLimit: 0.33, MonthlyCap: 10}, 0, "p0ssess $0.10/$0.33"},
		{"no daily limit", State{SpentToday: 0.14, MonthlyCap: 10}, 0, "p0ssess $0.14 today"},
		{"under a cent", State{SpentToday: 0.0035, DailyLimit: 1}, 0, "p0ssess <$0.01/$1"},
		{"month cap", State{SpentToday: 0.2, DailyLimit: 1, SpentMonth: 10, MonthlyCap: 10}, 0, "p0ssess month cap $10.00/$10 · paid runs stopped"},
		{"month cap narrow", State{SpentMonth: 10, MonthlyCap: 10, DailyLimit: 1}, 20, "p0ssess stopped"},
		{"team member", State{SpentToday: 0.42, DailyLimit: 1, MonthlyCap: 10, Team: true, TeamSpent: 41, TeamCap: 50}, 0,
			"p0ssess $0.42/$1 · team $41.00/$50"},
		{"team budget used up", State{SpentToday: 0.42, DailyLimit: 1, MonthlyCap: 10, Team: true, TeamSpent: 50, TeamCap: 50, Running: one}, 0,
			"p0ssess team budget $50.00/$50 · paid runs stopped · 1 running (mimo-v2.6-flash)"},
		{"stale server", State{SpentToday: 0.42, DailyLimit: 1, Stale: "1.0.3"}, 0, "p0ssess $0.42/$1 · restart to update"},
		{"stale narrow", State{SpentToday: 0.42, DailyLimit: 1, Stale: "1.0.3", Running: one}, 40, "p0ssess $0.42/$1 · 1 running · restart"},
	}
	for _, tc := range cases {
		if got := Segment(tc.s, plain, tc.room); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
	if got := Segment(State{SpentToday: 1, DailyLimit: 2}, Settings{Mark: "ᗣ"}, 0); got != "ᗣ p0ssess $1.00/$2" {
		t.Errorf("mark: %q", got)
	}
}

func TestLevelsAndColors(t *testing.T) {
	col := Settings{Mark: "ᗣ", Color: true}
	calm := Segment(State{SpentToday: 0.42, DailyLimit: 1, MonthlyCap: 10}, col, 0)
	if !strings.HasPrefix(calm, colorIdle+"ᗣ") || strings.Contains(calm, colorAmber) || strings.Contains(calm, colorRed) {
		t.Errorf("calm: %q", calm)
	}
	near := State{SpentToday: 0.85, DailyLimit: 1, MonthlyCap: 10}
	over := State{SpentToday: 1.4, DailyLimit: 1, SpentMonth: 3, MonthlyCap: 10}
	for _, s := range []State{near, over, {SpentMonth: 8.5, MonthlyCap: 10, DailyLimit: 1}, {Team: true, TeamSpent: 45, TeamCap: 50, DailyLimit: 1}} {
		if s.Level() != Near {
			t.Errorf("%+v: level %v", s, s.Level())
		}
		if got := Segment(s, col, 0); !strings.Contains(got, colorAmber+"p0ssess") || strings.Contains(got, colorRed) {
			t.Errorf("amber: %q", got)
		}
	}
	for _, s := range []State{{SpentMonth: 10, MonthlyCap: 10}, {Team: true, TeamSpent: 50, TeamCap: 50, MonthlyCap: 10}} {
		if got := Segment(s, col, 0); s.Level() != Stopped || !strings.Contains(got, colorRed+"p0ssess") {
			t.Errorf("red: %q", got)
		}
	}
	for _, s := range []State{near, {SpentMonth: 10, MonthlyCap: 10}} {
		if got := Segment(s, col, 0); strings.Contains(got, "\x1b[5m") {
			t.Errorf("blinking: %q", got)
		}
	}
	off := Segment(State{SpentToday: 0.14, DailyLimit: 1, Offline: true}, col, 0)
	if !strings.HasPrefix(off, colorOffline+"ᗣ") || !strings.Contains(off, colorIdle+"p0ssess") {
		t.Errorf("offline dims only the mark: %q", off)
	}
	stale := Segment(State{SpentToday: 0.14, DailyLimit: 1, Stale: "1.0.3"}, col, 0)
	if !strings.HasSuffix(stale, colorAmber+" · restart to update"+reset) || !strings.Contains(stale, colorIdle+"p0ssess $0.14/$1"+reset) {
		t.Errorf("stale marker in amber: %q", stale)
	}
	if got := Segment(near, Settings{Mark: "ᗣ"}, 0); strings.Contains(got, "\x1b") {
		t.Errorf("no color asked: %q", got)
	}
}

func TestVisibleLen(t *testing.T) {
	s := "\x1b[32mmain\x1b[0m \x1b]8;;https://x\x07link\x1b]8;;\x07"
	if got := VisibleLen(s); got != len("main link") {
		t.Errorf("VisibleLen = %d", got)
	}
}

const input = `{"session_id":"s1","cwd":"/tmp","model":{"id":"claude-opus-5-5","display_name":"Opus"},
 "rate_limits":{"five_hour":{"used_percentage":40,"resets_at":%d},"seven_day":{"used_percentage":10,"resets_at":%d}}}`

func TestRunWrapsUserCommand(t *testing.T) {
	if runtime.GOOS == "windows" && proc.GitBash() == "" {
		t.Skip("the wrapped command is a Unix shell line; on Windows it needs Git Bash")
	}
	home := t.TempDir()
	t.Setenv("P0SSESS_HOME", home)
	t.Setenv("COLUMNS", "200")
	t.Setenv("NO_COLOR", "1")
	d := paths.Default()
	os.MkdirAll(d.Config, 0o700)
	os.WriteFile(d.EngineFile(), []byte("[statusline]\ncommand = \"cat >/dev/null; echo '[Opus] repo'\"\n"), 0o600)
	now := time.Now()
	history.Append(d.HistoryFile(), history.Entry{Type: history.TypeRun, RunID: "d-1", Model: "m/x", CashUSD: 0.14, TS: now})
	in := []byte(strings.ReplaceAll(strings.ReplaceAll(input, "%d", "1"), "\"resets_at\":1", "\"resets_at\":"+itoa(now.Add(2*time.Hour).Unix())))
	got := Run(in, d, now)
	if got != "[Opus] repo  ᗣ p0ssess $0.14/$1" {
		t.Errorf("Run = %q", got)
	}
	st, _ := usage.Load(d.UsageFile())
	if !st.Subscriber || st.LeadModel != "claude-opus-5-5" || st.ReadingAt.IsZero() || len(st.Sessions) != 1 {
		t.Errorf("usage not recorded: %+v", st)
	}
	// Broken JSON and a failing command still print what they can.
	os.WriteFile(d.EngineFile(), []byte("[statusline]\ncommand = \"exit 3\"\n"), 0o600)
	if got := Run([]byte("{"), d, now); got != "ᗣ p0ssess $0.14/$1" {
		t.Errorf("degraded Run = %q", got)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestOfflineMarker(t *testing.T) {
	t.Setenv("P0SSESS_HOME", t.TempDir())
	d := paths.Default()
	if Gather(d, "", time.Now()).Offline {
		t.Fatal("offline with no marker")
	}
	os.MkdirAll(d.Cache, 0o700)
	os.WriteFile(d.OfflineMarker(), nil, 0o600)
	if !Gather(d, "", time.Now()).Offline {
		t.Error("the marker should dim the ghost")
	}
}

// The ledger is read once; later refreshes take the cache until a file it depends on changes, so a
// refresh stays well under 20 ms however long the history is.
func TestGatherCachedAndFast(t *testing.T) {
	t.Setenv("P0SSESS_HOME", t.TempDir())
	d := paths.Default()
	now := time.Date(2026, 11, 20, 15, 0, 0, 0, time.Local)
	var es []history.Entry
	for i := 0; i < 20000; i++ {
		es = append(es, history.Entry{Type: history.TypeRun, RunID: "d-" + strconv.Itoa(i), Model: "m/x", CashUSD: 0.0001,
			TS: now.Add(-time.Duration(i) * time.Minute)})
	}
	os.MkdirAll(d.Config, 0o700)
	os.WriteFile(d.BudgetFile(), []byte("monthly_usd = 10\ndaily_usd = 1\n"), 0o600)
	if err := history.Update(d.HistoryFile(), func(*history.Ledger) ([]history.Entry, error) { return es, nil }); err != nil {
		t.Fatal(err)
	}
	first := Gather(d, "s1", now)
	if first.DailyLimit != 1 || first.SpentToday <= 0 || first.SpentMonth <= first.SpentToday {
		t.Fatalf("first: %+v", first)
	}
	start := time.Now()
	const n = 50
	for i := 0; i < n; i++ {
		Gather(d, "s1", now.Add(time.Second))
	}
	if per := time.Since(start) / n; per > 20*time.Millisecond {
		t.Errorf("cached refresh took %v", per)
	}
	// A new run invalidates the cache at once.
	history.Append(d.HistoryFile(), history.Entry{Type: history.TypeRun, RunID: "new", Model: "m/x", CashUSD: 0.5, TS: now})
	if got := Gather(d, "s1", now.Add(2*time.Second)); got.SpentToday < first.SpentToday+0.49 {
		t.Errorf("cache not invalidated: %v then %v", first.SpentToday, got.SpentToday)
	}
	// So does a new budget.
	os.WriteFile(d.BudgetFile(), []byte("monthly_usd = 10\ndaily_usd = 3\n"), 0o600)
	if got := Gather(d, "s1", now.Add(3*time.Second)); got.DailyLimit != 3 {
		t.Errorf("budget change: %+v", got)
	}
}

func TestStaleMarker(t *testing.T) {
	t.Setenv("P0SSESS_HOME", t.TempDir())
	d := paths.Default()
	if Gather(d, "sess-1", time.Now()).Stale != "" {
		t.Fatal("stale with no marker")
	}
	dir := filepath.Dir(d.SessionRulesFile("sess-1"))
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "stale-1.0.3"), nil, 0o600)
	if got := Gather(d, "sess-1", time.Now()).Stale; got != "1.0.3" {
		t.Errorf("stale %q", got)
	}
	if Gather(d, "sess-2", time.Now()).Stale != "" {
		t.Error("another session is not stale")
	}
}

func TestRescueMarker(t *testing.T) {
	s := State{SpentToday: 0.42, DailyLimit: 1, MonthlyCap: 10, Rescue: true}
	if got := Segment(s, Settings{}, 0); got != "p0ssess $0.42/$1 · rescue" {
		t.Errorf("plain: %q", got)
	}
	got := Segment(s, Settings{Color: true}, 0)
	if !strings.Contains(got, colorViolet+"rescue"+reset) {
		t.Errorf("rescue is not violet: %q", got)
	}
	if got := Segment(s, Settings{}, 24); !strings.Contains(got, "rescue") {
		t.Errorf("rescue dropped at narrow room: %q", got)
	}
	s.Rescue = false
	if got := Segment(s, Settings{}, 0); strings.Contains(got, "rescue") {
		t.Errorf("marker without rescue: %q", got)
	}
}
