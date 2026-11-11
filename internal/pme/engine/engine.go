// Package engine is the Predictive Model Engine's front door: it loads the
// shipped data and the user's files, and answers recommend, budget, rules and
// history requests for the CLI, the MCP server, the hook and the local page.
package engine

import (
	"errors"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"p0ssess"
	"p0ssess/internal/budget"
	"p0ssess/internal/history"
	"p0ssess/internal/pme/bench"
	"p0ssess/internal/pme/catalog"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/pme/registry"
	"p0ssess/internal/pme/score"
	"p0ssess/internal/rules"
	"p0ssess/internal/usage"
)

type Engine struct {
	Dirs  paths.Dirs
	Reg   *registry.Registry
	Bench *bench.Set
	Cat   *catalog.Catalog
	Now   func() time.Time

	// reported holds the ignored rules already reported, per session.
	reported sync.Map
}

// Load reads the embedded data and merges the cached live model list.
func Load(d paths.Dirs) (*Engine, error) {
	reg, err := registry.Load(p0ssess.Data)
	if err != nil {
		return nil, err
	}
	if err := reg.MergeLive(d.ModelsCache()); err != nil {
		fmt.Fprintln(os.Stderr, "p0ssess: ignoring live model cache:", err)
	}
	b, err := bench.Load(p0ssess.Data)
	if err != nil {
		return nil, err
	}
	cat, err := catalog.Load(p0ssess.Data)
	if err != nil {
		return nil, err
	}
	return &Engine{Dirs: d, Reg: reg, Bench: b, Cat: cat, Now: time.Now}, nil
}

func (e *Engine) Config() (score.Config, error)  { return score.LoadConfig(e.Dirs.EngineFile()) }
func (e *Engine) Budget() (budget.Budget, error) { return budget.Load(e.Dirs.BudgetFile()) }

func (e *Engine) Ledger() *history.Ledger {
	es, err := history.Read(e.Dirs.HistoryFile())
	if err != nil {
		fmt.Fprintln(os.Stderr, "p0ssess: history:", err)
	}
	return history.NewLedger(es)
}

func (e *Engine) Usage() usage.State {
	s, _ := usage.Load(e.Dirs.UsageFile())
	return s
}

// Scope tells which rule files apply.
type Scope struct {
	Cwd     string `json:"cwd,omitempty"`
	Session string `json:"session_id,omitempty"`
}

// Resolve fills a missing session from Claude Code's environment (the MCP server and the commands it
// runs carry the session that started them, subagents included), else from the latest status line or
// hook reading for the directory.
func (e *Engine) Resolve(s Scope) Scope {
	if s.Cwd == "" {
		if v := os.Getenv("CLAUDE_PROJECT_DIR"); v != "" {
			s.Cwd = v
		} else {
			s.Cwd, _ = os.Getwd()
		}
	}
	if s.Session == "" {
		s.Session = os.Getenv("CLAUDE_CODE_SESSION_ID")
	}
	if s.Session == "" {
		if se, ok := e.Usage().SessionFor(s.Cwd); ok && e.Now().Sub(se.Seen) < 12*time.Hour {
			s.Session = se.ID
		}
	}
	return s
}

func (e *Engine) RuleFiles(s Scope) rules.Files {
	f := rules.Files{Global: e.Dirs.RulesFile()}
	if root := paths.ProjectRoot(s.Cwd); root != "" {
		f.Project = paths.ProjectRulesFile(root)
	}
	if s.Session != "" {
		f.Session = e.Dirs.SessionRulesFile(s.Session)
	}
	return f
}

func (e *Engine) Rules(s Scope) (rules.Set, error) { return rules.Load(e.RuleFiles(s)) }

// IgnoredNotes words the rules that were read but not applied, each once per session (once per
// process when no session is known).
func (e *Engine) IgnoredNotes(s Scope, set rules.Set) []string {
	var out []string
	for _, ig := range set.Ignored {
		key := s.Session + "\x00" + ig.Rule.Scope + "\x00" + ig.Rule.ID + "\x00" + ig.Rule.String()
		if _, seen := e.reported.LoadOrStore(key, true); seen {
			continue
		}
		if ig.Rule.Scope == rules.ScopeProject && !strings.HasPrefix(ig.Why, "invalid") {
			out = append(out, fmt.Sprintf("project rules tried to loosen a limit (%s: %s); ignored — add it yourself if you want it", ig.Rule.String(), ig.Why))
		} else {
			out = append(out, fmt.Sprintf("rule %s (%s) ignored: %s", ig.Rule.ID, ig.Rule.Scope, ig.Why))
		}
	}
	return out
}

// EffectiveBudget applies budget overrides from rules: the user's own (session, else global) set the
// value; a project's only ever lowers it.
func EffectiveBudget(b budget.Budget, d rules.Decision) (budget.Budget, []string) {
	var notes []string
	for field, r := range d.Budget {
		switch field {
		case "per_task_usd":
			b.PerTaskUSD = r.Value
		case "daily_usd":
			b.DailyUSD = r.Value
		case "monthly_usd":
			b.MonthlyUSD = r.Value
		}
		notes = append(notes, r.String())
	}
	for field, r := range d.ProjectBudget {
		applied := false
		switch field {
		case "per_task_usd":
			if r.Value < b.PerTaskUSD {
				b.PerTaskUSD, applied = r.Value, true
			}
		case "monthly_usd":
			if r.Value < b.MonthlyUSD {
				b.MonthlyUSD, applied = r.Value, true
			}
		case "daily_usd":
			if r.Value > 0 {
				b.DailyCapUSD, applied = r.Value, true
			}
		}
		if applied {
			notes = append(notes, r.String())
		}
	}
	sort.Strings(notes)
	return b, notes
}

// ProjectRaises lists the project budget overrides that do not lower the user's own limit: a project
// may only tighten, so these are ignored and reported. own is the budget with the user's overrides only.
func ProjectRaises(own budget.Budget, d rules.Decision) []rules.Ignored {
	var out []rules.Ignored
	for _, r := range d.ProjectCaps {
		cur := map[string]float64{"per_task_usd": own.PerTaskUSD, "monthly_usd": own.MonthlyUSD, "daily_usd": own.DailyUSD}[r.Field]
		if r.Field == "daily_usd" && cur <= 0 {
			continue // any positive project limit is below a paced or absent one
		}
		if r.Value >= cur {
			out = append(out, rules.Ignored{Rule: r, Why: fmt.Sprintf("it would raise the limit above your $%.2f", cur)})
		}
	}
	return out
}

// effectiveBudget applies the overrides and adds project overrides that tried to raise a limit to the
// set's ignored rules.
func effectiveBudget(b budget.Budget, d rules.Decision, set *rules.Set) (budget.Budget, []string) {
	own, _ := EffectiveBudget(b, rules.Decision{Budget: d.Budget})
	set.Ignored = append(set.Ignored, ProjectRaises(own, d)...)
	return EffectiveBudget(b, d)
}

var ccModel = regexp.MustCompile(`^(?:anthropic/)?claude-([a-z]+)-(\d+)[-.](\d+)`)

// LeadID maps a Claude Code model id ("claude-opus-5-5[1m]") to an
// OpenRouter id ("anthropic/claude-opus-5.5").
func LeadID(s string) string {
	m := ccModel.FindStringSubmatch(strings.ToLower(s))
	if m == nil {
		return ""
	}
	return fmt.Sprintf("anthropic/claude-%s-%s.%s", m[1], m[2], m[3])
}

type Request struct {
	UseCase string           `json:"use_case,omitempty"`
	Profile *catalog.Profile `json:"profile,omitempty"`
	Stakes  int              `json:"stakes,omitempty"`
	Lead    string           `json:"lead_model,omitempty"`
	Top     int              `json:"top,omitempty"`
	Record  bool             `json:"-"`
	Scope
}

type UseCaseInfo struct {
	ID          string   `json:"id"`
	N           int      `json:"n"`
	Task        string   `json:"task"`
	Kind        string   `json:"kind"`
	Family      string   `json:"family"`
	Path        string   `json:"path"`
	Verify      string   `json:"verify"`
	Method      string   `json:"verify_method"`
	StakesRange []int    `json:"stakes_range"`
	QualityBar  string   `json:"quality_bar"`
	Ensemble    []string `json:"ensemble,omitempty"`
	RoutingNote string   `json:"stakes_routing_change,omitempty"`
}

type Recommendation struct {
	OfferID        string               `json:"offer_id,omitempty"`
	UseCase        UseCaseInfo          `json:"use_case"`
	Classification []catalog.Match      `json:"classification,omitempty"`
	Stakes         int                  `json:"stakes"`
	StakesNote     string               `json:"stakes_note,omitempty"`
	Work           catalog.Work         `json:"work"`
	Lead           string               `json:"lead_model"`
	Lambda         score.Lambda         `json:"lambda"`
	Pick           *score.Option        `json:"pick"`
	Announce       string               `json:"announce"`
	Options        []score.Option       `json:"options"`
	Excluded       []score.Exclusion    `json:"excluded,omitempty"`
	ClaudeFeasible bool                 `json:"claude_feasible"`
	Ceiling        float64              `json:"stakes_ceiling"`
	Budget         BudgetLine           `json:"budget"`
	Rules          []string             `json:"rules_applied,omitempty"`
	Suggestions    []history.Suggestion `json:"suggestions,omitempty"`
	Privacy        string               `json:"privacy"`
	SecondOpinion  *SecondOpinion       `json:"second_opinion,omitempty"`
	Notes          []string             `json:"notes,omitempty"`
}

type BudgetLine struct {
	PerTaskUSD float64 `json:"per_task_usd"`
	DailyUSD   float64 `json:"daily_limit_usd"`
	MonthlyUSD float64 `json:"monthly_cap_usd"`
	SpentToday float64 `json:"spent_today_usd"`
	SpentMonth float64 `json:"spent_month_usd"`
	SaveClaude bool    `json:"save_claude"`
}

var ErrNoUseCase = errors.New("no use case: pass use_case or a profile")

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
	rec := &Recommendation{}
	var uc *catalog.UseCase
	switch {
	case req.UseCase != "":
		uc = e.Cat.Get(req.UseCase)
		if uc == nil {
			return nil, fmt.Errorf("unknown use case %q", req.UseCase)
		}
	case req.Profile != nil:
		ms := e.Cat.Classify(*req.Profile, 3)
		if len(ms) == 0 {
			return nil, ErrNoUseCase
		}
		rec.Classification = ms
		uc = e.Cat.Get(ms[0].ID)
		if ms[0].Confidence < 0.4 {
			rec.Notes = append(rec.Notes, fmt.Sprintf("weak match (%.2f); pass use_case to be sure", ms[0].Confidence))
		}
	default:
		return nil, ErrNoUseCase
	}
	stakes := req.Stakes
	if stakes == 0 && req.Profile != nil {
		stakes = req.Profile.Stakes
	}
	if stakes == 0 {
		stakes = uc.DefaultStakes()
		if stakes == 0 {
			stakes = 2
			rec.StakesNote = "this job inherits the stakes of the work it carries; pass stakes (assumed 2)"
		} else if lo, hi := uc.StakesBounds(); lo != hi {
			rec.StakesNote = fmt.Sprintf("catalog default %d of %d-%d; rate the instance", stakes, lo, hi)
		}
	}
	if stakes < 1 || stakes > 4 {
		return nil, fmt.Errorf("stakes must be 1-4, got %d", stakes)
	}

	rs, err := e.Rules(req.Scope)
	if err != nil {
		rec.Notes = append(rec.Notes, "rules: "+err.Error()+" (the tools refuse to spend until this is fixed)")
	}
	d := rs.Decide(uc.ID, uc.Kind(), now)
	bud, overrides := effectiveBudget(bud, d, &rs)
	rec.Notes = append(rec.Notes, e.IgnoredNotes(req.Scope, rs)...)
	ledger := e.Ledger()
	spend := ledger.Spend(now)
	daily := bud.DailyLimit(spend.MonthBeforeToday, now)
	us := e.Usage()

	lead := LeadID(req.Lead)
	if lead == "" {
		lead = LeadID(us.LeadModel)
	}
	if lead == "" || e.Reg.Get(lead) == nil {
		lead = cfg.DefaultLead
	}
	plan := bud.Plan
	if plan == "auto" {
		plan = "api"
		if us.Subscriber {
			plan = "subscription"
		} else {
			rec.Notes = append(rec.Notes, "plan unknown: Claude priced at list (set plan in budget.toml)")
		}
	}
	pressure := us.Project(now, cfg.Lambda.EMin, time.Duration(cfg.Lambda.StaleMinutes)*time.Minute)
	lam := score.LambdaFor(plan, bud.UsageCredits, bud.SaveClaude, pressure, cfg.Lambda)

	work := uc.WorkFor(req.Profile)
	in := score.Input{
		UseCase: uc, Work: work, Stakes: stakes, Lead: e.Reg.Get(lead), Subagent: e.Reg.Get(cfg.SubagentModel),
		Models: e.candidates(cfg, uc, d), Bench: e.Bench, Lambda: lam.Value, Cfg: cfg, Budget: bud, Rules: d,
		History: ledger, Spend: spend, DailyLimit: daily, Private: paths.Private(req.Cwd),
	}
	if req.Profile != nil {
		if len(req.Profile.Inputs) > 0 {
			in.Inputs = normalize(req.Profile.Inputs)
		}
		if len(req.Profile.Outputs) > 0 {
			in.Outputs = normalize(req.Profile.Outputs)
		}
		in.NeedsTools = req.Profile.NeedsTools
	}
	if in.Subagent != nil && strings.Contains(lead, "haiku") {
		in.Subagent = nil
	}
	res := score.Score(in)

	top := req.Top
	if top <= 0 {
		top = 5
	}
	rec.UseCase = UseCaseInfo{ID: uc.ID, N: uc.N, Task: uc.Task, Kind: uc.Kind(), Family: uc.Family, Path: uc.Path,
		Verify: res.ReviewLevel, Method: uc.Verify.Method, QualityBar: uc.QualityBar, Ensemble: uc.Ensemble, RoutingNote: uc.RoutingNote}
	lo, hi := uc.StakesBounds()
	rec.UseCase.StakesRange = []int{lo, hi}
	rec.Stakes, rec.Work, rec.Lead, rec.Lambda = stakes, work, lead, lam
	rec.ClaudeFeasible, rec.Ceiling = res.ClaudeFeasible, res.Ceiling
	rec.Excluded = res.Excluded
	rec.Options = res.Options
	if len(rec.Options) > top {
		rec.Options = rec.Options[:top]
	}
	if len(res.Options) > 0 {
		p := res.Options[0]
		for _, o := range res.Options {
			if o.MixNext && o.Total <= p.Total*1.5 {
				p = o
				break
			}
		}
		rec.Pick = &p
		rec.Announce = announce(p, uc)
	} else {
		rec.Announce = "no route fits this task"
	}
	rec.Budget = BudgetLine{PerTaskUSD: bud.PerTaskUSD, DailyUSD: finite(daily), MonthlyUSD: bud.MonthlyUSD,
		SpentToday: spend.Today, SpentMonth: spend.Month, SaveClaude: bud.SaveClaude}
	rec.Privacy = "no-training: every call sends provider.data_collection=deny"
	if bud.Privacy.Mode == "zdr" {
		rec.Privacy = "zero-retention endpoints only (zdr=true)"
	}
	if in.Private {
		rec.Notes = append(rec.Notes, "this repository has a .p0ssess-private file: no delegation from here")
	} else if so, note := e.secondOpinion(uc, stakes, req.Profile, bud, rs, ledger, spend, daily, now); so != nil {
		rec.SecondOpinion = so
	} else if note != "" {
		rec.Notes = append(rec.Notes, note)
	}
	rec.Rules = append(rec.Rules, overrides...)
	for _, r := range applied(d) {
		rec.Rules = append(rec.Rules, r)
	}
	for _, s := range ledger.Suggest(rs.Active(now), bud.PerTaskUSD) {
		if contains(s.Rule.UseCases, uc.ID) || contains(s.Rule.Kinds, uc.Kind()) {
			rec.Suggestions = append(rec.Suggestions, s)
		}
	}

	if req.Record {
		rec.OfferID = history.NewOfferID()
		var opts []history.OfferOption
		for _, o := range rec.Options {
			opts = append(opts, history.OfferOption{Model: o.Model, Route: o.Route, Total: round(o.Total), Cash: round(o.Breakdown.Cash), PFail: round(o.PFail), Approve: o.NeedsApproval, ApprovalID: o.ApprovalID})
		}
		err := history.Append(e.Dirs.HistoryFile(), history.Entry{
			TS: now.UTC(), Type: history.TypeOffer, OfferID: rec.OfferID, UseCase: uc.ID, Kind: uc.Kind(), Family: uc.Family,
			Stakes: stakes, Project: paths.ProjectRoot(req.Cwd), Session: req.Session, Options: opts,
			Lambda: round(lam.Value), Preset: lam.Preset,
		})
		if err != nil {
			rec.Notes = append(rec.Notes, "history: "+err.Error())
		}
	}
	return rec, nil
}

func announce(o score.Option, uc *catalog.UseCase) string {
	task := strings.ToLower(uc.Task[:1]) + uc.Task[1:]
	switch o.Route {
	case score.RouteClaude:
		return "keeping this on Claude"
	case score.RouteLocal:
		return "running " + strings.TrimPrefix(o.Model, "local:") + " locally"
	case score.RouteSubagent:
		return fmt.Sprintf("handing this to a %s subagent: %s", history.Short(o.Model), task)
	}
	return fmt.Sprintf("handing this to %s: %s", history.Short(o.Model), task)
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

// candidates picks the OpenRouter models to score.
func (e *Engine) candidates(cfg score.Config, uc *catalog.UseCase, d rules.Decision) []*registry.Model {
	want := map[string]bool{}
	for _, id := range uc.Candidates() {
		want[id] = true
	}
	// A project's only rule narrows the choice; it never brings in a model the use case does not name.
	if d.Only != nil && d.Only.Scope != rules.ScopeProject {
		for _, id := range d.Only.Models {
			want[id] = true
		}
	}
	for id := range d.Allowed {
		want[id] = true
	}
	switch cfg.Candidates {
	case "evidence":
		for _, m := range e.Reg.Models {
			if len(m.ReferencedBy) > 0 {
				want[m.ID] = true
			}
		}
		for _, id := range e.Bench.Models() {
			want[id] = true
		}
	case "all":
		for id := range e.Reg.Models {
			want[id] = true
		}
	}
	named := map[string]bool{}
	for _, id := range uc.Candidates() {
		named[id] = true
	}
	var out []*registry.Model
	for _, id := range e.Reg.IDs() {
		// Batch endpoints return hours later; only offer them where the
		// catalog names them.
		if strings.HasSuffix(id, ":batch") && !named[id] {
			continue
		}
		if want[id] {
			out = append(out, e.Reg.Get(id))
		}
	}
	return out
}

func normalize(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, catalog.NormalizeModality(s))
	}
	return out
}

func finite(f float64) float64 {
	if math.IsInf(f, 0) {
		return -1
	}
	return f
}

func round(f float64) float64 { return math.Round(f*1e5) / 1e5 }

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
