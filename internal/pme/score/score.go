package score

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"p0ssess/internal/budget"
	"p0ssess/internal/history"
	"p0ssess/internal/pme/bench"
	"p0ssess/internal/pme/catalog"
	"p0ssess/internal/pme/registry"
	"p0ssess/internal/rules"
)

// Routes.
const (
	RouteClaude   = "claude"          // the lead model does it in the session
	RouteSubagent = "claude-subagent" // a native Claude subagent on a smaller model
	RouteDirect   = "direct"          // one OpenRouter call
	RouteAgent    = "agent"           // a sandboxed OpenCode session
	RouteLocal    = "local"           // a local tool, no model
	RouteEngine   = "engine"          // an OpenRouter PDF engine, no chat model
)

type Input struct {
	UseCase    *catalog.UseCase
	Work       catalog.Work
	Inputs     []string // task input modalities; defaults to the use case's
	Outputs    []string
	Stakes     int
	NeedsTools bool
	Lead       *registry.Model
	Subagent   *registry.Model
	Models     []*registry.Model // OpenRouter candidates
	Bench      *bench.Set
	Lambda     float64
	Cfg        Config
	Budget     budget.Budget
	Rules      rules.Decision
	History    *history.Ledger
	Spend      history.Spend
	DailyLimit float64
	Private    bool
}

type Breakdown struct {
	Cash     float64 `json:"cash"`
	Claude   float64 `json:"claude"`
	Overhead float64 `json:"overhead"`
	Review   float64 `json:"review"`
	Risk     float64 `json:"risk"`
	Friction float64 `json:"friction,omitempty"`
}

type Option struct {
	Rank            int       `json:"rank"`
	Route           string    `json:"route"`
	Model           string    `json:"model"`
	Total           float64   `json:"total_usd"`
	Breakdown       Breakdown `json:"breakdown"`
	PFail           float64   `json:"p_fail"`
	PFailUpper      float64   `json:"p_fail_upper"`
	Evidence        string    `json:"evidence"`
	History         string    `json:"history,omitempty"`
	CashMax         float64   `json:"cash_max_usd"`
	ClaudeListUSD   float64   `json:"claude_list_usd"`
	NeedsApproval   bool      `json:"needs_approval"`
	ApprovalReasons []string  `json:"approval_reasons,omitempty"`
	ApprovalID      string    `json:"standing_approval,omitempty"`
	// MaxCostUSD is the spend ceiling already authorized for this option: the standing approval's
	// per-run limit, or the per-task limit when no approval is needed. Pass it as the direct tool's
	// max_cost_usd. Zero when the user must approve first; then pass the amount they approve.
	MaxCostUSD  float64  `json:"max_cost_usd,omitempty"`
	Review      string   `json:"review"`
	Reason      string   `json:"reason"`
	Notes       []string `json:"notes,omitempty"`
	PriceSource string   `json:"price_source,omitempty"`
	MixNext     bool     `json:"mix_next,omitempty"`
}

type Exclusion struct {
	Model string `json:"model"`
	Route string `json:"route,omitempty"`
	Why   string `json:"why"`
}

type Result struct {
	Options  []Option    `json:"options"`
	Excluded []Exclusion `json:"excluded,omitempty"`
	// ClaudeFeasible is false when the lead model cannot do the job at all.
	ClaudeFeasible bool    `json:"claude_feasible"`
	ClaudeWhy      string  `json:"claude_infeasible_reason,omitempty"`
	Ceiling        float64 `json:"ceiling"`
	ReviewLevel    string  `json:"review_level"`
}

type ctx struct {
	in         Input
	cfg        Config
	uc         *catalog.UseCase
	w          catalog.Work
	ins, outs  []string
	stakes     int
	review     string
	retries    int
	harmEff    float64
	leadScale  float64
	lDirect    float64 // Claude list dollars for the lead to do the work
	feasible   bool
	infeasible string
	zdr        bool
}

func Score(in Input) Result {
	c := &ctx{in: in, cfg: in.Cfg, uc: in.UseCase, w: in.Work, stakes: in.Stakes}
	if c.stakes < 1 || c.stakes > 4 {
		c.stakes = 2
	}
	c.ins = in.Inputs
	if len(c.ins) == 0 {
		c.ins = c.uc.In
	}
	c.outs = in.Outputs
	if len(c.outs) == 0 {
		c.outs = c.uc.Out
	}
	if c.w.Items < 1 {
		c.w.Items = 1
	}
	c.review = c.uc.Verify.Level
	if c.uc.EditsCode() {
		c.review = "full"
	}
	if c.review == "mechanical" {
		c.retries = c.cfg.MechanicalRetries
	}
	c.harmEff = c.cfg.Escape.At(c.review) * c.cfg.Harm.At(c.stakes)
	c.zdr = in.Budget.Privacy.Mode == "zdr"
	sonnetOut := 10.0
	if lp, ok := in.Lead.Prices(false, false); ok && lp.Out > 0 {
		c.leadScale = lp.Out / sonnetOut
	} else {
		c.leadScale = 1
	}
	c.feasible, c.infeasible = c.fits(in.Lead, true)
	if c.feasible {
		c.lDirect = c.claudeWork(in.Lead, c.cfg.Lead)
	}

	res := Result{ClaudeFeasible: c.feasible, ClaudeWhy: c.infeasible, Ceiling: c.cfg.Ceilings.At(c.stakes), ReviewLevel: c.review}
	var opts []Option
	exclude := func(model, route, why string) {
		res.Excluded = append(res.Excluded, Exclusion{Model: model, Route: route, Why: why})
	}

	if c.feasible {
		opts = append(opts, c.claudeOption())
	} else {
		exclude(in.Lead.ID, RouteClaude, c.infeasible)
	}

	d := in.Rules
	delegationOff := ""
	switch {
	case in.Private:
		delegationOff = "this repository has a .p0ssess-private file"
	case d.ForbidDelegation != nil:
		delegationOff = "rule: " + d.ForbidDelegation.String()
	}

	// Local tools and PDF engines are not delegation to another model.
	if c.uc.Path == "local_tool" && c.uc.LocalTool != "" {
		opts = append(opts, c.localOption())
	}
	if c.uc.Engine != "" && delegationOff == "" {
		if o, ok := c.engineOption(); ok {
			opts = append(opts, o)
		}
	}

	if delegationOff != "" {
		exclude("*", "", "no delegation: "+delegationOff)
	} else {
		if in.Subagent != nil && in.Subagent.ID != in.Lead.ID {
			if d.ForbidClaudeAgents != nil {
				exclude(in.Subagent.ID, RouteSubagent, "rule: "+d.ForbidClaudeAgents.String())
			} else if o, why := c.subagentOption(in.Subagent); why != "" {
				exclude(in.Subagent.ID, RouteSubagent, why)
			} else {
				opts = append(opts, o)
			}
		}
		for _, m := range in.Models {
			o, why := c.delegatedOption(m)
			if why != "" {
				exclude(m.ID, "", why)
				continue
			}
			opts = append(opts, o)
		}
	}

	c.applyMix(opts)
	sort.SliceStable(opts, func(i, j int) bool {
		if math.Abs(opts[i].Total-opts[j].Total) > 1e-12 {
			return opts[i].Total < opts[j].Total
		}
		return opts[i].PFail < opts[j].PFail
	})
	for i := range opts {
		opts[i].Rank = i + 1
		opts[i].Reason = c.reason(opts[i], i == 0, opts)
	}
	res.Options = opts
	res.Excluded = c.trimExcluded(res.Excluded)
	return res
}

// fits applies the modality, context and tool filters.
func (c *ctx) fits(m *registry.Model, isLead bool) (bool, string) {
	for _, mod := range c.ins {
		mod = catalog.NormalizeModality(mod)
		if mod == "text" || mod == "" {
			continue
		}
		if !m.HasInput(mod) {
			return false, "does not take " + mod + " input"
		}
	}
	for _, mod := range c.outs {
		mod = catalog.NormalizeModality(mod)
		switch mod {
		case "", "text":
			if !m.HasOutput("text") {
				return false, "does not return text"
			}
		case "transcript":
			if !m.HasOutput("transcription") && !(m.HasOutput("text") && m.HasInput("audio")) {
				return false, "cannot transcribe"
			}
		case "speech":
			if !m.HasOutput("speech") && !m.HasOutput("audio") {
				return false, "does not produce speech"
			}
		default:
			if !m.HasOutput(mod) {
				return false, "does not produce " + mod
			}
		}
	}
	prompt := c.promptTokens()
	if ctxLen := m.ContextLen(); ctxLen > 0 && prompt+c.w.OutTokens > ctxLen {
		return false, fmt.Sprintf("needs %dk tokens of context, has %dk", (prompt+c.w.OutTokens)/1000, ctxLen/1000)
	}
	if m.MaxOutput != nil && *m.MaxOutput > 0 && c.w.OutTokens > *m.MaxOutput && m.Kind == "text" {
		return false, fmt.Sprintf("output limit %d tokens", *m.MaxOutput)
	}
	if !isLead && (c.agentPath() || c.in.NeedsTools) && !m.Caps.Tools {
		return false, "no tool use"
	}
	return true, ""
}

func (c *ctx) agentPath() bool { return c.w.Turns > 0 }

// promptTokens is the per-item prompt size including media read as tokens.
func (c *ctx) promptTokens() int {
	md := c.cfg.Media
	n := c.w.InTokens
	n += int(c.w.AudioSec * md.AudioTokensPerSec)
	n += int(c.w.VideoSec * md.VideoTokensPerSec)
	n += c.w.Images * md.ImageTokens
	n += c.w.Pages * md.PageTokens
	return n
}

// sessionCost prices an agent-style session of turns at the given prices.
func sessionCost(tp registry.TokenPrices, turns int, s SessionShape) float64 {
	per := float64(s.ContextTokens)*tp.CacheRead + float64(s.NewInTokens)*tp.In + float64(s.OutTokens)*tp.Out
	return float64(turns) * per / 1e6
}

// claudeWork is the Claude list-price cost of doing the work on model m.
func (c *ctx) claudeWork(m *registry.Model, shape SessionShape) float64 {
	tp, ok := m.Prices(false, false)
	if !ok {
		return 0
	}
	if c.agentPath() {
		return sessionCost(tp, c.w.Turns, shape)
	}
	prompt := c.promptTokens()
	tpl := m.ForPrompt(tp, prompt)
	per := (float64(prompt)*tpl.In + float64(c.w.OutTokens)*tpl.Out) / 1e6
	// One round re-reads the live session's context.
	return float64(c.w.Items)*per + float64(shape.ContextTokens)*tp.CacheRead/1e6
}

func (c *ctx) overheadL() float64 { return c.cfg.OverheadUSD * c.leadScale }

func (c *ctx) reviewL(level string) float64 {
	result := c.w.OutTokens * c.w.Items
	if c.agentPath() {
		result = c.cfg.AgentResultTokens
	}
	in := 2.0
	if lp, ok := c.in.Lead.Prices(false, false); ok {
		in = lp.In
	}
	return c.cfg.ReviewUSD.At(level)*c.leadScale + c.cfg.ReviewRead.At(level)*float64(result)*in/1e6
}

// pFail combines benchmark evidence, history and availability.
type pf struct {
	mean, upper, quality float64
	evidence, hist       string
	strength             float64
	declineP             float64
	disfavored           bool
}

func (c *ctx) pFail(m *registry.Model) pf {
	est := c.in.Bench.For(c.uc.Family, m.ID, c.uc.Proxy, c.uc.CatalogRank(m.ID))
	p, sig := est.PFail, est.Sigma
	out := pf{evidence: est.Evidence, strength: est.Strength}
	if c.in.History != nil {
		st := c.in.History.Stats(m.ID, c.uc.ID, c.uc.Family)
		succ := float64(st.UseCase.Accepted) + 0.5*float64(st.Family.Accepted-st.UseCase.Accepted)
		fail := float64(st.UseCase.Redo) + 0.5*float64(st.Family.Redo-st.UseCase.Redo)
		if n := succ + fail; n > 0 {
			n0 := est.Strength
			p = (p*n0 + fail) / (n0 + n)
			sig *= math.Sqrt(n0 / (n0 + n))
			out.hist = fmt.Sprintf("%d accepted, %d redone here", st.UseCase.Accepted, st.UseCase.Redo)
			if st.UseCase.Runs() == 0 {
				out.hist = fmt.Sprintf("%d accepted, %d redone on similar work", st.Family.Accepted, st.Family.Redo)
			}
		}
		u := st.UseCase
		if offers := u.Offers(); offers > 0 {
			no := float64(u.Declined + u.Overridden)
			out.declineP = (no + 1) / (float64(offers) + 4)
			out.disfavored = offers >= 3 && no/float64(offers) >= 0.6
		}
	}
	out.quality = p
	if up := m.Uptime(); up >= 0 && !m.IsClaude() {
		unavail := 1 - up
		if !m.SingleProvider() {
			unavail *= 0.5 // OpenRouter falls back across providers
		}
		p = 1 - (1-p)*(1-unavail)
	}
	out.mean = clamp(p)
	out.upper = clamp(p + sig)
	return out
}

// withRetries applies mechanical-check retries: a failed check re-runs the
// job, so the miss rate is p^(r+1) and expected runs are 1 + p + … + p^r.
func (c *ctx) withRetries(p float64) (eff, runs float64) {
	runs, pk := 0, 1.0
	for i := 0; i <= c.retries; i++ {
		runs += pk
		pk *= p
	}
	return pk, runs
}

func (c *ctx) claudeOption() Option {
	m := c.in.Lead
	f := c.pFail(m)
	eff, runs := c.withRetries(f.mean)
	up, _ := c.withRetries(f.upper)
	lam := c.in.Lambda
	b := Breakdown{Claude: lam * c.lDirect * runs}
	b.Risk = eff * (lam*c.lDirect + c.harmEff)
	o := Option{Route: RouteClaude, Model: m.ID, Breakdown: b, Total: b.Claude + b.Risk,
		PFail: eff, PFailUpper: up, Evidence: f.evidence, History: f.hist, ClaudeListUSD: c.lDirect * runs, Review: "Claude's own work"}
	if up > c.cfg.Ceilings.At(c.stakes) {
		o.Notes = append(o.Notes, fmt.Sprintf("above the stakes-%d ceiling; Claude is the fallback", c.stakes))
	}
	return o
}

func (c *ctx) localOption() Option {
	lam := c.in.Lambda
	p := bench.Families["local"].Prior
	eff, runs := c.withRetries(p)
	l := c.cfg.LocalToolUSD * c.leadScale * runs
	b := Breakdown{Claude: lam * l, Review: lam * c.reviewL(c.review)}
	redo := c.lDirect
	if !c.feasible {
		redo = 0
	}
	b.Risk = eff * (lam*redo + c.harmEff)
	return Option{Route: RouteLocal, Model: "local:" + c.uc.LocalTool, Breakdown: b, Total: b.Claude + b.Review + b.Risk,
		PFail: eff, PFailUpper: math.Min(1, eff+0.1), Evidence: "local tool; no model", ClaudeListUSD: l, Review: c.review}
}

func (c *ctx) engineOption() (Option, bool) {
	est := c.uc.EstCost
	if est == nil || (est.Min == nil && est.Max == nil) {
		return Option{}, false
	}
	cash := mid(est) * (1 + c.cfg.CreditFee)
	lam := c.in.Lambda
	p := bench.Families["document"].FBest
	eff, runs := c.withRetries(p)
	b := Breakdown{Cash: cash * runs, Overhead: lam * c.overheadL(), Review: lam * c.reviewL(c.review)}
	b.Risk = eff * (lam*c.lDirect + c.harmEff)
	o := Option{Route: RouteEngine, Model: "engine:" + c.uc.Engine, Breakdown: b, Total: b.Cash + b.Overhead + b.Review + b.Risk,
		PFail: eff, PFailUpper: math.Min(1, eff+0.15), Evidence: "OpenRouter PDF engine; catalog estimate",
		CashMax: cash * c.cfg.CashHeadroom * runs, Review: c.review, PriceSource: "catalog estimate"}
	c.approval(&o, false)
	return o, true
}

func (c *ctx) subagentOption(m *registry.Model) (Option, string) {
	if ok, why := c.fits(m, false); !ok {
		return Option{}, why
	}
	f := c.pFail(m)
	eff, runs := c.withRetries(f.mean)
	up, _ := c.withRetries(f.upper)
	// A Claude subagent is Claude, so stakes 4 does not bar it; the ceiling
	// for the level still applies.
	if why := c.gate(m, up, true); why != "" {
		return Option{}, why
	}
	lam := c.in.Lambda
	work := c.claudeWork(m, c.cfg.Agent) * runs
	ov, rv := c.overheadL(), c.reviewL(c.review)
	if c.feasible && (1-eff)*c.lDirect <= ov+rv+work {
		return Option{}, fmt.Sprintf("cannot save: hand-off, review and subagent work ($%.3f) exceed the expected work saved ($%.3f)", ov+rv+work, (1-eff)*c.lDirect)
	}
	redo := c.lDirect
	if !c.feasible {
		redo = work + ov
	}
	b := Breakdown{Claude: lam * work, Overhead: lam * ov, Review: lam * rv, Risk: eff * (lam*redo + c.harmEff)}
	return Option{Route: RouteSubagent, Model: m.ID, Breakdown: b, Total: b.Claude + b.Overhead + b.Review + b.Risk,
		PFail: eff, PFailUpper: up, Evidence: f.evidence, History: f.hist, ClaudeListUSD: work + ov + rv, Review: c.review}, ""
}

// gate applies the stakes ceiling and the stakes-4 rule.
func (c *ctx) gate(m *registry.Model, upper float64, claude bool) string {
	allowed := c.in.Rules.MaxStakesFor(m.ID)
	if c.stakes >= 4 && !claude && allowed < 4 {
		return "stakes 4 stays on Claude unless a rule allows this model"
	}
	if ceil := c.cfg.Ceilings.At(c.stakes); upper > ceil && allowed < c.stakes {
		return fmt.Sprintf("p_fail up to %.2f is above the stakes-%d ceiling %.2f", upper, c.stakes, ceil)
	}
	return ""
}

func (c *ctx) delegatedOption(m *registry.Model) (Option, string) {
	d := c.in.Rules
	if r := d.Forbidden[m.ID]; r != nil {
		return Option{}, "rule: " + r.String()
	}
	if d.Only != nil && !contains(d.Only.Models, m.ID) {
		return Option{}, "rule: " + d.Only.String()
	}
	var providers []string
	if m.Endpoints != nil {
		providers = m.Endpoints.Providers
	}
	if ok, why := c.in.Budget.ModelAllowed(m.ID, providers); !ok {
		return Option{}, why
	}
	if why := c.privacy(m); why != "" {
		return Option{}, why
	}
	route := RouteDirect
	if c.agentPath() {
		route = RouteAgent
		if d.ForbidOpenCode != nil {
			return Option{}, "rule: " + d.ForbidOpenCode.String()
		}
	}
	if ok, why := c.fits(m, false); !ok {
		return Option{}, why
	}
	cash, cashMax, src, notes, ok := c.cash(m)
	if !ok {
		return Option{}, "no usable price"
	}
	f := c.pFail(m)
	eff, runs := c.withRetries(f.mean)
	up, _ := c.withRetries(f.upper)
	if why := c.gate(m, up, false); why != "" {
		return Option{}, why
	}
	lam := c.in.Lambda
	ov, rv := c.overheadL(), c.reviewL(c.review)
	if c.feasible && (1-eff)*c.lDirect <= ov+rv {
		return Option{}, fmt.Sprintf("cannot save: hand-off and review ($%.3f of Claude) exceed the expected work saved ($%.3f)", ov+rv, (1-eff)*c.lDirect)
	}
	if c.in.Budget.MonthlyUSD >= 0 && c.in.Spend.Month+cashMax*runs > c.in.Budget.MonthlyUSD+1e-9 {
		return Option{}, fmt.Sprintf("would pass the $%.2f monthly cap", c.in.Budget.MonthlyUSD)
	}
	redo := lam * c.lDirect
	if !c.feasible {
		redo = cash + lam*ov // nothing else can do it: the redo is another run
	}
	b := Breakdown{Cash: cash * runs, Overhead: lam * ov, Review: lam * rv, Risk: eff * (redo + c.harmEff)}
	if f.declineP > 0 {
		b.Friction = f.declineP * lam * ov
	}
	o := Option{Route: route, Model: m.ID, Breakdown: b,
		Total: b.Cash + b.Overhead + b.Review + b.Risk + b.Friction,
		PFail: eff, PFailUpper: up, Evidence: f.evidence, History: f.hist, CashMax: cashMax * runs,
		ClaudeListUSD: ov + rv, Review: c.review, PriceSource: src, Notes: notes}
	if f.disfavored {
		o.Notes = append(o.Notes, "you usually decline this route here")
	}
	if m.Live {
		o.Notes = append(o.Notes, "known only from the live model list: no uptime or data-policy facts")
	}
	if m.SingleProvider() {
		o.Notes = append(o.Notes, "single provider")
	}
	if c.stakes >= 4 {
		o.Notes = append(o.Notes, "stakes 4 allowed by your rule; Claude reviews all of it")
		o.Review = "full"
	}
	c.approval(&o, true)
	return o, ""
}

func (c *ctx) privacy(m *registry.Model) string {
	if !c.zdr {
		return ""
	}
	if m.Batch() {
		return "batch endpoints are never zero-retention"
	}
	if m.Endpoints == nil {
		return "zero-retention mode: no endpoint data for this model"
	}
	if m.Endpoints.HasZDR {
		return ""
	}
	if c.in.Budget.Privacy.AllowSafetyRetention && m.Policy() == registry.PolicySafetyRetention {
		return ""
	}
	return "zero-retention mode: no zero-retention endpoint"
}

// cash prices one run at the expected endpoint and at list price with
// headroom for the approval check, both with OpenRouter's credit fee.
func (c *ctx) cash(m *registry.Model) (exp, max float64, src string, notes []string, ok bool) {
	fee := 1 + c.cfg.CreditFee
	w := c.w
	p := m.Pricing
	items := float64(w.Items)
	switch {
	case hasAny(c.outs, "image") && !hasAny(c.outs, "video"):
		per, s := c.mediaFallback(m, func(x *registry.Model) *float64 { return x.Pricing.ImageOutRaw })
		if per <= 0 {
			return 0, 0, "", nil, false
		}
		n := math.Max(1, float64(w.Images))
		return per * n * fee, per * n * fee * c.cfg.CashHeadroom, s, []string{"image price unit unconfirmed; scaled from the catalog estimate"}, true
	case hasAny(c.outs, "video"):
		if p.USDPerSecondMin == nil || w.GenSec <= 0 {
			return 0, 0, "", nil, false
		}
		hi := *p.USDPerSecondMin
		if p.USDPerSecondMax != nil {
			hi = *p.USDPerSecondMax
		}
		return *p.USDPerSecondMin * w.GenSec * fee, hi * w.GenSec * fee, "per-second video price (lowest tier)", nil, true
	case m.HasOutput("transcription"):
		if p.PromptRaw == nil || w.AudioSec <= 0 {
			return 0, 0, "", nil, false
		}
		v := *p.PromptRaw * w.AudioSec * fee
		return v, v * c.cfg.CashHeadroom, "raw price read as per second", []string{"speech-to-text unit assumed per second (unconfirmed)"}, true
	case m.HasOutput("speech"):
		if p.PromptRaw == nil || w.Chars <= 0 {
			return 0, 0, "", nil, false
		}
		v := *p.PromptRaw * float64(w.Chars) * fee
		return v, v * c.cfg.CashHeadroom, "raw price read as per character", []string{"text-to-speech unit assumed per character (unconfirmed)"}, true
	case hasAny(c.outs, "audio") && !m.HasOutput("text") || m.Kind == "audio" && hasAny(c.outs, "audio"):
		per, s := c.mediaFallback(m, nil)
		if per <= 0 {
			return 0, 0, "", nil, false
		}
		n := math.Max(1, w.GenSec/30)
		return per * n * fee, per * n * fee * c.cfg.CashHeadroom, s, []string{"music price comes from description text, not price fields"}, true
	}
	tp, okp := m.Prices(c.zdr, true)
	list, okl := m.Prices(c.zdr, false)
	if !okp || !okl {
		return 0, 0, "", nil, false
	}
	if m.HasOutput("embeddings") || m.HasOutput("rerank") {
		if tp.In <= 0 {
			return 0, 0, "", nil, false
		}
		v := float64(w.InTokens) * tp.In / 1e6 * items * fee
		vm := float64(w.InTokens) * list.In / 1e6 * items * fee * c.cfg.CashHeadroom
		return v, vm, tp.Source, nil, true
	}
	var e, l float64
	if c.agentPath() {
		e = sessionCost(tp, w.Turns, c.cfg.Agent)
		l = sessionCost(list, w.Turns, c.cfg.Agent)
	} else {
		prompt := c.promptTokens()
		out := float64(w.OutTokens)
		if m.Caps.ReasoningRequired != nil && *m.Caps.ReasoningRequired {
			out *= 1 + c.cfg.ReasoningFactor
			notes = append(notes, "reasoning cannot be turned off; billed as output")
		}
		tpl, ll := m.ForPrompt(tp, prompt), m.ForPrompt(list, prompt)
		e = (float64(prompt)*tpl.In + out*tpl.Out) / 1e6 * items
		l = (float64(prompt)*ll.In + out*ll.Out) / 1e6 * items
		if c.uc.Family == "search" && p.WebSearchUSD != nil {
			e += *p.WebSearchUSD * items
			l += *p.WebSearchUSD * items
		}
		tp = tpl
	}
	if e <= 0 && m.Kind != "text" {
		return 0, 0, "", nil, false
	}
	return e * fee, l * fee * c.cfg.CashHeadroom, tp.Source, notes, true
}

// mediaFallback prices a media model from the catalog's per-run estimate,
// scaled by the raw price field relative to the recommended model's.
func (c *ctx) mediaFallback(m *registry.Model, raw func(*registry.Model) *float64) (float64, string) {
	est := c.uc.EstCost
	if est == nil || (est.Min == nil && est.Max == nil) {
		return 0, ""
	}
	base := mid(est)
	if raw == nil || c.uc.Recommended == nil || *c.uc.Recommended == m.ID {
		return base, "catalog estimate"
	}
	var rec *registry.Model
	for _, x := range c.in.Models {
		if x.ID == *c.uc.Recommended {
			rec = x
		}
	}
	if rec == nil || raw(rec) == nil || raw(m) == nil || *raw(rec) <= 0 {
		return base, "catalog estimate"
	}
	return base * *raw(m) / *raw(rec), "catalog estimate scaled by raw price"
}

func (c *ctx) approval(o *Option, firstUse bool) {
	if o.Route == RouteClaude || o.Route == RouteSubagent || o.Route == RouteLocal {
		return
	}
	d := c.in.Rules
	limit := c.in.Budget.PerTaskUSD
	var reasons []string
	if c.in.DailyLimit >= 0 && c.in.Spend.Today >= c.in.DailyLimit && o.CashMax > 0 {
		limit = 0
		reasons = append(reasons, fmt.Sprintf("daily soft limit $%.2f reached", c.in.DailyLimit))
	}
	if o.CashMax > limit+1e-12 && limit > 0 {
		reasons = append(reasons, fmt.Sprintf("up to $%.3f, above the $%.2f per-task limit", o.CashMax, limit))
	} else if o.CashMax > 0 && limit == 0 && len(reasons) == 0 {
		reasons = append(reasons, "per-task auto-approve is $0")
	}
	if c.uc.EditsCode() {
		reasons = append(reasons, "edits your code")
	}
	if firstUse && c.in.History != nil && !c.in.History.UsedModels()[o.Model] && d.Allowed[o.Model] == nil {
		reasons = append(reasons, "first use of this model")
	}
	if len(reasons) == 0 {
		return
	}
	use := map[string]rules.Use{}
	if c.in.History != nil {
		use = c.in.History.ApprovalUse(d.Approvals, nowOf(c.in))
	}
	if r, why := d.Approval(o.Model, o.CashMax, use); r != nil {
		o.ApprovalID = r.ID
		o.Notes = append(o.Notes, "covered by standing approval: "+r.String())
		return
	} else if why != "" {
		reasons = append(reasons, why)
	}
	o.NeedsApproval = true
	o.ApprovalReasons = reasons
}

// applyMix marks the mix member furthest below its share as next.
func (c *ctx) applyMix(opts []Option) {
	r := c.in.Rules.Only
	if r == nil || r.Kind != rules.KindMix || c.in.History == nil {
		return
	}
	weights := r.Weights
	if len(weights) != len(r.Models) {
		weights = make([]float64, len(r.Models))
		for i := range weights {
			weights[i] = 1
		}
	}
	sum, runs := 0.0, map[string]float64{}
	total := 0.0
	for i, m := range r.Models {
		sum += weights[i]
		st := c.in.History.Stats(m, c.uc.ID, "")
		runs[m] = float64(st.UseCase.Runs())
		total += runs[m]
	}
	best, bestGap := -1, math.Inf(-1)
	for i := range opts {
		idx := indexOf(r.Models, opts[i].Model)
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

func (c *ctx) reason(o Option, top bool, all []Option) string {
	fp := fmt.Sprintf("p_fail %.2f", o.PFail)
	switch o.Route {
	case RouteClaude:
		if top {
			if len(all) == 1 {
				return fmt.Sprintf("keep on Claude: nothing else fits or saves (%s)", fp)
			}
			return fmt.Sprintf("keep on Claude: lowest cost per accepted result ($%.3f of Claude at list $%.3f; %s)", o.Total, o.ClaudeListUSD, fp)
		}
		return fmt.Sprintf("Claude itself: $%.3f per accepted result (%s)", o.Total, fp)
	case RouteLocal:
		return fmt.Sprintf("run %s locally: no model cost (%s)", strings.TrimPrefix(o.Model, "local:"), fp)
	}
	lead := "option"
	if top {
		lead = "best"
	}
	what := fmt.Sprintf("$%.3f cash", o.Breakdown.Cash)
	if o.Route == RouteSubagent {
		what = fmt.Sprintf("$%.3f of Claude subagent usage", o.Breakdown.Claude)
	}
	ev := o.Evidence
	if len(ev) > 80 {
		ev = ev[:77] + "..."
	}
	s := fmt.Sprintf("%s: %s via %s, %s, %s; %s (%s)", lead, short(o.Model), o.Route, what, reviewWords[o.Review], fp, ev)
	if !c.feasible && top {
		s = "Claude cannot do this (" + c.infeasible + "); " + s
	}
	return s
}

// trimExcluded keeps the exclusions worth showing: catalog-named models and
// rule-named ones first, at most 12.
func (c *ctx) trimExcluded(ex []Exclusion) []Exclusion {
	named := map[string]bool{}
	for _, m := range c.uc.Candidates() {
		named[m] = true
	}
	sort.SliceStable(ex, func(i, j int) bool {
		return named[ex[i].Model] && !named[ex[j].Model]
	})
	if len(ex) > 12 {
		ex = ex[:12]
	}
	return ex
}

var reviewWords = map[string]string{
	"none":       "passed through with its label",
	"spot":       "Claude spot-checks it",
	"mechanical": "a mechanical check decides",
	"full":       "Claude reviews all of it",
}

func nowOf(in Input) (t timeT) { return timeNow() }

func mid(est *catalog.Cost) float64 {
	switch {
	case est.Min != nil && est.Max != nil:
		return (*est.Min + *est.Max) / 2
	case est.Max != nil:
		return *est.Max
	case est.Min != nil:
		return *est.Min
	}
	return 0
}

func hasAny(list []string, v string) bool {
	for _, s := range list {
		if catalog.NormalizeModality(s) == v {
			return true
		}
	}
	return false
}

func contains(list []string, v string) bool { return indexOf(list, v) >= 0 }

func indexOf(list []string, v string) int {
	for i, s := range list {
		if s == v {
			return i
		}
	}
	return -1
}

func short(id string) string {
	if _, after, ok := strings.Cut(id, "/"); ok {
		return after
	}
	return id
}

func clamp(x float64) float64 { return math.Max(0, math.Min(1, x)) }
