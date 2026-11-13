// Package local is the client half of the Predictive Model Engine. It asks the engine service for
// routes (falling back to the signed route table), and keeps everything that spends or limits
// spending on this machine: budgets, rules, standing approvals, the ledger and the guard in front of
// every paid call.
package local

import (
	"errors"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"p0ssess/internal/budget"
	"p0ssess/internal/history"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/pme/remote"
	"p0ssess/internal/pme/table"
	"p0ssess/internal/rules"
	"p0ssess/internal/team"
	"p0ssess/internal/usage"
)

type Engine struct {
	Dirs   paths.Dirs
	Table  *table.Store
	Remote *remote.Client
	Now    func() time.Time

	// reported holds the ignored rules already reported, per session.
	reported   sync.Map
	refreshing atomic.Bool
}

// Load opens the route table (the cached one, else the one built in) and the engine client.
func Load(d paths.Dirs) (*Engine, error) {
	return &Engine{Dirs: d, Table: table.Open(d.Cache), Remote: remote.New(d), Now: time.Now}, nil
}

func (e *Engine) Config() (Config, error)        { return LoadConfig(e.Dirs.EngineFile()) }
func (e *Engine) Budget() (budget.Budget, error) { return budget.Load(e.Dirs.BudgetFile()) }

// OutcomesEnabled reports whether anonymous outcome records may be queued and sent (outcomes.send in
// budget.toml). A budget that cannot be read counts as off.
func (e *Engine) OutcomesEnabled() bool {
	b, err := e.Budget()
	return err == nil && b.Outcomes.Send
}

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

// facts returns the current route table.
func (e *Engine) facts() *table.Table {
	if e.Table == nil {
		return nil
	}
	return e.Table.Table()
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

// TeamPolicy is the team policy in force, or nil.
func (e *Engine) TeamPolicy() *team.Policy { return team.Load(e.Dirs, e.Table.Keys()) }

func (e *Engine) RuleFiles(s Scope) rules.Files {
	f := rules.Files{Global: e.Dirs.RulesFile(), Team: e.TeamPolicy().LocalRules()}
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
		switch {
		case strings.HasPrefix(ig.Why, "invalid"):
			out = append(out, fmt.Sprintf("rule %s (%s) ignored: %s", ig.Rule.ID, ig.Rule.Scope, ig.Why))
		case ig.Rule.Scope == rules.ScopeProject:
			out = append(out, fmt.Sprintf("project rules tried to loosen a limit (%s: %s); ignored — add it yourself if you want it", ig.Rule.String(), ig.Why))
		case ig.Rule.Scope == rules.ScopeTeam:
			out = append(out, fmt.Sprintf("team rules tried to loosen a limit (%s: %s); ignored", ig.Rule.String(), ig.Why))
		default:
			out = append(out, fmt.Sprintf("rule %s (%s) ignored: %s", ig.Rule.ID, ig.Rule.Scope, ig.Why))
		}
	}
	return out
}

// EffectiveBudget applies budget overrides from rules: the user's own (session, else global) set the
// value; a team's or a project's only ever lowers it. The team's budget share is one such override on
// monthly_usd, so the monthly cap is the lesser of the user's own and the share.
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

// ProjectRaises lists the team and project budget overrides that do not lower the user's own limit: they
// may only tighten, so these are ignored and reported. own is the budget with the user's overrides only.
// A team budget share above the user's own cap is not reported: the user's cap is simply the lower one.
func ProjectRaises(own budget.Budget, d rules.Decision) []rules.Ignored {
	var out []rules.Ignored
	for _, r := range d.ProjectCaps {
		if r.Scope == rules.ScopeTeam && r.ID == team.ShareRuleID {
			continue
		}
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

// effectiveBudget applies the overrides and adds team and project overrides that tried to raise a limit
// to the set's ignored rules.
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

// Routes.
const (
	RouteClaude   = "claude"          // the lead model does it in the session
	RouteSubagent = "claude-subagent" // a native Claude subagent on a smaller model
	RouteDirect   = "direct"          // one OpenRouter call
	RouteAgent    = "agent"           // a sandboxed OpenCode session
	RouteLocal    = "local"           // a local tool, no model
	RouteEngine   = "engine"          // an OpenRouter PDF engine, no chat model
)

type (
	Option    = remote.Option
	Breakdown = remote.Breakdown
	Exclusion = remote.Exclusion
	Match     = remote.Match
	Work      = remote.Work
)

type Request struct {
	UseCase string   `json:"use_case,omitempty"`
	Profile *Profile `json:"profile,omitempty"`
	Stakes  int      `json:"stakes,omitempty"`
	Lead    string   `json:"lead_model,omitempty"`
	Top     int      `json:"top,omitempty"`
	Record  bool     `json:"-"`
	Scope
}

type UseCaseInfo struct {
	ID          string `json:"id"`
	N           int    `json:"n"`
	Task        string `json:"task"`
	Kind        string `json:"kind"`
	Family      string `json:"family"`
	Path        string `json:"path"`
	Verify      string `json:"verify"`
	Method      string `json:"verify_method"`
	StakesRange []int  `json:"stakes_range"`
	QualityBar  string `json:"quality_bar"`
	EditsCode   bool   `json:"edits_code"`
	Preprocess  string `json:"preprocess,omitempty"`
}

type Recommendation struct {
	OfferID        string               `json:"offer_id,omitempty"`
	UseCase        UseCaseInfo          `json:"use_case"`
	Classification []Match              `json:"classification,omitempty"`
	Stakes         int                  `json:"stakes"`
	StakesNote     string               `json:"stakes_note,omitempty"`
	Work           Work                 `json:"work"`
	Lead           string               `json:"lead_model"`
	Lambda         Lambda               `json:"lambda"`
	Pick           *Option              `json:"pick"`
	Announce       string               `json:"announce"`
	Options        []Option             `json:"options"`
	Excluded       []Exclusion          `json:"excluded,omitempty"`
	ClaudeFeasible bool                 `json:"claude_feasible"`
	Ceiling        float64              `json:"stakes_ceiling"`
	Budget         BudgetLine           `json:"budget"`
	Rules          []string             `json:"rules_applied,omitempty"`
	Suggestions    []history.Suggestion `json:"suggestions,omitempty"`
	Privacy        string               `json:"privacy"`
	SecondOpinion  *SecondOpinion       `json:"second_opinion,omitempty"`
	Notes          []string             `json:"notes,omitempty"`
	DataAsOf       string               `json:"data_as_of,omitempty"`
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
