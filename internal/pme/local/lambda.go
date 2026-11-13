package local

import (
	"fmt"
	"math"

	"p0ssess/internal/usage"
)

// Lambda is the weight on Claude's list price.
type Lambda struct {
	Value  float64        `json:"value"`
	Plan   string         `json:"plan"`
	Preset string         `json:"preset"`
	Note   string         `json:"note"`
	Usage  usage.Pressure `json:"pressure"`
}

// LambdaFor implements the research's recommended family. On an API-key plan
// λ = 1. On a subscription λ = floor + (1 - floor)·û^k, where û is projected
// end-of-window use from the worse of the 5-hour and weekly windows; it reaches
// 1 (list price) at projected exhaustion. Past exhaustion it stays at 1 with
// usage credits on, and rises by beta·(û - 1) with them off, capped at 3,
// because a blocked window costs more than list price. With no fresh reading
// λ is the floor.
func LambdaFor(plan string, credits, saveClaude bool, p usage.Pressure, lc LambdaConfig) Lambda {
	floor, k, preset := lc.Shape(saveClaude)
	l := Lambda{Plan: plan, Preset: preset, Usage: p}
	if plan == "api" || preset == "list" {
		l.Value = 1
		if plan == "api" {
			l.Note = "API-key plan: Claude at list price"
		} else {
			l.Note = "list preset: Claude at list price"
		}
		return l
	}
	if !p.Known {
		l.Value = floor
		l.Note = p.Note + "; using the floor"
		return l
	}
	u := math.Max(0, p.Projected)
	switch {
	case u <= 1:
		l.Value = floor + (1-floor)*math.Pow(u, k)
	case credits:
		l.Value = 1
	default:
		l.Value = math.Min(3, 1+lc.Beta*(u-1))
	}
	l.Note = fmt.Sprintf("%s; λ %.2f", p.Note, l.Value)
	return l
}
