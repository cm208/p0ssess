package me

import (
	"encoding/json"
	"encoding/xml"
	"strings"
	"testing"
	"time"

	"p0ssess/internal/notices"
	"p0ssess/internal/savings"
)

func seedReceipts(t *testing.T, s *Server) {
	t.Helper()
	now := s.E.Now()
	for _, r := range []notices.Record{
		{TS: now, Receipt: "(kimi, $0.10)", Model: "moonshotai/kimi-k3.1-pro", Kind: "summarize-long-doc", CostUSD: 0.10, ClaudeUSD: 1.10, SavedUSD: 1.00,
			Basis: "measured", ClaudeModel: "claude-sonnet-5.5", Tok: &notices.Tokens{Prompt: 4000, Completion: 500}},
		{TS: now, Receipt: "(grok, $0.40)", Model: "x-ai/grok-9", Kind: "agent-edit", CostUSD: 0.40, ClaudeUSD: 0.30, SavedUSD: -0.10, Rescue: true,
			Basis: "estimated", Tok: &notices.Tokens{Prompt: 900}},
		{TS: now.AddDate(0, -1, 0), Receipt: "(old, $0.10)", Model: "a/old", CostUSD: 0.10, ClaudeUSD: 0.5, SavedUSD: 0.4, Basis: "measured", Tok: &notices.Tokens{Prompt: 10}},
	} {
		if err := notices.Append(s.E.Dirs.NoticesFile(), r); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProofRollsUpTheReceipts(t *testing.T) {
	s, _ := start(t)
	seedReceipts(t, s)
	code, body, _ := get(t, s.URL()+"api/proof", "")
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var v struct {
		This, Last, All savings.Period
		Months          []string
		Monthly         reportView
	}
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatal(err)
	}
	if v.This.Runs != 2 || v.Last.Runs != 1 || v.All.Runs != 3 || len(v.Months) != 2 {
		t.Fatalf("%s", body)
	}
	if d := v.This.SavedUSD - 0.90; d > 1e-9 || d < -1e-9 || v.This.ClaudeCheaper != 1 || v.This.Rescue.Runs != 1 || v.This.TokensKept != 5400 {
		t.Errorf("this month %+v", v.This)
	}
	// A Free install has nothing to send and is told so.
	if v.Monthly.Pro || v.Monthly.Pending != "" {
		t.Errorf("monthly %+v", v.Monthly)
	}
}

func TestProofCard(t *testing.T) {
	s, _ := start(t)
	seedReceipts(t, s)
	code, body, h := get(t, s.URL()+"savings-card.svg", "")
	if code != 200 || h.Get("Content-Type") != "image/svg+xml" {
		t.Fatalf("%d %s", code, h.Get("Content-Type"))
	}
	if err := xml.Unmarshal([]byte(body), new(struct{ XMLName xml.Name })); err != nil {
		t.Fatalf("card is not well-formed: %v", err)
	}
	for _, want := range []string{"~$0.90", "saved against Claude", "kimi-k3.1-pro", "1 measured · 1 estimated", "font/woff2;base64,"} {
		if !strings.Contains(body, want) {
			t.Errorf("card lacks %q", want)
		}
	}
	if !strings.Contains(h.Get("Content-Security-Policy"), "font-src data:") {
		t.Error("the card needs its own CSP to show its font")
	}
	if c, _, h := get(t, s.URL()+"savings-card.svg?month=all&dl=1", ""); c != 200 || !strings.Contains(h.Get("Content-Disposition"), "p0ssess-savings-all.svg") {
		t.Errorf("download %d %v", c, h)
	}
	for _, bad := range []string{"2026-13", "../x", "nov"} {
		if c, _, _ := get(t, s.URL()+"savings-card.svg?month="+bad, ""); c != 400 {
			t.Errorf("month %q: %d", bad, c)
		}
	}
}

// A period Claude would have won is drawn as no saving, never as a positive number.
func TestCardNeverClaimsALoss(t *testing.T) {
	at := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	p := savings.Roll([]notices.Record{{TS: at, Receipt: "(x)", Model: "a/x", CostUSD: 1, ClaudeUSD: 0.2, SavedUSD: -0.8, Basis: "measured", Tok: &notices.Tokens{Prompt: 1}}}, "2026-11", time.UTC)
	svg := SavingsCardSVG(p)
	if !strings.Contains(svg, "no saving") || !strings.Contains(svg, "Claude would have cost $0.80 less") || strings.Contains(svg, "saved against Claude") {
		t.Errorf("card: %s", svg[strings.Index(svg, "<text"):][:600])
	}
	empty := SavingsCardSVG(savings.Roll(nil, "2026-11", time.UTC))
	if !strings.Contains(empty, "nothing to claim yet") {
		t.Error("empty card")
	}
}
