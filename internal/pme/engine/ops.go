package engine

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"p0ssess/internal/budget"
	"p0ssess/internal/history"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/pme/score"
	"p0ssess/internal/rules"
	"p0ssess/internal/usage"
)

type Status struct {
	Budget      budget.Budget     `json:"budget"`
	Overrides   []string          `json:"overrides,omitempty"`
	Spend       history.Spend     `json:"spend"`
	DailyLimit  float64           `json:"daily_limit_usd"`
	MonthLeft   float64           `json:"month_left_usd"`
	NearBudget  bool              `json:"near_budget"`
	Plan        string            `json:"plan"`
	Lambda      score.Lambda      `json:"lambda"`
	Running     []usage.Job       `json:"running"`
	LeadModel   string            `json:"lead_model"`
	LiveModels  string            `json:"live_models"`
	RegistryAge string            `json:"registry_as_of"`
	Files       map[string]string `json:"files"`
	Scope       Scope             `json:"scope"`
	Notes       []string          `json:"notes,omitempty"`
}

func (e *Engine) Status(s Scope) (*Status, error) {
	now := e.Now()
	s = e.Resolve(s)
	cfg, err := e.Config()
	if err != nil {
		return nil, err
	}
	b, err := e.Budget()
	if err != nil {
		return nil, err
	}
	rs, err := e.Rules(s)
	if err != nil {
		return nil, err
	}
	d := rs.Decide("", "", now)
	b, notes := effectiveBudget(b, d, &rs)
	es, err := history.Read(e.Dirs.HistoryFile())
	if err != nil {
		return nil, fmt.Errorf("reading the ledger: %w", err)
	}
	sp := history.NewLedger(es).Spend(now)
	us := e.Usage()
	plan := b.Plan
	if plan == "auto" {
		plan = "api"
		if us.Subscriber {
			plan = "subscription"
		}
	}
	pr := us.Project(now, cfg.Lambda.EMin, time.Duration(cfg.Lambda.StaleMinutes)*time.Minute)
	daily := b.DailyLimit(sp.MonthBeforeToday, now)
	st := &Status{Budget: b, Overrides: notes, Spend: sp, DailyLimit: finite(daily), Notes: e.IgnoredNotes(s, rs),
		MonthLeft: max(0, b.MonthlyUSD-sp.Month-sp.Reserved), NearBudget: b.MonthlyUSD > 0 && sp.Month+sp.Reserved >= 0.8*b.MonthlyUSD,
		Plan: plan, Lambda: score.LambdaFor(plan, b.UsageCredits, b.SaveClaude, pr, cfg.Lambda),
		Running: usage.Running(e.Dirs.RunningDir(), now), LeadModel: us.LeadModel, RegistryAge: e.Reg.AsOf, Scope: s,
		Files: map[string]string{"budget": e.Dirs.BudgetFile(), "engine": e.Dirs.EngineFile(),
			"rules_global": e.Dirs.RulesFile(), "history": e.Dirs.HistoryFile(), "usage": e.Dirs.UsageFile()}}
	if f := e.RuleFiles(s); f.Project != "" {
		st.Files["rules_project"] = f.Project
	}
	if e.Reg.LiveAsOf != "" {
		st.LiveModels = fmt.Sprintf("%d models, fetched %s", e.Reg.LiveCount, e.Reg.LiveAsOf)
	} else {
		st.LiveModels = "not fetched; using the shipped snapshot"
	}
	return st, nil
}

type RuleView struct {
	rules.Rule
	Text    string     `json:"text"`
	Expired bool       `json:"expired,omitempty"`
	Ignored string     `json:"ignored,omitempty"`
	Used    *rules.Use `json:"used,omitempty"`
}

func (e *Engine) RulesList(s Scope, kind string) ([]RuleView, error) {
	s = e.Resolve(s)
	rs, err := e.Rules(s)
	now := e.Now()
	use := e.Ledger().ApprovalUse(rs.Rules, now)
	var out []RuleView
	for _, r := range rs.Rules {
		if kind != "" && r.Kind != kind {
			continue
		}
		v := RuleView{Rule: r, Text: r.String(), Expired: !r.Active(now)}
		if r.Kind == rules.KindApprove {
			u := use[r.ID]
			v.Used = &u
		}
		out = append(out, v)
	}
	for _, ig := range rs.Ignored {
		if kind == "" || ig.Rule.Kind == kind {
			out = append(out, RuleView{Rule: ig.Rule, Text: ig.Rule.String(), Ignored: ig.Why})
		}
	}
	order := map[string]int{rules.ScopeSession: 0, rules.ScopeProject: 1, rules.ScopeGlobal: 2}
	sort.SliceStable(out, func(i, j int) bool { return order[out[i].Scope] < order[out[j].Scope] })
	return out, err
}

// RuleAdd validates a rule and, unless dryRun, saves it. expiresIn > 0 sets
// an expiry that many minutes from now. Session rules default to 12 hours.
func (e *Engine) RuleAdd(s Scope, r rules.Rule, dryRun bool, expiresIn int) (RuleView, error) {
	s = e.Resolve(s)
	now := e.Now()
	// The id and created time are assigned here, never taken from the caller: a future created time
	// would keep an approval's per-day count and total from ever being used up.
	r.ID, r.Created = "", time.Time{}
	if r.Scope == "" {
		r.Scope = rules.ScopeGlobal
	}
	if expiresIn > 0 {
		t := now.Add(time.Duration(expiresIn) * time.Minute).UTC().Truncate(time.Second)
		r.Expires = &t
	} else if r.Scope == rules.ScopeSession && r.Expires == nil {
		t := now.Add(12 * time.Hour).UTC().Truncate(time.Second)
		r.Expires = &t
	}
	for _, m := range r.Models {
		if e.Reg.Get(m) == nil {
			return RuleView{}, fmt.Errorf("unknown model %q", m)
		}
	}
	for _, u := range r.UseCases {
		if e.Cat.Get(u) == nil {
			return RuleView{}, fmt.Errorf("unknown use case %q", u)
		}
	}
	if err := r.Validate(); err != nil {
		return RuleView{}, err
	}
	if r.Scope == rules.ScopeProject {
		if why := r.Loosens(); why != "" {
			return RuleView{}, fmt.Errorf("project rules can only tighten (%s); save it at session or global scope", why)
		}
	}
	f := e.RuleFiles(s)
	switch {
	case r.Scope == rules.ScopeProject && f.Project == "":
		return RuleView{}, errors.New("no project here (no .git or .p0ssess above the working directory)")
	case r.Scope == rules.ScopeSession && f.Session == "":
		return RuleView{}, errors.New("no session id known; pass session_id")
	}
	if dryRun {
		return RuleView{Rule: r, Text: r.String()}, nil
	}
	saved, err := rules.Add(f, r, now)
	if err != nil {
		return RuleView{}, err
	}
	return RuleView{Rule: saved, Text: saved.String()}, nil
}

func (e *Engine) RuleRemove(s Scope, id string) (RuleView, error) {
	r, err := rules.Remove(e.RuleFiles(e.Resolve(s)), id)
	if err != nil {
		return RuleView{}, err
	}
	return RuleView{Rule: r, Text: r.String()}, nil
}

type Outcome struct {
	OfferID    string  `json:"offer_id,omitempty"`
	RunID      string  `json:"run_id,omitempty"`
	Model      string  `json:"model"`
	Outcome    string  `json:"outcome"`
	CashUSD    float64 `json:"cash_usd,omitempty"`
	UseCase    string  `json:"use_case,omitempty"`
	Chosen     string  `json:"chosen,omitempty"`
	ApprovalID string  `json:"approval_id,omitempty"`
	Stakes     int     `json:"stakes,omitempty"`
	Reason     string  `json:"reason,omitempty"`
	Scope
}

// RecordOutcome appends an outcome, filling use case, kind, family, stakes
// and the covering standing approval from the offer when one is named.
func (e *Engine) RecordOutcome(o Outcome) (history.Entry, error) {
	o.Scope = e.Resolve(o.Scope)
	en := history.Entry{TS: e.Now().UTC(), Type: history.TypeOutcome, OfferID: o.OfferID, Model: o.Model, Outcome: o.Outcome,
		CashUSD: o.CashUSD, UseCase: o.UseCase, Chosen: o.Chosen, ApprovalID: o.ApprovalID, Stakes: o.Stakes, Reason: o.Reason,
		Session: o.Session, Project: paths.ProjectRoot(o.Cwd)}
	if o.OfferID != "" {
		off := e.Ledger().Offer(o.OfferID)
		if off == nil {
			return en, fmt.Errorf("unknown offer %q", o.OfferID)
		}
		if en.UseCase == "" {
			en.UseCase = off.UseCase
		}
		if en.Stakes == 0 {
			en.Stakes = off.Stakes
		}
		if en.ApprovalID == "" {
			for _, opt := range off.Options {
				if opt.Model == o.Model {
					en.ApprovalID = opt.ApprovalID
				}
			}
		}
	}
	if en.UseCase != "" {
		uc := e.Cat.Get(en.UseCase)
		if uc == nil {
			return en, fmt.Errorf("unknown use case %q", en.UseCase)
		}
		en.Kind, en.Family = uc.Kind(), uc.Family
	}
	if o.CashUSD < 0 {
		return en, errors.New("cash_usd must be 0 or more")
	}
	// The cash on an outcome is the measured run's. Without a run to point
	// at, reported cash becomes a run of its own so it is counted once.
	if en.Outcome == history.Accepted || en.Outcome == history.Redo {
		l := e.Ledger()
		if o.RunID != "" {
			cost, ok := l.RunCost(o.RunID)
			if !ok {
				return en, fmt.Errorf("unknown run %q", o.RunID)
			}
			en.RunID, en.CashUSD = o.RunID, cost
		} else if r, ok := l.MatchRun(o.OfferID, o.Model, e.Now().Add(-RunMatchWindow)); ok {
			en.RunID, en.CashUSD = r.RunID, r.CashUSD
		} else if o.CashUSD > 0 {
			en.RunID = "r-" + history.NewOfferID()[2:]
			run := history.Entry{TS: en.TS, Type: history.TypeRun, RunID: en.RunID, Route: "reported", Model: o.Model,
				CashUSD: o.CashUSD, OfferID: o.OfferID, UseCase: en.UseCase, Session: en.Session, Project: en.Project,
				ApprovalID: en.ApprovalID}
			if err := history.Append(e.Dirs.HistoryFile(), run); err != nil {
				return en, err
			}
		}
	}
	return en, history.Append(e.Dirs.HistoryFile(), en)
}

// RunMatchWindow is how far back an outcome without a run id looks for its run.
const RunMatchWindow = 3 * time.Hour

// Run is one finished delegated call, measured by the relay or the direct path.
type Run struct {
	ID          string
	Route       string // "agent" or "direct"
	Model       string
	CashUSD     float64
	UpstreamUSD float64
	Status      string
	OfferID     string
	UseCase     string
	Cwd         string
	Session     string
	ApprovalID  string
	Reserve     string
	// Estimated marks a cost that was not measured; CashUSD is then the reserved maximum.
	Estimated   bool
	Generations []string
}

// RecordRun adds a run to the ledger; spend is the sum of these.
func (e *Engine) RecordRun(r Run) error {
	if r.CashUSD < 0 || math.IsNaN(r.CashUSD) {
		r.CashUSD = 0
	}
	return history.Append(e.Dirs.HistoryFile(), history.Entry{TS: e.Now().UTC(), Type: history.TypeRun, RunID: r.ID,
		Route: r.Route, Model: r.Model, CashUSD: r.CashUSD, UpstreamUSD: r.UpstreamUSD, Status: r.Status,
		OfferID: r.OfferID, UseCase: r.UseCase, Session: r.Session, Project: paths.ProjectRoot(r.Cwd),
		ApprovalID: r.ApprovalID, Reserve: r.Reserve, Estimated: r.Estimated, Generations: r.Generations})
}

type Learned struct {
	Tendencies  []history.Tendency   `json:"tendencies"`
	Suggestions []history.Suggestion `json:"suggestions"`
	Recent      []history.Entry      `json:"recent"`
}

func (e *Engine) Learned(s Scope) Learned {
	s = e.Resolve(s)
	l := e.Ledger()
	rs, _ := e.Rules(s)
	b, _ := e.Budget()
	return Learned{Tendencies: l.Tendencies(), Suggestions: l.Suggest(rs.Active(e.Now()), b.PerTaskUSD), Recent: l.Recent(20)}
}

// Settings that can be changed in plain language through Claude or the page.
var settingKeys = []string{"monthly_usd", "per_task_usd", "daily_usd", "save_claude", "plan", "usage_credits",
	"privacy.mode", "privacy.allow_safety_retention", "models.allow", "models.deny", "providers.allow", "providers.deny",
	"sandbox.shell_internet", "sandbox.shell_internet.add", "sandbox.shell_internet.remove",
	"lambda.preset", "statusline.command", "statusline.mark", "statusline.width"}

func SettingKeys() []string { return append([]string(nil), settingKeys...) }

// SetSetting changes one setting in budget.toml or engine.toml. The file is read and rewritten under
// a lock, so concurrent changes to different settings are all kept.
func (e *Engine) SetSetting(key, value string) (msg string, err error) {
	path := e.Dirs.BudgetFile()
	set := e.setBudget
	if strings.HasPrefix(key, "lambda.") || strings.HasPrefix(key, "statusline.") {
		path, set = e.Dirs.EngineFile(), e.setEngine
	}
	err = paths.WithLock(path, func() error {
		msg, err = set(key, value)
		return err
	})
	return msg, err
}

// projectPath canonicalizes a project directory for the shell internet list.
func projectPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(h, p[2:])
		}
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("%q is not an absolute path", p)
	}
	c, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	if st, err := os.Stat(c); err != nil || !st.IsDir() {
		return "", fmt.Errorf("%s is not a directory", c)
	}
	if c == "/" {
		return "", errors.New("the whole filesystem cannot be one project")
	}
	return c, nil
}

func (e *Engine) setBudget(key, value string) (string, error) {
	b, err := e.Budget()
	if err != nil {
		return "", err
	}
	num := func() (float64, error) {
		n, err := strconv.ParseFloat(strings.TrimPrefix(value, "$"), 64)
		if err == nil && (math.IsNaN(n) || math.IsInf(n, 0)) {
			return 0, errors.New("must be a finite number")
		}
		return n, err
	}
	flag := func() (bool, error) { return strconv.ParseBool(value) }
	list := func() []string {
		var out []string
		for _, s := range strings.Split(value, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	switch key {
	case "monthly_usd":
		b.MonthlyUSD, err = num()
	case "per_task_usd":
		b.PerTaskUSD, err = num()
	case "daily_usd":
		b.DailyUSD, err = num()
	case "save_claude":
		b.SaveClaude, err = flag()
	case "usage_credits":
		b.UsageCredits, err = flag()
	case "plan":
		b.Plan = value
	case "privacy.mode":
		b.Privacy.Mode = value
	case "privacy.allow_safety_retention":
		b.Privacy.AllowSafetyRetention, err = flag()
	case "models.allow":
		b.Models.Allow = list()
	case "models.deny":
		b.Models.Deny = list()
	case "providers.allow":
		b.Providers.Allow = list()
	case "providers.deny":
		b.Providers.Deny = list()
	case "sandbox.shell_internet":
		var ps []string
		for _, p := range list() {
			c, perr := projectPath(p)
			if perr != nil {
				return "", fmt.Errorf("%s: %w", key, perr)
			}
			ps = append(ps, c)
		}
		b.Sandbox.ShellInternet = ps
	case "sandbox.shell_internet.add":
		c, perr := projectPath(value)
		if perr != nil {
			return "", fmt.Errorf("%s: %w", key, perr)
		}
		if !slices.Contains(b.Sandbox.ShellInternet, c) {
			b.Sandbox.ShellInternet = append(b.Sandbox.ShellInternet, c)
		}
		value = c
	case "sandbox.shell_internet.remove":
		c, perr := projectPath(value)
		if perr != nil {
			c = value
		}
		b.Sandbox.ShellInternet = slices.DeleteFunc(b.Sandbox.ShellInternet, func(p string) bool { return p == c || p == value })
		value = c
	default:
		return "", fmt.Errorf("unknown setting %q (one of %s)", key, strings.Join(settingKeys, ", "))
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w", key, err)
	}
	if err := budget.Save(e.Dirs.BudgetFile(), b); err != nil {
		return "", err
	}
	if strings.HasPrefix(key, "sandbox.shell_internet") {
		on := "none"
		if len(b.Sandbox.ShellInternet) > 0 {
			on = strings.Join(b.Sandbox.ShellInternet, ", ")
		}
		return fmt.Sprintf("%s %s (saved to %s). Shell internet is now on for: %s. New sandbox sessions pick it up; "+
			"shell commands reach public addresses on ports 80 and 443 only, never the local network or the host.",
			key, value, e.Dirs.BudgetFile(), on), nil
	}
	return fmt.Sprintf("%s = %s (saved to %s)", key, value, e.Dirs.BudgetFile()), nil
}

// setEngine edits engine.toml as a generic table so unknown sections survive.
func (e *Engine) setEngine(key, value string) (string, error) {
	path := e.Dirs.EngineFile()
	doc := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		if _, err := toml.Decode(string(b), &doc); err != nil {
			return "", err
		}
	}
	section, field, _ := strings.Cut(key, ".")
	tab, _ := doc[section].(map[string]any)
	if tab == nil {
		tab = map[string]any{}
	}
	switch key {
	case "lambda.preset":
		if _, ok := score.Presets[value]; !ok && value != "custom" {
			return "", fmt.Errorf("lambda.preset must be list, gentle, balanced, eager or custom")
		}
		tab[field] = value
	case "statusline.width":
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return "", errors.New("statusline.width must be 0 or more")
		}
		tab[field] = n
	case "statusline.command", "statusline.mark":
		tab[field] = value
	default:
		return "", fmt.Errorf("unknown setting %q", key)
	}
	doc[section] = tab
	var buf strings.Builder
	buf.WriteString("# p0ssess engine presets. See docs/ENGINE.md.\n\n")
	if err := toml.NewEncoder(&buf).Encode(doc); err != nil {
		return "", err
	}
	if _, err := score.LoadConfigFrom([]byte(buf.String())); err != nil {
		return "", err
	}
	if err := paths.WriteFileAtomic(path, []byte(buf.String()), 0o600); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s = %s (saved to %s)", key, value, path), nil
}
