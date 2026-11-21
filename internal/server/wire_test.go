package server

import (
	"context"
	"os"
	"strings"
	"testing"

	"p0ssess/internal/notices"
	"p0ssess/internal/opencode"
	"p0ssess/internal/relay"
	"p0ssess/internal/usage"
)

// The relay's direct path answers with web search when asked, and its notes stay out of the result
// text: they go to the notices file (1.1 decision 2).
func TestDirectRunnerWebAndNotices(t *testing.T) {
	f := &fakeRouter{chat: func() (int, string) { return 200, okChat }}
	withFake(t, f)
	cfg, eng, _ := directEnv(t, "monthly_usd = 1\nper_task_usd = 0.10\ndaily_usd = -1\n")
	run := directRunner(cfg, eng)
	res, err := run(context.Background(), relay.DirectTask{Task: "latest EV battery price news", Web: true, Model: testModel})
	if err != nil || res.Answer != "hi" || res.Route != "direct" || res.RunID == "" {
		t.Fatalf("%+v %v", res, err)
	}
	f.mu.Lock()
	body := f.bodies[len(f.bodies)-1]
	f.mu.Unlock()
	if !strings.Contains(body, `"id":"web"`) {
		t.Errorf("no web plugin in %s", body)
	}
	notice(eng, "r1", []string{"picked inside your limits; gpt-6.1-sol would cost $0.42"}, "daily limit 80% used")
	b, _ := os.ReadFile(eng.Dirs.NoticesFile())
	if !strings.Contains(string(b), "gpt-6.1-sol") || !strings.Contains(string(b), "daily limit") {
		t.Errorf("notices %s", b)
	}
}

// The receipt is one line: model, cost and the saving against the same tokens on Claude.
func TestReceipt(t *testing.T) {
	_, eng, _ := directEnv(t, "monthly_usd = 1\n")
	got := receipt(eng, "moonshotai/kimi-k3.1-pro", 0.10, false, nil)
	if got != "(kimi-k3.1-pro, $0.10)" {
		t.Errorf("no tokens: %q", got)
	}
	// 1M in at $2 and 100k out at $10 is $3.00 on Sonnet; the run cost $0.10.
	got = receipt(eng, "moonshotai/kimi-k3.1-pro", 0.10, false, &opencode.Tokens{Input: 1e6, Output: 1e5})
	if got != "(kimi-k3.1-pro, $0.10, saved ~$2.90)" {
		t.Errorf("receipt %q", got)
	}
}

// A run Claude would have done cheaper makes no saving claim in the line, and the notices record keeps the
// honest negative with its basis.
func TestReceiptNeverClaimsALoss(t *testing.T) {
	_, eng, _ := directEnv(t, "monthly_usd = 1\n")
	tok := &opencode.Tokens{Input: 1000, Output: 100}
	line, m := receiptMeasured(eng, "x-ai/grok-9", 0.50, false, tok)
	if line != "(grok-9, $0.50)" || m.Basis != "measured" || m.SavedUSD >= 0 {
		t.Fatalf("%q %+v", line, m)
	}
	if _, saved := receiptSaved(eng, "x-ai/grok-9", 0.50, false, tok); saved != 0 {
		t.Errorf("saved %v", saved)
	}
	rec := notices.Record{Receipt: line, Model: "x-ai/grok-9", CostUSD: 0.50}
	m.Fill(&rec, tokensOf(tok))
	record(eng, rec)
	got := notices.Receipts(eng.Dirs.NoticesFile())
	if len(got) != 1 || got[0].SavedUSD >= 0 || got[0].Tok == nil || got[0].Tok.Prompt != 1000 || got[0].Basis != "measured" {
		t.Errorf("record %+v", got)
	}
}

// An estimated cost is marked, and the lead model prices the Claude side.
func TestReceiptBasisAndLead(t *testing.T) {
	_, eng, _ := directEnv(t, "monthly_usd = 1\n")
	tok := &opencode.Tokens{Input: 1e6}
	_, m := receiptMeasured(eng, "m/x", 0.10, true, tok)
	if m.Basis != "estimated" || m.ClaudeModel != "claude-sonnet-5.5" || m.LeadKnown {
		t.Errorf("%+v", m)
	}
	if err := usage.Update(eng.Dirs.UsageFile(), func(s *usage.State) { s.LeadModel = "claude-opus-5-5[1m]" }); err != nil {
		t.Fatal(err)
	}
	_, m = receiptMeasured(eng, "m/x", 0.10, false, tok)
	if m.ClaudeModel != "claude-opus-5.5" || !m.LeadKnown || m.ClaudeUSD != 4 {
		t.Errorf("%+v", m)
	}
}
