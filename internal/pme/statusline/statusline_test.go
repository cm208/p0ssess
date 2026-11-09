package statusline

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"p0ssess/internal/history"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/usage"
)

func TestSegment(t *testing.T) {
	plain := Settings{Mark: "ᗣ"}
	jobs := []usage.Job{{Model: "xiaomi/mimo-v2.6-flash"}, {Model: "google/gemini-3.1-pro"}}
	cases := []struct {
		name string
		s    State
		room int
		want string
	}{
		{"idle nothing spent", State{MonthlyCap: 10}, 0, ""},
		{"idle spent", State{SpentToday: 0.14, SpentMonth: 1, MonthlyCap: 10}, 0, "ᗣ p0ssess  $0.14 today"},
		{"working narrow", State{SpentToday: 0.14, MonthlyCap: 10, Running: jobs}, 30, "ᗣ p0ssess  2 running  $0.14"},
		{"working wide", State{SpentToday: 0.14, MonthlyCap: 10, Running: jobs}, 200, "ᗣ p0ssess  2 running  $0.14 today  mimo-v2.6-flash, gemini-3.1-pro"},
		{"near budget", State{SpentToday: 0, SpentMonth: 8.10, MonthlyCap: 10}, 0, "ᗣ p0ssess  $8.10 of $10 this month"},
		{"under a cent", State{SpentToday: 0.0035, MonthlyCap: 10}, 0, "ᗣ p0ssess  <$0.01 today"},
		{"tiny room keeps the shortest", State{SpentToday: 0.14, Running: jobs}, 5, "ᗣ p0ssess  2 running"},
	}
	for _, tc := range cases {
		if got := Segment(tc.s, plain, tc.room); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
	colored := Segment(State{SpentMonth: 9, MonthlyCap: 10}, Settings{Mark: "ᗣ", Color: true}, 0)
	if !strings.Contains(colored, colorAmber+"ᗣ") || strings.Contains(colored, "\x1b[31m") || strings.Contains(colored, "\x1b[5m") {
		t.Errorf("near budget should be an amber ghost, never red or blinking: %q", colored)
	}
	if got := Segment(State{SpentToday: 1}, Settings{}, 0); got != "p0ssess  $1 today" {
		t.Errorf("no mark: %q", got)
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
	if got != "[Opus] repo  ᗣ p0ssess  $0.14 today" {
		t.Errorf("Run = %q", got)
	}
	st, _ := usage.Load(d.UsageFile())
	if !st.Subscriber || st.LeadModel != "claude-opus-5-5" || st.ReadingAt.IsZero() || len(st.Sessions) != 1 {
		t.Errorf("usage not recorded: %+v", st)
	}
	// Broken JSON and a failing command still print what they can.
	os.WriteFile(d.EngineFile(), []byte("[statusline]\ncommand = \"exit 3\"\n"), 0o600)
	if got := Run([]byte("{"), d, now); got != "ᗣ p0ssess  $0.14 today" {
		t.Errorf("degraded Run = %q", got)
	}
	os.Remove(d.HistoryFile())
	if got := Run(in, d, now); got != "" {
		t.Errorf("idle with no spend should print nothing, got %q", got)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
