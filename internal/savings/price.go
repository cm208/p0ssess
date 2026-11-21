// Package savings turns a run's measured tokens and cost into an honest saving against Claude, and
// rolls the receipts up by month, model and kind of work.
//
// The method (docs/ENGINE.md, "Savings proof"): the tokens a model used are priced at Claude's price
// list for the model Claude Code is running (Sonnet when that is not known) and set against what
// OpenRouter reported for the same call. The difference is the saving; it is negative when Claude would
// have been cheaper, and it is never rounded up to a claim.
package savings

import (
	"p0ssess/internal/calibrate"
	"p0ssess/internal/notices"
)

// Basis values of a priced run.
const (
	Measured  = "measured"
	Estimated = "estimated"
	Unpriced  = "unpriced"
)

// FallbackModel prices the Claude side when the model Claude Code runs is not known.
const FallbackModel = "claude-sonnet-5.5"

// Input is one finished run.
type Input struct {
	CostUSD float64
	// CostEstimated says the cost was priced from tokens, not reported by OpenRouter.
	CostEstimated bool
	Tokens        notices.Tokens
	// Lead is the model Claude Code is running, as its status line reports it; "" when unknown.
	Lead string
}

// Result is a run's Claude price and saving.
type Result struct {
	Basis       string
	ClaudeModel string
	ClaudeUSD   float64
	SavedUSD    float64
	// LeadKnown says the Claude price is for the model Claude Code runs, not the fallback.
	LeadKnown bool
}

// Claude prices tokens at Claude's list price. Cache writes count at the input price, below what the API
// bills, so the Claude side is never overstated. Reasoning tokens are billed as output.
func Claude(lead string, t notices.Tokens) (usd float64, model string, leadKnown bool) {
	p, ok := calibrate.PriceFor(lead)
	if !ok {
		p, _ = calibrate.PriceFor(FallbackModel)
	}
	ct := calibrate.Tokens{In: t.Prompt, Out: t.Completion + t.Reasoning, CacheRead: t.CacheRead, CacheWrite: t.CacheWrite}
	return ct.USD(p), p.Model, ok
}

// Price measures a run. With no token counts nothing is claimed: Basis is Unpriced and the saving 0.
func Price(in Input) Result {
	if in.Tokens.Total() == 0 {
		return Result{Basis: Unpriced}
	}
	claude, model, known := Claude(in.Lead, in.Tokens)
	r := Result{Basis: Measured, ClaudeModel: model, ClaudeUSD: claude, SavedUSD: claude - in.CostUSD, LeadKnown: known}
	if in.CostEstimated {
		r.Basis = Estimated
	}
	return r
}

// Fill writes the result into a receipt record.
func (r Result) Fill(rec *notices.Record, t notices.Tokens) {
	rec.Basis, rec.ClaudeModel, rec.ClaudeUSD, rec.SavedUSD = r.Basis, r.ClaudeModel, r.ClaudeUSD, r.SavedUSD
	if r.Basis != Unpriced {
		tt := t
		rec.Tok = &tt
	}
}
