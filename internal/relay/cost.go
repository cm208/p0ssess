package relay

import (
	"fmt"
	"math"

	"p0ssess/internal/costbasis"
	"p0ssess/internal/jail"
	"p0ssess/internal/opencode"
	"p0ssess/internal/proxy"
)

// How a run's cost was arrived at: Result.CostBasis, cost_basis in the tool result.
const (
	// CostMeasured is OpenRouter's own figure for every model call, read by the key proxy (or, in the
	// cloud sandbox, by the laptop's cost guard).
	CostMeasured = "openrouter"
	// CostBYOK is OpenRouter's figure plus the provider's own charge that OpenRouter reported, for at
	// least one call on the user's own provider key.
	CostBYOK = costbasis.BYOKUpstream
	// CostOpenCode is OpenCode's session cost: no call went through a meter that knew this run.
	CostOpenCode = "opencode"
	// CostTokens prices the tokens of calls with no readable cost at the model's known prices.
	CostTokens = "tokens"
	// CostCap counts the run at its cost cap: the cost could neither be read nor priced.
	CostCap = "cap"
)

// LedgerBasis is a run's cost basis in the ledger's terms (costbasis.Reported, BYOKUpstream, Tokens,
// Reserved).
func LedgerBasis(basis string) string {
	switch basis {
	case CostBYOK:
		return costbasis.BYOKUpstream
	case CostTokens:
		return costbasis.Tokens
	case CostCap:
		return costbasis.Reserved
	}
	return costbasis.Reported
}

// Pricer prices tokens on model at the prices p0ssess knows (its route table); ok is false when it knows
// none. The plugin's server sets it.
type Pricer func(model string, t opencode.Tokens) (float64, bool)

// SetPricer sets how runs whose cost was not reported are priced from their tokens.
func (e *Engine) SetPricer(p Pricer) { e.pricer = p }

// metered is what the jail's cost meter charged the record's current run.
func (e *Engine) metered(rec *SessionRecord) (proxy.RunCost, bool) {
	if rec.Jail == "" || rec.MeterRun == "" {
		return proxy.RunCost{}, false
	}
	return jail.RunCost(rec.Jail, rec.MeterRun)
}

func (e *Engine) runCap(rec *SessionRecord) float64 {
	if rec.Grant.CapUSD > 0 {
		return rec.Grant.CapUSD
	}
	return e.cfg.MaxRunCostUSD
}

func (e *Engine) price(model string, t opencode.Tokens) (float64, bool) {
	if e.pricer == nil || t.Input+t.Output+t.Reasoning+t.Cache.Read+t.Cache.Write <= 0 {
		return 0, false
	}
	v, ok := e.pricer(model, t)
	if !ok || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0, false
	}
	return v, true
}

// runCost decides a run's cost and its basis. The key proxy's metered OpenRouter cost wins whenever it
// saw the run's calls, because OpenCode reports $0 for a model it has no price for. A call with no
// readable cost is priced from its tokens, else the run counts at its cap. Without metered calls,
// OpenCode's cost is used; a $0 cost for tokens that were used is priced from the tokens, else counted at
// the cap. oc is OpenCode's cost for the run (its last reading when ocOK is false), tokens its tokens.
func (e *Engine) runCost(rec *SessionRecord, oc float64, ocOK bool, tokens opencode.Tokens) (float64, string) {
	capUSD := e.runCap(rec)
	if rc, ok := e.metered(rec); ok && rc.Calls > 0 {
		if rc.Unknown == 0 {
			if rc.BYOK > 0 {
				return rc.KnownUSD, CostBYOK
			}
			return rc.KnownUSD, CostMeasured
		}
		if p, ok := e.price(rec.Model, opencode.Tokens{Input: rc.UnknownInput, Output: rc.UnknownOutput}); ok {
			return rc.KnownUSD + p, CostTokens
		}
		return math.Max(math.Max(rc.ChargedUSD, rc.KnownUSD), capUSD), CostCap
	}
	switch {
	case !ocOK:
		return math.Max(oc, capUSD), CostCap
	case oc > 0:
		return oc, CostOpenCode
	case tokens.Input+tokens.Output+tokens.Reasoning+tokens.Cache.Read+tokens.Cache.Write <= 0:
		return 0, CostOpenCode
	}
	if p, ok := e.price(rec.Model, tokens); ok {
		return p, CostTokens
	}
	return capUSD, CostCap
}

// liveCost is the run's spend so far, for the cap check while it runs: the larger of OpenCode's figure
// and the meter's charges.
func (e *Engine) liveCost(rec *SessionRecord, oc float64) float64 {
	if rc, ok := e.metered(rec); ok && rc.ChargedUSD > oc {
		return rc.ChargedUSD
	}
	return oc
}

func costNote(basis string, usd float64) string {
	switch basis {
	case CostBYOK:
		return "some calls ran on your own provider key; their cost is OpenRouter's fee plus the provider's charge OpenRouter reported"
	case CostTokens:
		return "OpenRouter reported no cost for some of this run's calls; they are priced from their tokens at the model's known prices"
	case CostCap:
		return fmt.Sprintf("the run's cost could not be measured or priced; it is counted at its $%.4f cap", usd)
	}
	return ""
}
