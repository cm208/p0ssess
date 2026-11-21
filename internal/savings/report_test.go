package savings

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"p0ssess/internal/notices"
	"p0ssess/internal/pme/paths"
)

var now = time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)

func octRecs() []notices.Record {
	return []notices.Record{
		rec("2026-10-12T10:00:00Z", "a/kimi", "summarize-long-doc", 0.10, 1.10, Measured, false, 5000),
		rec("2026-10-13T10:00:00Z", "b/grok", "agent-edit", 0.40, 0.30, Estimated, true, 700),
		rec("2026-11-01T10:00:00Z", "a/kimi", "summarize-long-doc", 0.10, 1.10, Measured, false, 5000),
	}
}

func dirs(t *testing.T) paths.Dirs {
	t.Helper()
	d := paths.Dirs{State: t.TempDir()}
	return d
}

func TestMonthlyBodyHoldsFiguresOnly(t *testing.T) {
	m, ok := Pending(dirs(t), octRecs(), now)
	if !ok || m.Month != "2026-10" || m.Runs != 2 || m.PricedRuns != 2 || m.MeasuredRuns != 1 || m.RescueRuns != 1 || m.TokensKept != 5700 {
		t.Fatalf("%+v ok=%v", m, ok)
	}
	if !near(m.SavedUSD, 0.90) || !near(m.CostUSD, 0.50) || !near(m.ClaudeUSD, 1.40) || m.ClaudeCheaperRuns != 1 {
		t.Errorf("figures %+v", m)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(m.Body()), &raw); err != nil {
		t.Fatal(err)
	}
	want := "v month runs priced_runs measured_runs cost_usd claude_usd saved_usd claude_cheaper_runs rescue_runs rescue_saved_usd tokens_kept models kinds"
	for _, k := range strings.Fields(want) {
		delete(raw, k)
	}
	if len(raw) != 0 {
		t.Errorf("unexpected fields %v", raw)
	}
	for _, l := range m.Models {
		if strings.Contains(l.Name, "/") {
			t.Errorf("model label %q is not short", l.Name)
		}
	}
}

func TestSendRules(t *testing.T) {
	calls := 0
	status := 202
	var perr error
	post := func(_ context.Context, b []byte) (int, error) { calls++; return status, perr }
	d := dirs(t)
	ctx := context.Background()

	// Free, unlinked or switched off: nothing is sent and nothing is marked.
	for _, c := range []struct{ enabled, pro, linked bool }{{false, true, true}, {true, false, true}, {true, true, false}} {
		if sent, err := Send(ctx, d, octRecs(), c.enabled, c.pro, c.linked, post, now); sent || err != nil || calls != 0 {
			t.Fatalf("%+v: sent=%v err=%v calls=%d", c, sent, err, calls)
		}
	}
	if readSent(d) != "" {
		t.Error("a Free install marked a month")
	}

	// A server error is retried next time.
	status = 503
	if sent, err := Send(ctx, d, octRecs(), true, true, true, post, now); sent || err != nil && !strings.Contains(err.Error(), "503") || calls != 1 {
		t.Fatalf("503: %v %v %d", sent, err, calls)
	}
	status = 202
	if sent, err := Send(ctx, d, octRecs(), true, true, true, post, now); !sent || err != nil || calls != 2 {
		t.Fatalf("ok: %v %v %d", sent, err, calls)
	}
	// Once only.
	if sent, _ := Send(ctx, d, octRecs(), true, true, true, post, now); sent || calls != 2 {
		t.Errorf("sent twice")
	}
	// The next month goes again.
	if sent, _ := Send(ctx, d, octRecs(), true, true, true, post, time.Date(2027, 1, 3, 0, 0, 0, 0, time.UTC)); sent {
		t.Error("a month with no runs was sent")
	}
	// A refusal is not retried.
	d2 := dirs(t)
	status, calls = 403, 0
	Send(ctx, d2, octRecs(), true, true, true, post, now)
	Send(ctx, d2, octRecs(), true, true, true, post, now)
	if calls != 1 {
		t.Errorf("a refused report was retried %d times", calls)
	}
	// A network error is retried.
	d3 := dirs(t)
	status, perr, calls = 0, errors.New("offline"), 0
	if _, err := Send(ctx, d3, octRecs(), true, true, true, post, now); err == nil {
		t.Error("network error swallowed")
	}
	Send(ctx, d3, octRecs(), true, true, true, post, now)
	if calls != 2 {
		t.Errorf("calls %d", calls)
	}
}

func TestPendingNeedsLastMonthRuns(t *testing.T) {
	if _, ok := Pending(dirs(t), []notices.Record{octRecs()[2]}, now); ok {
		t.Error("this month's run made a report")
	}
	if Last(time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)) != "2025-12" {
		t.Error("January's last month")
	}
}
