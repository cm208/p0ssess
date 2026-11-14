package local

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"p0ssess/internal/budget"
	"p0ssess/internal/history"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/pme/remote"
	"p0ssess/internal/pme/table"
	"p0ssess/internal/rules"
)

// RemoteTimeout is how long a recommendation waits for the engine service before it answers from the
// route table.
var RemoteTimeout = 4 * time.Second

// HistoryRows caps the outcome counts sent with a request.
const HistoryRows = 500

const privateNote = "this repository has a .p0ssess-private file: no delegation from here"

const noLeadNote = "no Claude lead (lead = none): only routes p0ssess runs itself are offered"

// Recommend ranks the routes for a task. The engine service scores them; when it cannot be reached
// (or answers anything but a good 200) the signed route table answers instead, without a word. In a
// repository with a .p0ssess-private file nothing is sent. Approval, the monthly cap, rule wording,
// suggestions and the offer record are always worked out here.
func (e *Engine) Recommend(req Request) (*Recommendation, error) {
	now := e.Now()
	req.Scope = e.Resolve(req.Scope)
	cfg, err := e.Config()
	if err != nil {
		return nil, err
	}
	bud, err := e.Budget()
	if err != nil {
		return nil, err
	}
	if req.UseCase == "" && req.Profile == nil {
		return nil, ErrNoUseCase
	}
	stakes := req.Stakes
	if stakes == 0 && req.Profile != nil {
		stakes = req.Profile.Stakes
	}
	if stakes < 0 || stakes > 4 {
		return nil, fmt.Errorf("stakes must be 1-4, got %d", stakes)
	}
	var notes []string
	rs, err := e.Rules(req.Scope)
	if err != nil {
		notes = append(notes, "rules: "+err.Error()+" (the tools refuse to spend until this is fixed)")
	}
	ledger := e.Ledger()
	us := e.Usage()
	t := e.facts()

	noLead := cfg.NoLead()
	lead := LeadID(req.Lead)
	if lead == "" {
		lead = LeadID(us.LeadModel)
	}
	if lead != "" && !t.Empty() && t.Model(lead) == nil || noLead {
		lead = ""
	}
	plan := bud.Plan
	if plan == "auto" {
		plan = "api"
		if us.Subscriber {
			plan = "subscription"
		} else if !noLead {
			notes = append(notes, "plan unknown: Claude priced at list (set plan in budget.toml)")
		}
	}
	pressure := us.Project(now, cfg.Lambda.EMin, time.Duration(cfg.Lambda.StaleMinutes)*time.Minute)
	lam := LambdaFor(plan, bud.UsageCredits, bud.SaveClaude, pressure, cfg.Lambda)

	private := paths.Private(req.Cwd)
	var resp *remote.Response
	if !private {
		w := e.wire(req, stakes, lead, lam.Value, bud, rs, ledger, now)
		w.NoLead = noLead
		resp = e.ask(w)
	}
	fromTable := resp == nil
	if fromTable {
		if lead == "" {
			lead = cfg.DefaultLead
		}
		if noLead {
			resp, err = fromNoLeadRoutes(t, req, stakes, cfg)
		} else {
			resp, err = fromRoutes(t, req, stakes, lead, lam.Value, cfg, private)
		}
		if err != nil {
			return nil, err
		}
	}
	if noLead {
		resp = withoutLead(resp)
		notes = append(notes, noLeadNote)
	}
	if private {
		notes = append(notes, privateNote)
	}
	return e.finish(req, resp, fromTable, private, rs, bud, cfg, ledger, lam, notes, now), nil
}

// wire builds the request the engine service takes.
func (e *Engine) wire(req Request, stakes int, lead string, lam float64, bud budget.Budget, rs rules.Set, ledger *history.Ledger, now time.Time) remote.Request {
	top := req.Top
	if top <= 0 {
		top = 5
	}
	w := remote.Request{UseCase: req.UseCase, Profile: req.Profile.wire(), Stakes: stakes, LeadModel: lead, Lambda: round(lam), Top: min(top, 20),
		Lists:   remote.Lists{ModelsAllow: list(bud.Models.Allow), ModelsDeny: list(bud.Models.Deny), ProvidersAllow: list(bud.Providers.Allow), ProvidersDeny: list(bud.Providers.Deny)},
		Privacy: remote.Privacy{Mode: bud.Privacy.Mode, AllowSafetyRetention: bud.Privacy.AllowSafetyRetention},
		Tuning:  Tuning(e.Dirs.EngineFile()), Rules: []remote.Rule{}, History: []remote.HistoryRow{}}
	if w.UseCase == "" && w.Profile == nil {
		w.Profile = &remote.Profile{}
	}
	for _, r := range rs.Active(now) {
		switch r.Kind {
		case rules.KindForbid, rules.KindAllow, rules.KindOnly, rules.KindMix:
		default:
			continue
		}
		wr := remote.Rule{ID: r.ID, Scope: r.Scope, Kind: r.Kind, Models: r.Models, UseCases: r.UseCases, Kinds: r.Kinds}
		switch r.Kind {
		case rules.KindForbid, rules.KindAllow:
			wr.Target = r.Target
		case rules.KindMix:
			wr.Weights = r.Weights
		}
		if r.Kind == rules.KindAllow {
			wr.MaxStakes = r.MaxStakes
		}
		w.Rules = append(w.Rules, wr)
	}
	for _, c := range ledger.UseCaseCounts(HistoryRows) {
		w.History = append(w.History, remote.HistoryRow{UseCase: c.UseCase, Model: c.Model, Accepted: c.Accepted, Redo: c.Redo,
			Declined: c.Declined, Overridden: c.Overridden})
	}
	return w
}

func list(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// ask calls the engine service. Any failure gives nil, and the route table answers.
func (e *Engine) ask(r remote.Request) *remote.Response {
	if e.Remote == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), RemoteTimeout)
	defer cancel()
	resp, err := e.Remote.Recommend(ctx, r)
	bad := err != nil || resp.UseCase.ID == "" || resp.Stakes < 1 || resp.Stakes > 4
	e.SetOffline(bad)
	e.RefreshInBackground()
	if bad {
		return nil
	}
	return resp
}

// fromRoutes answers from the route table.
func fromRoutes(t *table.Table, req Request, stakes int, lead string, lam float64, cfg Config, private bool) (*remote.Response, error) {
	r := &remote.Response{V: 1, LeadModel: lead, Work: req.Profile.work(), Pick: -1}
	var u *table.UseCase
	if req.UseCase != "" {
		u = t.UseCase(req.UseCase)
		if u == nil {
			if !t.Empty() {
				return nil, fmt.Errorf("unknown use case %q", req.UseCase)
			}
			r.UseCase.ID = req.UseCase
			return claudeOnly(r, max(stakes, 2), cfg, "no route table is available yet; keeping this on Claude"), nil
		}
	} else if u = classify(t, req.Profile); u == nil {
		return claudeOnly(r, max(stakes, 2), cfg, "no use case matches this profile; pass use_case to route it"), nil
	}
	if stakes == 0 {
		stakes = u.DefaultStakes
		if stakes == 0 {
			stakes = 2
			r.StakesNote = "this job inherits the stakes of the work it carries; pass stakes (assumed 2)"
		} else if lo, hi := u.StakesBounds(); lo != hi {
			r.StakesNote = fmt.Sprintf("catalog default %d of %d-%d; rate the instance", stakes, lo, hi)
		}
	}
	lo, hi := u.StakesBounds()
	r.Stakes, r.StakesCeiling = stakes, cfg.Ceilings.At(stakes)
	r.UseCase = remote.UseCase{ID: u.ID, N: u.N, Task: u.Task, Kind: u.Kind, Family: u.Family, Path: u.Path, Verify: u.Verify,
		StakesRange: []int{lo, hi}, EditsCode: u.EditsCode, Preprocess: u.Preprocess}
	if u.EditsCode {
		r.UseCase.Verify = "full"
	}
	rt := t.Route(u.ID, stakes, lam)
	claudeOnlyRoutes := private
	if rt == nil {
		best := -1
		for _, l := range t.Levels(u.ID) {
			if best < 0 || abs(l-stakes) < abs(best-stakes) || abs(l-stakes) == abs(best-stakes) && l > best {
				best = l
			}
		}
		if best > 0 {
			rt = t.Route(u.ID, best, lam)
		}
		claudeOnlyRoutes = claudeOnlyRoutes || stakes >= 4
	}
	if rt == nil {
		r.ClaudeFeasible = claudeCan(u)
		if r.ClaudeFeasible {
			r.Options = []remote.Option{claudeOption(lead)}
		}
		return r, nil
	}
	r.ClaudeFeasible = rt.ClaudeFeasible
	hasClaude := false
	for _, o := range rt.Options {
		o.Notes = append([]string(nil), o.Notes...)
		o.ApprovalReasons, o.NeedsApproval, o.ApprovalID = nil, false, ""
		if o.Route == RouteClaude {
			o.Model, hasClaude = lead, true
		}
		if claudeOnlyRoutes && o.Route != RouteClaude && o.Route != RouteLocal {
			continue
		}
		r.Options = append(r.Options, o)
	}
	if private && r.ClaudeFeasible && !hasClaude {
		r.Options = append(r.Options, claudeOption(lead))
	}
	return r, nil
}

// fromNoLeadRoutes answers from the table's no-lead routes. A table signed before 1.0.2 has none; then
// the delegated options of the Claude-lead routes are all there is, and a job those keep on Claude gets
// no route until the table is refreshed.
func fromNoLeadRoutes(t *table.Table, req Request, stakes int, cfg Config) (*remote.Response, error) {
	if !t.HasNoLead() {
		r, err := fromRoutes(t, req, stakes, cfg.DefaultLead, 1, cfg, false)
		if err == nil && len(withoutLead(r).Options) == 0 {
			r.Notes = append(r.Notes, "this route table has no routes for a caller with no Claude lead; run p0ssess engine refresh")
		}
		return r, err
	}
	r, err := fromRoutes(t, req, stakes, cfg.DefaultLead, 1, cfg, false)
	if err != nil || r.UseCase.ID == "" || t.UseCase(r.UseCase.ID) == nil {
		return r, err
	}
	r.Options = nil
	if rt := t.NoLeadRoute(r.UseCase.ID, r.Stakes); rt != nil && (rt.Stakes == r.Stakes || r.Stakes < 4) {
		for _, o := range rt.Options {
			o.Notes = append([]string(nil), o.Notes...)
			o.ApprovalReasons, o.NeedsApproval, o.ApprovalID = nil, false, ""
			r.Options = append(r.Options, o)
		}
	}
	return r, nil
}

// withoutLead drops the routes that need a Claude session: Claude itself, a Claude subagent and a
// local tool Claude would run.
func withoutLead(r *remote.Response) *remote.Response {
	opts := r.Options[:0:0]
	for _, o := range r.Options {
		switch o.Route {
		case RouteClaude, RouteSubagent, RouteLocal:
			continue
		}
		opts = append(opts, o)
	}
	r.Options, r.LeadModel, r.ClaudeFeasible = opts, LeadNone, false
	return r
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// claudeCan says from a use case's modalities whether Claude can do it in the session.
func claudeCan(u *table.UseCase) bool {
	for _, m := range u.Inputs {
		if m != "text" && m != "image" && m != "file" {
			return false
		}
	}
	for _, m := range u.Outputs {
		if m != "text" {
			return false
		}
	}
	return true
}

func claudeOption(lead string) remote.Option {
	return remote.Option{Rank: 1, Route: RouteClaude, Model: lead, Review: "Claude's own work", Reason: "keep on Claude"}
}

func claudeOnly(r *remote.Response, stakes int, cfg Config, note string) *remote.Response {
	r.Stakes, r.StakesCeiling, r.ClaudeFeasible = min(stakes, 4), cfg.Ceilings.At(min(stakes, 4)), true
	r.Options = []remote.Option{claudeOption(r.LeadModel)}
	r.Notes = append(r.Notes, note)
	return r
}

// finish applies everything that depends on the user's money, rules and approvals.
func (e *Engine) finish(req Request, resp *remote.Response, fromTable, private bool, rs rules.Set, bud budget.Budget, cfg Config,
	ledger *history.Ledger, lam Lambda, notes []string, now time.Time) *Recommendation {
	uc := resp.UseCase
	d := rs.Decide(uc.ID, uc.Kind, now)
	bud, overrides := effectiveBudget(bud, d, &rs)
	notes = append(notes, e.IgnoredNotes(req.Scope, rs)...)
	spend := ledger.Spend(now)
	daily := bud.DailyLimit(spend.MonthBeforeToday, now)
	t := e.facts()

	rec := &Recommendation{
		UseCase: UseCaseInfo{ID: uc.ID, N: uc.N, Task: uc.Task, Kind: uc.Kind, Family: uc.Family, Path: uc.Path, Verify: uc.Verify,
			Method: uc.Method, StakesRange: uc.StakesRange, QualityBar: uc.QualityBar, EditsCode: uc.EditsCode, Preprocess: uc.Preprocess},
		Classification: resp.Classification, Stakes: resp.Stakes, StakesNote: resp.StakesNote, Work: resp.Work, Lead: resp.LeadModel,
		Lambda: lam, ClaudeFeasible: resp.ClaudeFeasible, Ceiling: resp.StakesCeiling, DataAsOf: resp.DataAsOf,
	}
	if rec.Lead == "" || cfg.NoLead() {
		rec.Lead = cfg.DefaultLead
		if cfg.NoLead() {
			rec.Lead = LeadNone
		}
	}
	for _, x := range resp.Excluded {
		if x.Why == "rule" {
			x.Why = ruleWhy(rs, x.RuleID)
		}
		rec.Excluded = append(rec.Excluded, x)
	}

	off := ""
	switch {
	case private:
		off = "this repository has a .p0ssess-private file"
	case d.ForbidDelegation != nil:
		off = "rule: " + d.ForbidDelegation.String()
	}
	var opts []Option
	offHit := false
	for _, o := range resp.Options {
		if o.Route != RouteClaude && o.Route != RouteLocal && off != "" {
			offHit = true
			continue
		}
		if why := dropWhy(o, d, bud, t, resp.Stakes, spend); why != "" {
			rec.Excluded = append(rec.Excluded, Exclusion{Model: o.Model, Route: o.Route, Why: why})
			continue
		}
		opts = append(opts, o)
	}
	if offHit {
		rec.Excluded = append(rec.Excluded, Exclusion{Model: "*", Why: "no delegation: " + off})
	}
	if fromTable {
		applyMix(opts, d, ledger, uc.ID)
	}
	for i := range opts {
		opts[i].Rank = i + 1
		approve(&opts[i], d, bud, cfg, ledger, spend, daily, resp.Stakes, uc.EditsCode, now)
	}
	if len(opts) > 0 {
		p := opts[0]
		for _, o := range opts {
			if o.MixNext && o.Total <= opts[0].Total*1.5 {
				p = o
				break
			}
		}
		rec.Pick = &p
		rec.Announce = announce(p, uc.Task)
	} else {
		rec.Announce = "no route fits this task"
	}
	top := req.Top
	if top <= 0 {
		top = 5
	}
	if len(opts) > top {
		opts = opts[:top]
	}
	rec.Options = opts

	rec.Budget = BudgetLine{PerTaskUSD: bud.PerTaskUSD, DailyUSD: finite(daily), MonthlyUSD: bud.MonthlyUSD,
		SpentToday: spend.Today, SpentMonth: spend.Month, SaveClaude: bud.SaveClaude}
	rec.Privacy = "no-training: every call sends provider.data_collection=deny"
	if bud.Privacy.Mode == "zdr" {
		rec.Privacy = "zero-retention endpoints only (zdr=true)"
	}
	rec.Notes = append(notes, resp.Notes...)
	if resp.SecondOpinion != nil && !private {
		if so, note := e.secondOpinion(resp.SecondOpinion, bud, rs, ledger, spend, daily, cfg, now); so != nil {
			rec.SecondOpinion = so
		} else if note != "" {
			rec.Notes = append(rec.Notes, note)
		}
	}
	rec.Rules = append(rec.Rules, overrides...)
	rec.Rules = append(rec.Rules, applied(d)...)
	for _, s := range ledger.Suggest(rs.Active(now), bud.PerTaskUSD) {
		if contains(s.Rule.UseCases, uc.ID) || uc.Kind != "" && contains(s.Rule.Kinds, uc.Kind) {
			rec.Suggestions = append(rec.Suggestions, s)
		}
	}

	if req.Record {
		rec.OfferID = history.NewOfferID()
		var os []history.OfferOption
		for _, o := range rec.Options {
			os = append(os, history.OfferOption{Model: o.Model, Route: o.Route, Total: round(o.Total), Cash: round(o.Breakdown.Cash),
				PFail: round(o.PFail), Approve: o.NeedsApproval, ApprovalID: o.ApprovalID})
		}
		err := history.Append(e.Dirs.HistoryFile(), history.Entry{
			TS: now.UTC(), Type: history.TypeOffer, OfferID: rec.OfferID, UseCase: uc.ID, Kind: uc.Kind, Family: uc.Family,
			Stakes: rec.Stakes, Project: paths.ProjectRoot(req.Cwd), Session: req.Session, Options: os,
			Lambda: round(lam.Value), Preset: lam.Preset,
		})
		if err != nil {
			rec.Notes = append(rec.Notes, "history: "+err.Error())
		}
	}
	return rec
}

// ruleWhy words an exclusion the engine made for one of the user's rules.
func ruleWhy(rs rules.Set, id string) string {
	for _, r := range rs.Rules {
		if r.ID == id && id != "" {
			return "rule: " + r.String()
		}
	}
	return "rule"
}

// dropWhy applies the user's rules, lists, privacy mode, the stakes gate and the monthly cap to one
// option, and says why it goes, or "".
func dropWhy(o Option, d rules.Decision, bud budget.Budget, t *table.Table, stakes int, spend history.Spend) string {
	zdr := bud.Privacy.Mode == "zdr"
	switch o.Route {
	case RouteClaude, RouteLocal:
		return ""
	case RouteSubagent:
		if d.ForbidClaudeAgents != nil {
			return "rule: " + d.ForbidClaudeAgents.String()
		}
		return ""
	case RouteEngine:
		switch {
		case stakes >= 4:
			return "stakes 4 stays on Claude"
		case zdr:
			return "zero-retention mode: PDF engines have no zero-retention guarantee"
		}
	case RouteDirect, RouteAgent:
		if o.Route == RouteAgent && d.ForbidOpenCode != nil {
			return "rule: " + d.ForbidOpenCode.String()
		}
		if r := d.Forbidden[o.Model]; r != nil {
			return "rule: " + r.String()
		}
		if ok, r := d.OnlyAllows(o.Model); !ok {
			return "rule: " + r.String()
		}
		m := t.Model(o.Model)
		if ok, why := bud.ModelAllowed(o.Model, m.Providers()); !ok {
			return why
		}
		if zdr {
			if why := zdrBlock(m, bud.Privacy.AllowSafetyRetention); why != "" {
				return "zero-retention mode: " + why
			}
		}
		if stakes >= 4 && d.MaxStakesFor(o.Model) < 4 {
			return "stakes 4 stays on Claude unless a rule allows this model"
		}
	}
	if bud.MonthlyUSD >= 0 && o.CashMax > 0 && spend.Month+spend.Reserved+o.CashMax > bud.MonthlyUSD+1e-9 {
		return fmt.Sprintf("would pass the $%.2f monthly cap", bud.MonthlyUSD)
	}
	return ""
}

// approve says whether an option needs the user's yes. It follows the guard: the estimate (list price,
// without the headroom kept for the ceiling) decides, against the per-task limit and the daily room;
// stakes 4 and ask rules always ask; a first run and code edits ask only when the user turned those on.
func approve(o *Option, d rules.Decision, bud budget.Budget, cfg Config, ledger *history.Ledger, spend history.Spend, daily float64,
	stakes int, editsCode bool, now time.Time) {
	o.NeedsApproval, o.ApprovalReasons, o.ApprovalID = false, nil, ""
	if o.Route != RouteDirect && o.Route != RouteAgent && o.Route != RouteEngine {
		return
	}
	est := o.EstimateUSD
	if est <= 0 && o.CashMax > 0 {
		est = o.CashMax / math.Max(1, cfg.CashHeadroom)
	}
	o.EstimateUSD = est
	if est > 0 && spend.Today+spend.Reserved+est > daily+1e-9 {
		o.NeedsApproval = true
		o.ApprovalReasons = []string{fmt.Sprintf("would pass the $%.2f daily soft limit; only a session override of daily_usd lifts it", daily)}
		return
	}
	var reasons []string
	strict := false
	if est > bud.PerTaskUSD+1e-12 {
		reasons = append(reasons, fmt.Sprintf("about $%.3f, above the $%.2f auto-approve limit", est, bud.PerTaskUSD))
	}
	if stakes >= 4 {
		reasons, strict = append(reasons, "stakes 4"), true
	}
	if r := d.AskFor(o.Model); r != nil {
		reasons, strict = append(reasons, "rule: "+r.String()), true
	}
	if editsCode && bud.Ask.CodeEdits {
		reasons = append(reasons, "edits your code")
	}
	if o.Route != RouteEngine && bud.Ask.FirstUse && !ledger.UsedModels()[o.Model] && d.Allowed[o.Model] == nil {
		reasons = append(reasons, "first use of this model")
	}
	if len(reasons) == 0 {
		return
	}
	r, why := d.Approval(o.Model, est, ledger.ApprovalUse(d.Approvals, now))
	if r != nil && strict && r.Scope != rules.ScopeSession {
		r, why = nil, "needs a yes for this run"
	}
	if r != nil {
		o.ApprovalID = r.ID
		o.Notes = append(o.Notes, "covered by standing approval: "+r.String())
		return
	}
	if why != "" {
		reasons = append(reasons, why)
	}
	o.NeedsApproval = true
	o.ApprovalReasons = reasons
}

// applyMix marks the mix member furthest below its share as next.
func applyMix(opts []Option, d rules.Decision, ledger *history.Ledger, useCase string) {
	r := d.Only
	if r == nil || r.Kind != rules.KindMix {
		return
	}
	weights := r.Weights
	if len(weights) != len(r.Models) {
		weights = make([]float64, len(r.Models))
		for i := range weights {
			weights[i] = 1
		}
	}
	sum, total, runs := 0.0, 0.0, map[string]float64{}
	for i, m := range r.Models {
		sum += weights[i]
		runs[m] = float64(ledger.Stats(m, useCase, "").UseCase.Runs())
		total += runs[m]
	}
	best, bestGap := -1, math.Inf(-1)
	for i := range opts {
		idx := -1
		for j, m := range r.Models {
			if m == opts[i].Model {
				idx = j
			}
		}
		if idx < 0 {
			continue
		}
		share := 0.0
		if total > 0 {
			share = runs[opts[i].Model] / total
		}
		if gap := weights[idx]/sum - share; gap > bestGap+1e-12 {
			best, bestGap = i, gap
		}
	}
	if best >= 0 {
		opts[best].MixNext = true
		opts[best].Notes = append(opts[best].Notes, "next in your mix rule")
	}
}

func announce(o Option, task string) string {
	if task != "" {
		task = strings.ToLower(task[:1]) + task[1:]
	}
	with := func(s string) string {
		if task == "" {
			return s
		}
		return s + ": " + task
	}
	switch o.Route {
	case RouteClaude:
		return "keeping this on Claude"
	case RouteLocal:
		return "running " + strings.TrimPrefix(o.Model, "local:") + " locally"
	case RouteSubagent:
		return with(fmt.Sprintf("handing this to a %s subagent", history.Short(o.Model)))
	}
	return with("handing this to " + history.Short(o.Model))
}

func applied(d rules.Decision) []string {
	var out []string
	add := func(r *rules.Rule) {
		if r != nil {
			out = append(out, r.String())
		}
	}
	add(d.ForbidDelegation)
	add(d.ForbidClaudeAgents)
	add(d.ForbidOpenCode)
	add(d.Only)
	for _, r := range d.Restrict {
		add(r)
	}
	add(d.AllowAll)
	seen := map[string]bool{}
	for _, m := range []map[string]*rules.Rule{d.Forbidden, d.Allowed} {
		for _, r := range m {
			if !seen[r.ID] {
				seen[r.ID] = true
				add(r)
			}
		}
	}
	for i := range d.Approvals {
		add(&d.Approvals[i])
	}
	return out
}
