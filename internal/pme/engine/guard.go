package engine

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"p0ssess/internal/history"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/pme/registry"
)

// Charge is one call about to spend OpenRouter money, as the direct tool or the relay sees it.
type Charge struct {
	Route   string // "direct" or "agent"
	Model   string
	UseCase string // catalog id; empty when the caller did not say
	// Stakes 1-4; 0 takes the use case's catalog stakes, or 3 when the use case is unknown.
	Stakes int
	// MaxUSD is the most the call may spend: what is reserved and what the limits are checked against.
	MaxUSD float64
	// ExplicitMax is true when the caller chose MaxUSD rather than taking a default. A model with no
	// known price runs only with an explicit maximum above 0.
	ExplicitMax bool
	// Hold is how long the reservation holds before it counts as spent: the call's maximum runtime.
	Hold time.Duration
	Scope
}

// Grant is an authorized call. ReserveID names its reservation; settle or release it when the call ends.
type Grant struct {
	ReserveID  string  `json:"reserve_id"`
	MaxUSD     float64 `json:"max_usd"`
	ApprovalID string  `json:"approval_id,omitempty"`
	ZDR        bool    `json:"zdr,omitempty"`
	Stakes     int     `json:"stakes"`
	// PriceUnknown means no price is known for the model, so a measured cost of $0 cannot be trusted.
	PriceUnknown bool     `json:"price_unknown,omitempty"`
	Notes        []string `json:"notes,omitempty"`
	Session      string   `json:"-"`
	Cwd          string   `json:"-"`
}

// Refusal is a call the user's limits do not allow. NeedsApproval means the user can allow it.
type Refusal struct {
	NeedsApproval bool
	Reason        string
}

func (r *Refusal) Error() string {
	if r.NeedsApproval {
		return "needs approval: " + r.Reason
	}
	return "refused: " + r.Reason
}

func refuse(format string, a ...any) error { return &Refusal{Reason: fmt.Sprintf(format, a...)} }

// Authorize applies every limit to a call before it spends, independently of what recommend said, and
// reserves its maximum in the ledger. The checks and the reservation happen under the ledger lock, so
// concurrent calls (in this process or another) cannot each pass the same headroom. It fails closed:
// a budget, rules file or ledger that cannot be read refuses the call.
func (e *Engine) Authorize(c Charge) (*Grant, error) {
	now := e.Now()
	if math.IsNaN(c.MaxUSD) || math.IsInf(c.MaxUSD, 0) || c.MaxUSD < 0 {
		return nil, refuse("max cost must be a finite number, 0 or more")
	}
	c.Scope = e.Resolve(c.Scope)
	if paths.Private(c.Cwd) {
		return nil, refuse("this repository has a .p0ssess-private file; delegation is blocked here")
	}
	bud, err := e.Budget()
	if err != nil {
		return nil, refuse("cannot read the budget, so its limits cannot be enforced: %v", err)
	}
	rs, err := e.Rules(c.Scope)
	if err != nil {
		return nil, refuse("cannot read the rules, so they cannot be enforced: %v", err)
	}
	ucID, kind := "", ""
	stakes := c.Stakes
	if c.UseCase != "" {
		uc := e.Cat.Get(c.UseCase)
		if uc == nil {
			return nil, refuse("unknown use case %q", c.UseCase)
		}
		ucID, kind = uc.ID, uc.Kind()
		if stakes == 0 {
			stakes = uc.DefaultStakes()
		}
	}
	if stakes == 0 {
		stakes = 3
	}
	if stakes < 1 || stakes > 4 {
		return nil, refuse("stakes must be 1-4, got %d", stakes)
	}
	d := rs.Decide(ucID, kind, now)
	bud, _ = effectiveBudget(bud, d, &rs)
	g := &Grant{MaxUSD: c.MaxUSD, Stakes: stakes, Session: c.Session, Cwd: c.Cwd, Notes: e.IgnoredNotes(c.Scope, rs)}

	m := e.Reg.Get(c.Model)
	switch {
	case d.ForbidDelegation != nil:
		return nil, refuse("rule: %s", d.ForbidDelegation)
	case d.Forbidden[c.Model] != nil:
		return nil, refuse("rule: %s", d.Forbidden[c.Model])
	case c.Route == "agent" && d.ForbidOpenCode != nil:
		return nil, refuse("rule: %s", d.ForbidOpenCode)
	}
	if ok, r := d.OnlyAllows(c.Model); !ok {
		return nil, refuse("rule: %s", r)
	}
	var providers []string
	if m != nil && m.Endpoints != nil {
		providers = m.Endpoints.Providers
	}
	if ok, why := bud.ModelAllowed(c.Model, providers); !ok {
		return nil, refuse("%s is %s", c.Model, why)
	}
	claude := strings.HasPrefix(c.Model, "anthropic/") || (m != nil && m.IsClaude())
	if stakes >= 4 && !claude && d.MaxStakesFor(c.Model) < 4 {
		return nil, refuse("stakes 4 stays on Claude unless an allow rule with max_stakes = 4 names %s", c.Model)
	}
	if !m.PriceKnown() && !strings.HasSuffix(c.Model, ":free") {
		if !c.ExplicitMax || !(c.MaxUSD > 0) {
			return nil, refuse("no price is known for %s, so its cost cannot be estimated; pass an explicit max_cost_usd above 0 "+
				"(it is reserved and counted against every limit)", c.Model)
		}
		g.PriceUnknown = true
		g.Notes = append(g.Notes, fmt.Sprintf("no price is known for %s; the call is counted at its $%.4f maximum unless a cost is measured", c.Model, c.MaxUSD))
	}
	if bud.Privacy.Mode == "zdr" {
		if why := zdrBlock(m, bud.Privacy.AllowSafetyRetention); why != "" {
			return nil, refuse("zero-retention mode: %s", why)
		}
		g.ZDR = true
	}

	hold := c.Hold
	if hold <= 0 {
		hold = time.Hour
	}
	err = history.Update(e.Dirs.HistoryFile(), func(l *history.Ledger) ([]history.Entry, error) {
		sp := l.Spend(now)
		committed := sp.Month + sp.Reserved
		if committed+c.MaxUSD > bud.MonthlyUSD+1e-9 {
			return nil, refuse("$%.2f spent or reserved this month plus up to $%.2f for this call would pass the $%.2f monthly cap; "+
				"raise monthly_usd first", committed, c.MaxUSD, bud.MonthlyUSD)
		}
		daily := bud.DailyLimit(sp.MonthBeforeToday, now)
		if today := sp.Today + sp.Reserved; c.MaxUSD > 0 && today+c.MaxUSD > daily+1e-9 {
			return nil, refuse("$%.2f spent or reserved today plus up to $%.2f would pass the $%.2f daily soft limit. "+
				"Only a session override lifts it: ask the user, and if they agree, rules_add kind \"budget\", field \"daily_usd\", "+
				"scope \"session\" with the new limit", today, c.MaxUSD, daily)
		}
		if c.MaxUSD > bud.PerTaskUSD+1e-12 {
			r, why := d.Approval(c.Model, c.MaxUSD, l.ApprovalUse(d.Approvals, now))
			if r == nil {
				if why == "" {
					why = "no standing approval covers it"
				}
				return nil, &Refusal{NeedsApproval: true, Reason: fmt.Sprintf(
					"up to $%.4f on %s is above the $%.2f per-task auto-approve limit, and %s. Ask the user before spending it. "+
						"If they agree, save their yes as a one-run session approval: approvals_add with scope \"session\", "+
						"max_per_run_usd %.4f, max_count 1, models [%q]; its permission prompt is the user's confirmation. Then call again.",
					c.MaxUSD, c.Model, bud.PerTaskUSD, why, c.MaxUSD, c.Model)}
			}
			g.ApprovalID = r.ID
		}
		g.ReserveID = "rsv-" + history.NewOfferID()[2:]
		exp := now.Add(hold).UTC()
		return []history.Entry{{TS: now.UTC(), Type: history.TypeReserve, RunID: g.ReserveID, Route: c.Route, Model: c.Model,
			CashUSD: c.MaxUSD, ApprovalID: g.ApprovalID, UseCase: ucID, Stakes: stakes, Session: c.Session,
			Project: paths.ProjectRoot(c.Cwd), Expires: &exp}}, nil
	})
	if err != nil {
		var r *Refusal
		if errors.As(err, &r) {
			return nil, err
		}
		return nil, refuse("cannot read or write the ledger, so spend cannot be checked: %v", err)
	}
	return g, nil
}

// zdrBlock says why a model cannot run in zero-retention mode, or "" when it can. A model with no
// endpoint data is let through: the call is sent with zdr = true, and OpenRouter refuses it when no
// zero-retention endpoint exists.
func zdrBlock(m *registry.Model, allowSafety bool) string {
	switch {
	case m == nil || m.Endpoints == nil:
		return ""
	case m.Batch():
		return "batch endpoints are never zero-retention"
	case m.Endpoints.HasZDR:
		return ""
	case allowSafety && m.Policy() == registry.PolicySafetyRetention:
		return ""
	}
	return m.ID + " has no zero-retention endpoint"
}

// Settle records the measured (or, when Estimated, the reserved) cost of an authorized call and
// releases its reservation.
func (e *Engine) Settle(g *Grant, r Run) error {
	r.Reserve = g.ReserveID
	if r.ApprovalID == "" {
		r.ApprovalID = g.ApprovalID
	}
	if r.Session == "" {
		r.Session = g.Session
	}
	if r.Cwd == "" {
		r.Cwd = g.Cwd
	}
	return e.RecordRun(r)
}

// Release closes the reservation of a call that was never sent.
func (e *Engine) Release(g *Grant) error {
	if g == nil || g.ReserveID == "" {
		return nil
	}
	return history.Append(e.Dirs.HistoryFile(), history.Entry{TS: e.Now().UTC(), Type: history.TypeRelease, Reserve: g.ReserveID})
}

// Reconcile replaces an estimated run's cost with the measured one.
func (e *Engine) Reconcile(runID string, cost float64) error {
	if math.IsNaN(cost) || math.IsInf(cost, 0) || cost < 0 {
		return errors.New("cost must be a finite number, 0 or more")
	}
	return history.Append(e.Dirs.HistoryFile(), history.Entry{TS: e.Now().UTC(), Type: history.TypeSettle, RunID: runID, CashUSD: cost})
}
