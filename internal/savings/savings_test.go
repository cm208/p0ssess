package savings

import (
	"strings"
	"testing"
	"time"

	"p0ssess/internal/notices"
)

func near(a, b float64) bool { d := a - b; return d < 1e-9 && d > -1e-9 }

func TestPriceUsesClaudeListPrice(t *testing.T) {
	tok := notices.Tokens{Prompt: 1_000_000, Completion: 50_000, Reasoning: 50_000, CacheRead: 1_000_000}
	// Sonnet: 1M in at $2, (50k+50k) out at $10, 1M cache read at $0.10.
	r := Price(Input{CostUSD: 0.30, Tokens: tok})
	if r.Basis != Measured || r.ClaudeModel != "claude-sonnet-5.5" || r.LeadKnown || !near(r.ClaudeUSD, 3.10) || !near(r.SavedUSD, 2.80) {
		t.Errorf("sonnet: %+v", r)
	}
	// The model Claude Code runs prices the Claude side when it is known.
	r = Price(Input{CostUSD: 0.30, Tokens: tok, Lead: "claude-opus-5-5[1m]"})
	if r.ClaudeModel != "claude-opus-5.5" || !r.LeadKnown || !near(r.ClaudeUSD, 4+2+0.2) {
		t.Errorf("opus: %+v", r)
	}
	r = Price(Input{CostUSD: 0.30, Tokens: tok, Lead: "anthropic/claude-haiku-5.5"})
	if r.ClaudeModel != "claude-haiku-5.5" {
		t.Errorf("haiku: %+v", r)
	}
	// An unknown lead falls back to Sonnet.
	if r = Price(Input{CostUSD: 0.30, Tokens: tok, Lead: "some-future-model"}); r.ClaudeModel != "claude-sonnet-5.5" || r.LeadKnown {
		t.Errorf("fallback: %+v", r)
	}
}

func TestPriceMarksEstimatesAndNeverClaimsWithoutTokens(t *testing.T) {
	tok := notices.Tokens{Prompt: 1000}
	if r := Price(Input{CostUSD: 0.001, CostEstimated: true, Tokens: tok}); r.Basis != Estimated {
		t.Errorf("estimated: %+v", r)
	}
	if r := Price(Input{CostUSD: 0.5}); r.Basis != Unpriced || r.SavedUSD != 0 || r.ClaudeUSD != 0 {
		t.Errorf("no tokens: %+v", r)
	}
}

// Claude would have been cheaper: the saving is negative, never clamped or hidden.
func TestPriceNegative(t *testing.T) {
	r := Price(Input{CostUSD: 0.50, Tokens: notices.Tokens{Prompt: 1000, Completion: 100}})
	if r.SavedUSD >= 0 || !near(r.ClaudeUSD, 0.003) || !near(r.SavedUSD, 0.003-0.50) {
		t.Errorf("%+v", r)
	}
}

func rec(ts string, model, kind string, cost, claude float64, basis string, rescue bool, toks int) notices.Record {
	at, _ := time.Parse(time.RFC3339, ts)
	r := notices.Record{TS: at, Receipt: "(r)", Model: model, Kind: kind, CostUSD: cost, ClaudeUSD: claude, SavedUSD: claude - cost, Basis: basis, Rescue: rescue}
	if basis == Unpriced {
		r.ClaudeUSD, r.SavedUSD = 0, 0
	}
	if toks > 0 {
		r.Tok = &notices.Tokens{Prompt: toks}
	}
	return r
}

func TestRoll(t *testing.T) {
	recs := []notices.Record{
		rec("2026-10-30T10:00:00Z", "a/kimi", "summarize-long-doc", 0.10, 1.10, Measured, false, 100),
		rec("2026-11-02T10:00:00Z", "a/kimi", "summarize-long-doc", 0.10, 1.10, Measured, false, 1000),
		rec("2026-11-03T10:00:00Z", "b/grok", "agent-edit", 0.40, 0.30, Estimated, true, 2000),
		rec("2026-11-04T10:00:00Z", "b/grok", "agent-edit", 0.05, 0, Unpriced, false, 0),
		{TS: time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC), Notes: []string{"no receipt"}},
		// A receipt from before 1.2: only a positive saving was kept.
		{TS: time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC), Receipt: "(old)", Model: "c/old", CostUSD: 0.2, SavedUSD: 0.8},
	}
	rep := Build(recs, time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC))
	n := rep.This
	if n.Runs != 4 || n.Priced != 3 || n.Measured != 1 || n.Estimated != 2 || n.Unpriced != 1 {
		t.Fatalf("this month counts: %+v", n.Totals)
	}
	// saved: 1.00 (kimi) - 0.10 (grok, Claude cheaper) + 0.80 (old) = 1.70
	if !near(n.SavedUSD, 1.70) || n.ClaudeCheaper != 1 || !near(n.CostUSD, 0.75) || !near(n.PricedCostUSD, 0.70) {
		t.Errorf("this month sums: %+v", n.Totals)
	}
	if n.TokensKept != 3000 {
		t.Errorf("tokens kept %d", n.TokensKept)
	}
	if n.Rescue.Runs != 1 || n.Normal.Runs != 3 || !near(n.Rescue.SavedUSD, -0.10) {
		t.Errorf("rescue %+v normal %+v", n.Rescue, n.Normal)
	}
	if len(n.ByModel) != 3 || n.ByModel[0].Key != "kimi" || n.ByModel[1].Key != "old" || n.ByModel[2].Key != "grok" {
		t.Errorf("by model %+v", n.ByModel)
	}
	if len(n.ByKind) != 3 {
		t.Errorf("by kind %+v", n.ByKind)
	}
	if rep.Last.Runs != 1 || rep.All.Runs != 5 || len(rep.Months) != 2 || rep.Months[0] != "2026-11" {
		t.Errorf("last %d all %d months %v", rep.Last.Runs, rep.All.Runs, rep.Months)
	}
}

func TestMonthsFollowTheClockLocation(t *testing.T) {
	loc := time.FixedZone("x", -7*3600)
	r := rec("2026-11-01T03:00:00Z", "a/m", "", 0.1, 0.5, Measured, false, 1)
	if got := Roll([]notices.Record{r}, "2026-10", loc); got.Runs != 1 {
		t.Errorf("local month: %+v", got.Totals)
	}
}

func TestHeadlines(t *testing.T) {
	for _, c := range []struct {
		t    Totals
		kind string
		want string
	}{
		{Totals{}, "none", "no runs yet"},
		{Totals{Runs: 2, Unpriced: 2}, "none", "no saving to show yet: no run had token counts"},
		{Totals{Runs: 3, Priced: 3, ClaudeUSD: 4, PricedCostUSD: 1, SavedUSD: 3}, "saved", "~$3.00 saved against Claude (75%)"},
		{Totals{Runs: 3, Priced: 3, ClaudeUSD: 1, PricedCostUSD: 1.5, SavedUSD: -0.5}, "worse", "no saving: Claude would have cost $0.50 less"},
		{Totals{Runs: 3, Priced: 3, ClaudeUSD: 1, PricedCostUSD: 1, SavedUSD: 0.001}, "none", "no saving this period"},
	} {
		if h := HeadlineOf(c.t); h.Kind != c.kind {
			t.Errorf("%+v: kind %s", c.t, h.Kind)
		}
		if got := HeadlineText(c.t); got != c.want {
			t.Errorf("%+v: %q, want %q", c.t, got, c.want)
		}
	}
}

func TestCard(t *testing.T) {
	recs := []notices.Record{
		rec("2026-11-02T10:00:00Z", "a/kimi-k3.1-pro", "summarize-long-doc", 0.10, 1.10, Measured, false, 2_500_000),
		rec("2026-11-03T10:00:00Z", "b/grok-9", "agent-edit", 0.40, 0.30, Estimated, true, 2000),
	}
	out := Card(Roll(recs, "2026-11", time.UTC))
	for _, want := range []string{"p0ssess savings", "November 2026", "~$0.90 saved against Claude (64%)", "kimi-k3.1-pro",
		"agent edits", "summarize long doc", "rescue", "Claude usage kept  2.5M tokens", "1 measured · 1 estimated",
		"Claude was cheaper on 1 of 2 priced runs"} {
		if !strings.Contains(out, want) {
			t.Errorf("card lacks %q:\n%s", want, out)
		}
	}
	for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if n := len([]rune(l)); n != 64 {
			t.Errorf("line is %d columns: %q", n, l)
		}
	}
	empty := Card(Roll(nil, "2026-11", time.UTC))
	if !strings.Contains(empty, "no runs yet") {
		t.Errorf("empty:\n%s", empty)
	}
}
