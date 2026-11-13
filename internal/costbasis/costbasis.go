// Package costbasis names how a call's cost was arrived at, and reads a usage block's cost the one way
// every cost-settling path shares.
//
// OpenRouter reports a call's cost in its answer's usage object: cost, is_byok and
// cost_details.upstream_inference_cost. On a call routed through the user's own provider key (BYOK),
// cost is only OpenRouter's small fee (often 0) and the provider bills the inference to the user
// directly; that charge is upstream_inference_cost. Without BYOK, the upstream figure is already part of
// cost.
package costbasis

import "math"

// The basis of a recorded cost, from most to least certain.
const (
	// Reported is OpenRouter's own cost for the call.
	Reported = "reported"
	// BYOKUpstream is OpenRouter's fee plus the provider's charge that OpenRouter reported for a call on
	// the user's own provider key.
	BYOKUpstream = "byok-upstream"
	// Tokens prices the call's tokens at the model's list price: the cost could not be read.
	Tokens = "tokens"
	// Reserved counts the call at the maximum that was reserved for it: the cost could be neither read nor
	// priced.
	Reserved = "reserved"
)

func rank(b string) int {
	switch b {
	case Reported:
		return 1
	case BYOKUpstream:
		return 2
	case Tokens:
		return 3
	case Reserved:
		return 4
	}
	return 0
}

// Combine is the basis of a total made of two parts: the less certain one.
func Combine(a, b string) string {
	if rank(b) > rank(a) {
		return b
	}
	return a
}

// Estimated reports whether a basis is a price rather than a figure the billing side reported.
func Estimated(b string) bool { return b == Tokens || b == Reserved }

func valid(p *float64) bool { return p != nil && !math.IsNaN(*p) && !math.IsInf(*p, 0) && *p >= 0 }

// FromUsage is what a usage block's figures say the call cost the user. ok is false when they do not say:
// no cost was reported, or the call was on the user's own key and the provider's charge is missing (the
// reported cost is then only OpenRouter's fee, not the call's cost).
//
// A call that reports no cost of its own but a provider charge is taken as a BYOK call even without the
// flag.
func FromUsage(cost *float64, byok bool, upstream *float64) (usd float64, basis string, ok bool) {
	if !valid(cost) {
		return 0, "", false
	}
	if byok {
		if !valid(upstream) {
			return 0, "", false
		}
		return *cost + *upstream, BYOKUpstream, true
	}
	if *cost == 0 && valid(upstream) && *upstream > 0 {
		return *upstream, BYOKUpstream, true
	}
	return *cost, Reported, true
}
