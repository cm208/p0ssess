package local

import (
	"fmt"
	"math"
	"time"

	"p0ssess/internal/budget"
	"p0ssess/internal/history"
	"p0ssess/internal/pme/remote"
	"p0ssess/internal/rules"
)

// SecondOpinionUseCase is the use case the cross-family review is priced from.
const SecondOpinionUseCase = "dual-review-high-risk"

// SecondOpinion is an optional, additive review of a high-stakes diff by two models from different
// families. Claude still reviews the diff itself and decides; the two reviews are inputs, never a
// replacement.
type SecondOpinion struct {
	UseCase         string   `json:"use_case"`
	Models          []string `json:"models"`
	CashUSD         float64  `json:"cash_usd"`
	CashMaxUSD      float64  `json:"cash_max_usd"`
	NeedsApproval   bool     `json:"needs_approval"`
	ApprovalReasons []string `json:"approval_reasons,omitempty"`
	ApprovalID      string   `json:"approval_id,omitempty"`
	How             string   `json:"how"`
}

// secondOpinion checks the engine's offered review against the user's rules, lists, monthly cap and
// daily limit, and decides whether it asks first. It returns nil, with a note when that is worth
// saying, when the review does not fit.
func (e *Engine) secondOpinion(in *remote.SecondOpinion, bud budget.Budget, rs rules.Set, ledger *history.Ledger, spend history.Spend,
	daily float64, cfg Config, now time.Time) (*SecondOpinion, string) {
	if in == nil || len(in.Models) < 2 {
		return nil, ""
	}
	useCase := in.UseCase
	if useCase == "" {
		useCase = SecondOpinionUseCase
	}
	kind := rules.TaskReview
	if u := e.facts().UseCase(useCase); u != nil {
		kind = u.Kind
	}
	d := rs.Decide(useCase, kind, now)
	if d.ForbidDelegation != nil {
		return nil, ""
	}
	t := e.facts()
	for _, id := range in.Models {
		if d.Forbidden[id] != nil {
			return nil, ""
		}
		if ok, _ := d.OnlyAllows(id); !ok {
			return nil, ""
		}
		if ok, _ := bud.ModelAllowed(id, t.Model(id).Providers()); !ok {
			return nil, ""
		}
	}
	cashMax := in.CashMaxUSD
	if cashMax <= 0 {
		cashMax = in.CashUSD * math.Max(1, cfg.CashHeadroom)
	}
	so := &SecondOpinion{UseCase: useCase, Models: append([]string(nil), in.Models...), CashUSD: round(in.CashUSD), CashMaxUSD: round(cashMax),
		How: "Optional, in addition to your own review: send the diff file to each model with the direct tool " +
			"(prompt: review this diff for bugs and risks), then reconcile both reviews with your own. You and the user decide."}
	if spend.Month+spend.Reserved+so.CashMaxUSD > bud.MonthlyUSD {
		return nil, fmt.Sprintf("second opinion skipped: up to $%.2f would pass the $%.2f monthly cap", so.CashMaxUSD, bud.MonthlyUSD)
	}
	if daily >= 0 && !math.IsInf(daily, 1) && spend.Today+spend.Reserved+so.CashMaxUSD > daily {
		return nil, fmt.Sprintf("second opinion skipped: up to $%.2f would pass today's $%.2f limit", so.CashMaxUSD, daily)
	}
	per := so.CashUSD / float64(len(so.Models))
	if per > bud.PerTaskUSD {
		if r, _ := d.Approval(so.Models[0], per, ledger.ApprovalUse(d.Approvals, now)); r != nil {
			so.ApprovalID = r.ID
		} else {
			so.NeedsApproval = true
			so.ApprovalReasons = []string{fmt.Sprintf("about $%.3f per review, above the $%.2f auto-approve limit", per, bud.PerTaskUSD)}
		}
	}
	return so, ""
}
