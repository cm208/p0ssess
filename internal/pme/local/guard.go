package local

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"p0ssess/internal/history"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/pme/table"
	"p0ssess/internal/rules"
	"p0ssess/internal/team"
)

// Charge is one call about to spend OpenRouter money, as the direct tool or the relay sees it.
type Charge struct {
	Route   string // "direct" or "agent"
	Model   string
	UseCase string // use-case id; empty when the caller did not say
	// Stakes 1-4; 0 takes the use case's default stakes, or 3 when the use case is unknown.
	Stakes int
	// MaxUSD is the most the call may spend: what is reserved and what the limits are checked against.
	MaxUSD float64
	// ExplicitMax is true when the caller chose MaxUSD rather than taking a default. A model with no
	// known price runs only with an explicit maximum above 0.
	ExplicitMax bool
	// EstimateUSD is what the call is expected to cost. Whether the user is asked is decided from it;
	// MaxUSD stays the hard ceiling that is reserved and enforced. 0 means no estimate: MaxUSD is used,
	// except on the agent path, where the engine prices a session of the use case's turns.
	EstimateUSD float64
	// Fit lets a default MaxUSD shrink to the daily and monthly room left, and to a standing approval's
	// per-run limit, when the estimate still fits: the relay stops the run at the lowered ceiling.
	Fit bool
	// Edit is an agent run that may change files (its diff still comes back for review).
	Edit bool
	// Hold is how long the reservation holds before it counts as spent: the call's maximum runtime.
	Hold time.Duration
	// Confirmed means the user already said yes to this call's cost as part of a larger job they
	// approved as a whole (a calibration run). It answers the per-task and first-use asks; a yes the
	// call needs for itself (stakes 4, an ask rule) is still asked, and every cap still applies.
	Confirmed bool
	Scope
}

// Grant is an authorized call. ReserveID names its reservation; settle or release it when the call ends.
type Grant struct {
	ReserveID   string  `json:"reserve_id"`
	MaxUSD      float64 `json:"max_usd"`
	EstimateUSD float64 `json:"estimate_usd"`
	ApprovalID  string  `json:"approval_id,omitempty"`
	ZDR         bool    `json:"zdr,omitempty"`
	Stakes      int     `json:"stakes"`
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
	if math.IsNaN(c.EstimateUSD) || math.IsInf(c.EstimateUSD, 0) {
		c.EstimateUSD = 0
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
	t := e.facts()
	ucID, kind := "", ""
	stakes := c.Stakes
	if c.UseCase != "" {
		// Before any route table is verified, use cases are not known here: the call is then checked as
		// one that names none, which every forbid, only and mix rule meets.
		uc := t.UseCase(c.UseCase)
		switch {
		case uc != nil:
			ucID, kind = uc.ID, uc.Kind
			if stakes == 0 {
				stakes = uc.DefaultStakes
			}
		case !t.Empty():
			return nil, refuse("unknown use case %q", c.UseCase)
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

	m := t.Model(c.Model)
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
	if ok, why := bud.ModelAllowed(c.Model, m.Providers()); !ok {
		return nil, refuse("%s is %s", c.Model, why)
	}
	claude := strings.HasPrefix(c.Model, "anthropic/") || m.IsClaude()
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

	est := c.EstimateUSD
	if est <= 0 && c.Route == "agent" {
		est, _ = e.AgentEstimate(c.Model, ucID)
	}
	if est <= 0 || est > c.MaxUSD {
		est = c.MaxUSD
	}
	g.EstimateUSD = est
	var ask []string
	strict := false // a yes for this run is needed, not a standing approval
	if stakes >= 4 && !claude {
		ask, strict = append(ask, "it is stakes 4"), true
	}
	if r := d.AskFor(c.Model); r != nil {
		ask, strict = append(ask, "your rule says to ask ("+r.String()+")"), true
	}
	if c.Edit && bud.Ask.CodeEdits {
		ask = append(ask, "it edits code and ask.code_edits is on")
	}

	hold := c.Hold
	if hold <= 0 {
		hold = time.Hour
	}
	err = history.Update(e.Dirs.HistoryFile(), func(l *history.Ledger) ([]history.Entry, error) {
		sp := l.Spend(now)
		committed := sp.Month + sp.Reserved
		today := sp.Today + sp.Reserved
		daily := bud.DailyLimit(sp.MonthBeforeToday, now)
		if c.Fit {
			if room := math.Floor(math.Min(bud.MonthlyUSD-committed, daily-today)*1e4) / 1e4; room < g.MaxUSD && room >= est-1e-9 && room > 0 {
				g.MaxUSD = room
				g.Notes = append(g.Notes, fmt.Sprintf("ceiling lowered to $%.4f to stay within your limits", room))
			}
		}
		if committed+g.MaxUSD > bud.MonthlyUSD+1e-9 {
			return nil, refuse("$%.2f spent or reserved this month plus up to $%.2f for this call would pass the $%.2f monthly cap; "+
				"raise monthly_usd first", committed, g.MaxUSD, bud.MonthlyUSD)
		}
		if g.MaxUSD > 0 && today+g.MaxUSD > daily+1e-9 {
			return nil, refuse("$%.2f spent or reserved today plus up to $%.2f would pass the $%.2f daily soft limit. "+
				"Only a session override lifts it: ask the user, and if they agree, rules_add kind \"budget\", field \"daily_usd\", "+
				"scope \"session\" with the new limit", today, g.MaxUSD, daily)
		}
		reasons := ask
		if est > bud.PerTaskUSD+1e-12 {
			reasons = append(reasons, fmt.Sprintf("it is above your $%.2f auto-approve limit", bud.PerTaskUSD))
		}
		if bud.Ask.FirstUse && !l.UsedModels()[c.Model] && d.Allowed[c.Model] == nil {
			reasons = append(reasons, "it is the first run on this model and ask.first_use is on")
		}
		if len(reasons) > 0 && c.Confirmed && !strict {
			reasons = nil
			g.Notes = append(g.Notes, "covered by the yes for the whole job")
		}
		if len(reasons) > 0 {
			r, why := d.Approval(c.Model, est, l.ApprovalUse(d.Approvals, now))
			if r != nil && strict && r.Scope != rules.ScopeSession {
				r, why = nil, "a standing approval does not cover this; it needs a yes for this run"
			}
			if r == nil {
				if why != "" {
					reasons = append(reasons, why)
				}
				return nil, &Refusal{NeedsApproval: true, Reason: approvalAsk(c.Model, c.Session, strict, est, g.MaxUSD, reasons)}
			}
			g.ApprovalID = r.ID
			// A standing approval's per-run limit is a ceiling too, wherever the call's own is a default.
			if c.Fit && r.MaxPerRunUSD < g.MaxUSD && r.MaxPerRunUSD >= est-1e-9 {
				g.MaxUSD = r.MaxPerRunUSD
			}
		}
		g.ReserveID = "rsv-" + history.NewOfferID()[2:]
		exp := now.Add(hold).UTC()
		return []history.Entry{{TS: now.UTC(), Type: history.TypeReserve, RunID: g.ReserveID, Route: c.Route, Model: c.Model,
			CashUSD: g.MaxUSD, ApprovalID: g.ApprovalID, UseCase: cmp.Or(ucID, c.UseCase), Stakes: stakes, Session: c.Session,
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

// DefaultAgentTurns sizes the estimate of an agent run whose use case gives no turns.
const DefaultAgentTurns = 10

// Agent sessions are priced as turns of 40k context read from cache, 3k new input and 1k output, with
// OpenRouter's 5.5% credit fee.
const (
	agentContext = 40000
	agentNewIn   = 3000
	agentOut     = 1000
	creditFee    = 0.055
)

// AgentEstimate is the expected cash of an agent session on model: the use case's turns
// (DefaultAgentTurns when it has none) at list price, with OpenRouter's fee.
func (e *Engine) AgentEstimate(model, useCase string) (float64, bool) {
	t := e.facts()
	turns := DefaultAgentTurns
	if uc := t.UseCase(useCase); uc != nil && uc.Turns > 0 {
		turns = uc.Turns
	}
	bud, _ := e.Budget()
	m := t.Model(model)
	tp, ok := m.Prices(bud.Privacy.Mode == "zdr", false)
	if !ok {
		return 0, false
	}
	v := float64(turns) * (agentContext*tp.CacheRead + agentNewIn*tp.In + agentOut*tp.Out) / 1e6 * (1 + creditFee)
	if v <= 0 && !m.Free() {
		return 0, false
	}
	return v, true
}

// approvalAsk words a call that needs the user's yes: the cost, why, and the exact one-run approval
// that records the yes. The limit is rounded up so the approval covers the call, and the session is
// the one this call resolved, so the arguments work verbatim from any caller. With no session known,
// the yes is a global approval that lapses in an hour; a call that needs a yes for this run cannot
// take one.
func approvalAsk(model, session string, strict bool, est, maxUSD float64, reasons []string) string {
	per := math.Ceil(maxUSD*1e4-1e-9) / 1e4
	cost := fmt.Sprintf("about $%.4f", est)
	if maxUSD > est+1e-9 {
		cost += fmt.Sprintf(" (at most $%.4f)", per)
	}
	head := fmt.Sprintf("%s on %s; %s.", cost, history.Short(model), strings.Join(reasons, "; "))
	a := map[string]any{"scope": rules.ScopeSession, "session_id": session, "max_per_run_usd": per, "max_count": 1, "models": []string{model}}
	if session == "" {
		if strict {
			return head + " No Claude Code session is known here, so a yes for this run cannot be recorded; run it from Claude Code."
		}
		delete(a, "session_id")
		a["scope"], a["expires_in_minutes"] = rules.ScopeGlobal, 60
	}
	args, _ := json.Marshal(a)
	return fmt.Sprintf("%s Ask the user in one line with the cost. If they say yes, call approvals_add with %s, then call again.", head, args)
}

// zdrBlock says why a model cannot run in zero-retention mode, or "" when it can. A model with no
// endpoint data is let through: the call is sent with zdr = true, and OpenRouter refuses it when no
// zero-retention endpoint exists.
func zdrBlock(m *table.Model, allowSafety bool) string {
	switch {
	case m == nil || m.Endpoints == nil:
		return ""
	case m.Batch():
		return "batch endpoints are never zero-retention"
	case m.Endpoints.HasZDR:
		return ""
	case allowSafety && m.Policy() == table.PolicySafetyRetention:
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
	if err := history.Append(e.Dirs.HistoryFile(), history.Entry{TS: e.Now().UTC(), Type: history.TypeSettle, RunID: runID, CashUSD: cost}); err != nil {
		return err
	}
	return team.Settle(e.Dirs, runID, cost)
}
