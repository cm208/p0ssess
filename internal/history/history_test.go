package history

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"p0ssess/internal/rules"
)

var now = time.Date(2026, 11, 8, 20, 0, 0, 0, time.UTC)

func ledger(t *testing.T, es ...Entry) (*Ledger, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "history.jsonl")
	for _, e := range es {
		if err := Append(path, e); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	return NewLedger(got), path
}

func out(model, uc, kind, outcome string, cash float64, ts time.Time) Entry {
	return Entry{Type: TypeOutcome, Model: model, UseCase: uc, Kind: kind, Family: "video_understanding", Outcome: outcome, CashUSD: cash, TS: ts}
}

func TestAppendValidates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.jsonl")
	if err := Append(path, Entry{Type: TypeOutcome, Model: "m", Outcome: "great"}); err == nil {
		t.Error("bad outcome accepted")
	}
	if err := Append(path, Entry{Type: TypeOutcome, Outcome: Accepted}); err == nil {
		t.Error("missing model accepted")
	}
	long := strings.Repeat("x", 500)
	Append(path, Entry{Type: TypeOutcome, Model: "m", Outcome: Accepted, Reason: long})
	os.WriteFile(path+".bad", []byte("not json\n"), 0o600)
	es, _ := Read(path)
	if len(es) != 1 || len(es[0].Reason) != MaxReason {
		t.Errorf("reason not truncated: %d", len(es[0].Reason))
	}
}

func TestSpendAndStats(t *testing.T) {
	l, _ := ledger(t,
		out("google/g", "video-understand", "media", Accepted, 0.10, now.Add(-time.Hour)),
		out("google/g", "video-understand", "media", Redo, 0.05, now.Add(-30*time.Hour)),
		out("google/g", "video-bug-repro", "media", Accepted, 0.02, now.Add(-40*24*time.Hour)),
		out("deepseek/d", "code-file-edits", "agent", Declined, 0, now),
	)
	if s := l.Spend(now); s.Month != 0 || s.Runs != 0 {
		t.Errorf("outcomes counted as spend: %+v", s)
	}
	run := func(id, model string, cash float64, ts time.Time) Entry {
		return Entry{Type: TypeRun, RunID: id, Model: model, Route: "direct", CashUSD: cash, TS: ts}
	}
	l.Entries = append(l.Entries, run("d-1", "google/g", 0.10, now.Add(-time.Hour)), run("d-2", "google/g", 0.05, now.Add(-30*time.Hour)),
		run("d-3", "google/g", 0.02, now.Add(-40*24*time.Hour)))
	s := l.Spend(now)
	if math.Abs(s.Today-0.10) > 1e-9 || math.Abs(s.Month-0.15) > 1e-9 || math.Abs(s.MonthBeforeToday-0.05) > 1e-9 || s.Runs != 2 {
		t.Errorf("spend = %+v", s)
	}
	if c, ok := l.RunCost("d-2"); !ok || c != 0.05 {
		t.Errorf("RunCost = %v %v", c, ok)
	}
	if r, ok := l.MatchRun("", "google/g", now.Add(-2*time.Hour)); !ok || r.RunID != "d-1" {
		t.Errorf("MatchRun = %+v %v", r, ok)
	}
	l.Entries = append(l.Entries, Entry{Type: TypeOutcome, Model: "google/g", Outcome: Accepted, RunID: "d-1", TS: now})
	if r, ok := l.MatchRun("", "google/g", now.Add(-2*time.Hour)); ok {
		t.Errorf("claimed run matched again: %+v", r)
	}
	st := l.Stats("google/g", "video-understand", "video_understanding")
	if st.UseCase.Accepted != 1 || st.UseCase.Redo != 1 || st.Family.Offers() != 3 {
		t.Errorf("stats = %+v", st)
	}
	if !l.UsedModels()["google/g"] || l.UsedModels()["deepseek/d"] {
		t.Error("UsedModels")
	}
}

func TestTendencies(t *testing.T) {
	var es []Entry
	for i := 0; i < 9; i++ {
		es = append(es, out("google/gemini-3.8-flash", "video-understand", "media", Accepted, 0.05, now))
	}
	for i := 0; i < 4; i++ {
		es = append(es, out("deepseek/deepseek-v4.1-flash", "code-file-edits", "agent", Declined, 0, now))
	}
	es = append(es, out("deepseek/deepseek-v4.1-flash", "code-file-edits", "agent", Accepted, 0.01, now))
	l, _ := ledger(t, es...)
	ts := l.Tendencies()
	if len(ts) != 2 || ts[0].Text != "accepts gemini-3.8-flash for media 9/9" || !ts[0].Favored {
		t.Fatalf("tendencies = %+v", ts)
	}
	if ts[1].Text != "declines deepseek-v4.1-flash for agent work 4/5" || !ts[1].Disfavored {
		t.Errorf("second = %+v", ts[1])
	}
	sug := l.Suggest(nil, 0.02)
	if len(sug) != 2 {
		t.Fatalf("suggestions = %+v", sug)
	}
	if sug[0].Rule.Kind != rules.KindApprove || sug[0].Rule.MaxPerRunUSD != 0.07 {
		t.Errorf("approval suggestion = %+v", sug[0])
	}
	if sug[1].Rule.Kind != rules.KindForbid || sug[1].Rule.Kinds[0] != "agent" {
		t.Errorf("forbid suggestion = %+v", sug[1])
	}
	covered := []rules.Rule{{Kind: rules.KindApprove, Kinds: []string{"media"}, MaxPerRunUSD: 1}}
	if s := l.Suggest(covered, 0.02); len(s) != 1 {
		t.Errorf("covered use case should not be suggested again: %+v", s)
	}
}

func TestApprovalUse(t *testing.T) {
	rs := []rules.Rule{{ID: "a1", Kind: rules.KindApprove, Period: "day", MaxPerRunUSD: 1}}
	e1 := out("m/x", "u", "media", Accepted, 0.2, now.Add(-time.Hour))
	e1.ApprovalID = "a1"
	e2 := out("m/x", "u", "media", Accepted, 0.3, now.Add(-30*time.Hour))
	e2.ApprovalID = "a1"
	l, _ := ledger(t, e1, e2)
	u := l.ApprovalUse(rs, now)
	if u["a1"].Count != 1 || u["a1"].Total != 0.2 {
		t.Errorf("daily approval use = %+v", u)
	}
}

// TestReadSkipsTornAndOverlongLines checks that a torn final line (no newline, as a crash would
// leave) does not swallow the next appended run, and an overlong corrupt line does not abort the
// scan and make spend read low (which would fail the cap open).
func TestReadSkipsTornAndOverlongLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	// A torn partial line with no trailing newline.
	if err := os.WriteFile(path, []byte(`{"type":"run","run_id":"r1","cash_usd":1`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Append(path, Entry{Type: TypeRun, RunID: "r2", CashUSD: 2, TS: now}); err != nil {
		t.Fatal(err)
	}
	// A very long garbage line between two good runs.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(strings.Repeat("x", 4<<20) + "\n")
	f.Close()
	if err := Append(path, Entry{Type: TypeRun, RunID: "r3", CashUSD: 4, TS: now}); err != nil {
		t.Fatal(err)
	}
	es, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var sum float64
	seen := map[string]bool{}
	for _, e := range es {
		seen[e.RunID] = true
		sum += e.CashUSD
	}
	if !seen["r2"] || !seen["r3"] {
		t.Fatalf("lost a run after a corrupt line: got %v", seen)
	}
	if sum != 6 {
		t.Fatalf("spend read as %v, want 6 (corruption must not lower it)", sum)
	}
}
