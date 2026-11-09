package engine

import (
	"fmt"
	"math"
	"time"

	"p0ssess/internal/budget"
	"p0ssess/internal/history"
	"p0ssess/internal/pme/catalog"
	"p0ssess/internal/rules"
)

// SecondOpinionUseCase is the catalog entry the cross-family review is priced from.
const SecondOpinionUseCase = "dual-review-high-risk"

// SecondOpinion is an optional, additive review of a high-stakes diff by two
// models from different families. Claude still reviews the diff itself and
// decides; the two reviews are inputs, never a replacement.
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

// secondOpinion offers the cross-family review for stakes 3-4 work that edits
// code, when it fits the budget. It returns nil otherwise.
func (e *Engine) secondOpinion(uc *catalog.UseCase, stakes int, p *catalog.Profile, bud budget.Budget, rs rules.Set,
	ledger *history.Ledger, spend history.Spend, daily float64, now time.Time) (*SecondOpinion, string) {
	if stakes < 3 || !uc.EditsCode() {
		return nil, ""
	}
	dual := e.Cat.Get(SecondOpinionUseCase)
	if dual == nil {
		return nil, ""
	}
	d := rs.Decide(dual.ID, dual.Kind(), now)
	if d.ForbidDelegation != nil {
		return nil, ""
	}
	zdr := bud.Privacy.Mode == "zdr"
	authors := map[string]bool{}
	var picked []string
	var cash float64
	var ids []string
	if dual.Recommended != nil {
		ids = append(ids, *dual.Recommended)
	}
	ids = append(append(ids, dual.Ensemble...), dual.Alternatives...)
	for _, id := range ids {
		m := e.Reg.Get(id)
		if m == nil || m.IsClaude() || authors[m.Author] || d.Forbidden[id] != nil {
			continue
		}
		if ok, _ := d.OnlyAllows(id); !ok {
			continue
		}
		var providers []string
		if m.Endpoints != nil {
			providers = m.Endpoints.Providers
		}
		if ok, _ := bud.ModelAllowed(id, providers); !ok {
			continue
		}
		tp, ok := m.Prices(zdr, false)
		if !ok {
			continue
		}
		w := dual.WorkFor(p)
		tp = m.ForPrompt(tp, w.InTokens)
		cash += (float64(w.InTokens)*tp.In + float64(w.OutTokens)*tp.Out) / 1e6 * 1.055
		authors[m.Author] = true
		picked = append(picked, id)
		if len(picked) == 2 {
			break
		}
	}
	if len(picked) < 2 {
		return nil, "second opinion unavailable: fewer than two allowed models from different families"
	}
	cfg, _ := e.Config()
	headroom := cfg.CashHeadroom
	if headroom <= 0 {
		headroom = 1.5
	}
	so := &SecondOpinion{UseCase: dual.ID, Models: picked, CashUSD: round(cash), CashMaxUSD: round(cash * headroom),
		How: "Optional, in addition to your own review: send the diff file to each model with the direct tool " +
			"(prompt: review this diff for bugs and risks), then reconcile both reviews with your own. You and the user decide."}
	if spend.Month+so.CashMaxUSD > bud.MonthlyUSD {
		return nil, fmt.Sprintf("second opinion skipped: up to $%.2f would pass the $%.2f monthly cap", so.CashMaxUSD, bud.MonthlyUSD)
	}
	if daily >= 0 && !math.IsInf(daily, 1) && spend.Today+so.CashMaxUSD > daily {
		return nil, fmt.Sprintf("second opinion skipped: up to $%.2f would pass today's $%.2f limit", so.CashMaxUSD, daily)
	}
	per := so.CashMaxUSD / 2
	if per > bud.PerTaskUSD {
		use := ledger.ApprovalUse(d.Approvals, now)
		if r, _ := d.Approval(picked[0], per, use); r != nil {
			so.ApprovalID = r.ID
		} else {
			so.NeedsApproval = true
			so.ApprovalReasons = []string{fmt.Sprintf("up to $%.3f per review, above the $%.2f per-task limit", per, bud.PerTaskUSD)}
		}
	}
	return so, ""
}
