// Package rules holds the user's routing rules, standing approvals and budget
// overrides, at session, team, project and global scope.
//
// Precedence: a forbid rule at any scope beats any allow. Session and global
// rules are the user's own and have full power; for rules that pick one value
// (only, mix, budget overrides) the session beats global, and within a scope the
// newest rule wins. Team rules (the signed policy of the user's Team org) and
// project rules (.p0ssess/rules.toml, which arrives with a cloned repository)
// can only tighten: forbid rules, only and mix rules that narrow the user's own
// choice, and lower budget limits. Anything that would loosen is ignored and
// reported. Rules are only ever created by the user or the team's owner; the
// engine may suggest one.
package rules

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"p0ssess/internal/pme/paths"
)

const (
	KindForbid  = "forbid"
	KindAllow   = "allow"
	KindOnly    = "only"
	KindMix     = "mix"
	KindApprove = "approve"
	KindBudget  = "budget"
	// KindAsk makes Claude ask before a matching call even when it is within every limit.
	KindAsk = "ask"

	TargetModel        = "model"
	TargetClaudeAgents = "claude_agents"
	TargetOpenCode     = "opencode"
	TargetDelegation   = "delegation"

	ScopeSession = "session"
	ScopeTeam    = "team"
	ScopeProject = "project"
	ScopeGlobal  = "global"
)

var scopeRank = map[string]int{ScopeSession: 0, ScopeTeam: 1, ScopeProject: 2, ScopeGlobal: 3}

// tightenOnly reports the scopes whose rules may only tighten.
func tightenOnly(scope string) bool { return scope == ScopeTeam || scope == ScopeProject }

type Rule struct {
	ID     string `toml:"id" json:"id"`
	Kind   string `toml:"kind" json:"kind"`
	Target string `toml:"target,omitempty" json:"target,omitempty"`
	// Models for forbid/allow/only/mix and an optional model limit on approvals.
	Models  []string  `toml:"models,omitempty" json:"models,omitempty"`
	Weights []float64 `toml:"weights,omitempty" json:"weights,omitempty"`
	// What the rule covers; empty means everything.
	UseCases []string `toml:"use_cases,omitempty" json:"use_cases,omitempty"`
	Kinds    []string `toml:"kinds,omitempty" json:"kinds,omitempty"`
	// MaxStakes on an allow rule lifts the stakes gate up to that level.
	MaxStakes int `toml:"max_stakes,omitempty" json:"max_stakes,omitempty"`
	// Standing approval limits.
	MaxPerRunUSD float64 `toml:"max_per_run_usd,omitempty" json:"max_per_run_usd,omitempty"`
	MaxCount     int     `toml:"max_count,omitempty" json:"max_count,omitempty"`
	MaxTotalUSD  float64 `toml:"max_total_usd,omitempty" json:"max_total_usd,omitempty"`
	Period       string  `toml:"period,omitempty" json:"period,omitempty"` // "day" or "total"
	// Budget override.
	Field string  `toml:"field,omitempty" json:"field,omitempty"` // per_task_usd, daily_usd, monthly_usd
	Value float64 `toml:"value,omitempty" json:"value,omitempty"`

	Expires *time.Time `toml:"expires,omitempty" json:"expires,omitempty"`
	Created time.Time  `toml:"created" json:"created"`
	// Note keeps the user's own words for the rule.
	Note  string `toml:"note,omitempty" json:"note,omitempty"`
	Scope string `toml:"-" json:"scope"`
}

type file struct {
	Rules []Rule `toml:"rule"`
}

var budgetFields = map[string]bool{"per_task_usd": true, "daily_usd": true, "monthly_usd": true}

// Validate checks a rule's shape.
func (r Rule) Validate() error {
	for _, v := range append([]float64{r.MaxPerRunUSD, r.MaxTotalUSD, r.Value}, r.Weights...) {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return errors.New("numeric fields must be finite")
		}
	}
	switch r.Kind {
	case KindForbid, KindAllow:
		switch r.Target {
		case TargetModel:
			if len(r.Models) == 0 {
				return fmt.Errorf("%s model needs models", r.Kind)
			}
		case TargetClaudeAgents, TargetOpenCode, TargetDelegation:
		default:
			return fmt.Errorf("target must be model, claude_agents, opencode or delegation, not %q", r.Target)
		}
		if r.MaxStakes != 0 && (r.Kind != KindAllow || r.MaxStakes < 1 || r.MaxStakes > 4) {
			return errors.New("max_stakes is 1-4 and only on allow rules")
		}
	case KindOnly:
		if len(r.Models) == 0 {
			return errors.New("only needs models")
		}
	case KindMix:
		if len(r.Models) < 2 {
			return errors.New("mix needs two or more models")
		}
		if len(r.Weights) != 0 && len(r.Weights) != len(r.Models) {
			return errors.New("mix weights must match models")
		}
		for _, w := range r.Weights {
			if w <= 0 {
				return errors.New("mix weights must be positive")
			}
		}
	case KindApprove:
		if r.MaxPerRunUSD <= 0 {
			return errors.New("approve needs max_per_run_usd above 0")
		}
		if r.MaxCount < 0 || r.MaxTotalUSD < 0 {
			return errors.New("limits must be positive")
		}
		if r.Period != "" && r.Period != "day" && r.Period != "total" {
			return errors.New(`period is "day" or "total"`)
		}
	case KindAsk:
		if r.Target != "" || r.MaxStakes != 0 || r.Field != "" {
			return errors.New("ask takes only models, use_cases and kinds")
		}
	case KindBudget:
		if !budgetFields[r.Field] {
			return errors.New("field must be per_task_usd, daily_usd or monthly_usd")
		}
		if r.Value < 0 && r.Field != "daily_usd" {
			return errors.New("value must be 0 or more")
		}
		if len(r.UseCases) > 0 || len(r.Kinds) > 0 {
			return errors.New("budget overrides apply to all work; they cannot be limited to use cases or kinds")
		}
	default:
		return fmt.Errorf("kind must be forbid, allow, only, mix, approve, ask or budget, not %q", r.Kind)
	}
	for _, k := range r.Kinds {
		switch k {
		case TaskMedia, TaskGenerate, TaskAgent, TaskReview, TaskCode, TaskText, "all":
		default:
			return fmt.Errorf("unknown kind %q", k)
		}
	}
	return nil
}

func (r Rule) Active(now time.Time) bool { return r.Expires == nil || now.Before(*r.Expires) }

// Covers reports whether the rule applies to a use case.
func (r Rule) Covers(useCase, kind string) bool {
	if len(r.UseCases) > 0 && !contains(r.UseCases, useCase) {
		return false
	}
	if len(r.Kinds) > 0 {
		ok := false
		for _, k := range r.Kinds {
			if KindMatches(k, kind) {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

func (r Rule) hasModel(m string) bool {
	return len(r.Models) == 0 || contains(r.Models, m)
}

// String renders the rule as the one line Claude shows the user.
func (r Rule) String() string {
	var b strings.Builder
	what := r.coverage()
	switch r.Kind {
	case KindForbid, KindAllow:
		verb := "never use"
		if r.Kind == KindAllow {
			verb = "allow"
		}
		switch r.Target {
		case TargetModel:
			fmt.Fprintf(&b, "%s %s%s", verb, strings.Join(short(r.Models), ", "), what)
		case TargetClaudeAgents:
			fmt.Fprintf(&b, "%s Claude subagents%s", verb, what)
		case TargetOpenCode:
			fmt.Fprintf(&b, "%s the OpenCode agent path%s", verb, what)
		case TargetDelegation:
			if r.Kind == KindForbid {
				fmt.Fprintf(&b, "keep work on Claude%s", what)
			} else {
				fmt.Fprintf(&b, "allow delegation%s", what)
			}
		}
		if r.MaxStakes > 0 {
			fmt.Fprintf(&b, " up to stakes %d", r.MaxStakes)
		}
	case KindOnly:
		fmt.Fprintf(&b, "only use %s%s", strings.Join(short(r.Models), " or "), what)
	case KindMix:
		parts := short(r.Models)
		if len(r.Weights) == len(r.Models) {
			sum := 0.0
			for _, w := range r.Weights {
				sum += w
			}
			for i := range parts {
				parts[i] = fmt.Sprintf("%s %.0f%%", parts[i], 100*r.Weights[i]/sum)
			}
		}
		fmt.Fprintf(&b, "mix %s%s", strings.Join(parts, ", "), what)
	case KindApprove:
		fmt.Fprintf(&b, "auto-approve%s up to $%.2f each", orAllBare(what), r.MaxPerRunUSD)
		if len(r.Models) > 0 {
			fmt.Fprintf(&b, " on %s", strings.Join(short(r.Models), ", "))
		}
		per := ""
		if r.Period == "day" {
			per = " a day"
		}
		if r.MaxCount == 1 {
			fmt.Fprintf(&b, ", 1 run%s", per)
		} else if r.MaxCount > 0 {
			fmt.Fprintf(&b, ", %d runs%s", r.MaxCount, per)
		}
		if r.MaxTotalUSD > 0 {
			fmt.Fprintf(&b, ", $%.2f total%s", r.MaxTotalUSD, per)
		}
	case KindAsk:
		fmt.Fprintf(&b, "ask first%s", orAll(what))
		if len(r.Models) > 0 {
			fmt.Fprintf(&b, " on %s", strings.Join(short(r.Models), ", "))
		}
	case KindBudget:
		names := map[string]string{"per_task_usd": "per-task limit", "daily_usd": "daily limit", "monthly_usd": "monthly cap"}
		fmt.Fprintf(&b, "set the %s to $%.2f", names[r.Field], r.Value)
	}
	if r.Scope != "" {
		fmt.Fprintf(&b, " (%s", r.Scope)
		if r.Expires != nil {
			fmt.Fprintf(&b, ", until %s", r.Expires.Local().Format("Jan 2 15:04"))
		}
		b.WriteString(")")
	} else if r.Expires != nil {
		fmt.Fprintf(&b, " (until %s)", r.Expires.Local().Format("Jan 2 15:04"))
	}
	return b.String()
}

func (r Rule) coverage() string {
	var parts []string
	if len(r.UseCases) > 0 {
		parts = append(parts, strings.Join(r.UseCases, ", "))
	}
	if len(r.Kinds) > 0 {
		parts = append(parts, strings.Join(r.Kinds, ", ")+" work")
	}
	if len(parts) == 0 {
		return ""
	}
	return " for " + strings.Join(parts, "; ")
}

func orAll(s string) string {
	if s == "" {
		return " for all delegations"
	}
	return s
}

func orAllBare(s string) string {
	if s == "" {
		return " all delegations"
	}
	return s
}

func short(ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		if _, after, ok := strings.Cut(id, "/"); ok {
			out[i] = after
		} else {
			out[i] = id
		}
	}
	return out
}

// Files names the rule file for each scope; empty paths are skipped. Team holds the rules of a verified
// team policy, which has no file of its own here.
type Files struct {
	Session, Project, Global string
	Team                     []Rule
}

func (f Files) path(scope string) string {
	switch scope {
	case ScopeSession:
		return f.Session
	case ScopeProject:
		return f.Project
	case ScopeGlobal:
		return f.Global
	}
	return ""
}

func ReadFile(path, scope string) ([]Rule, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f file
	if _, err := toml.Decode(string(data), &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for i := range f.Rules {
		f.Rules[i].Scope = scope
	}
	return f.Rules, nil
}

func writeFile(path string, rs []Rule) error {
	var buf bytes.Buffer
	buf.WriteString("# p0ssess rules. Edit with /p0ssess-me or ask Claude; see docs/ENGINE.md.\n\n")
	if err := toml.NewEncoder(&buf).Encode(file{Rules: rs}); err != nil {
		return err
	}
	return paths.WriteFileAtomic(path, buf.Bytes(), 0o600)
}

// Set is every rule in force across scopes, and the rules that were read but not applied.
type Set struct {
	Rules   []Rule
	Ignored []Ignored
}

// Ignored is a rule that is not applied, with the reason.
type Ignored struct {
	Rule Rule   `json:"rule"`
	Why  string `json:"why"`
}

// Load reads every scope. Invalid rules and project rules that would loosen a limit are left out of
// Rules and listed in Ignored.
func Load(f Files) (Set, error) {
	var s Set
	var errs []error
	for _, scope := range []string{ScopeSession, ScopeTeam, ScopeProject, ScopeGlobal} {
		var rs []Rule
		if scope == ScopeTeam {
			for _, r := range f.Team {
				r.Scope = ScopeTeam
				rs = append(rs, r)
			}
		} else {
			var err error
			if rs, err = ReadFile(f.path(scope), scope); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		for _, r := range rs {
			if err := r.Validate(); err != nil {
				s.Ignored = append(s.Ignored, Ignored{Rule: r, Why: "invalid: " + err.Error()})
				continue
			}
			if tightenOnly(scope) {
				if why := r.Loosens(); why != "" {
					s.Ignored = append(s.Ignored, Ignored{Rule: r, Why: why})
					continue
				}
			}
			s.Rules = append(s.Rules, r)
		}
	}
	return s, errors.Join(errs...)
}

// Loosens says why a rule could widen what the user's own config allows, or "" when it can only
// tighten. Team and project scopes take only rules that tighten.
func (r Rule) Loosens() string {
	switch r.Kind {
	case KindForbid, KindOnly, KindMix, KindAsk:
		return ""
	case KindAllow:
		return "allow rules widen what may run"
	case KindApprove:
		return "standing approvals pre-approve spend"
	case KindBudget:
		if r.Field == "daily_usd" && r.Value <= 0 {
			return "it would pace or turn off the daily limit"
		}
		return ""
	}
	return "unknown rule kind"
}

// NewID makes a short random rule id.
func NewID() string {
	var b [4]byte
	rand.Read(b[:])
	return "r-" + hex.EncodeToString(b[:])
}

// Add validates and saves a rule to its scope's file, filling id and created.
func Add(f Files, r Rule, now time.Time) (Rule, error) {
	if r.Scope == "" {
		r.Scope = ScopeGlobal
	}
	if r.Scope == ScopeTeam {
		return r, errTeam
	}
	path := f.path(r.Scope)
	if path == "" {
		return r, fmt.Errorf("no %s scope available here", r.Scope)
	}
	if err := r.Validate(); err != nil {
		return r, err
	}
	if r.ID == "" {
		r.ID = NewID()
	}
	if r.Created.IsZero() {
		r.Created = now.UTC().Truncate(time.Second)
	}
	return r, paths.WithLock(path, func() error {
		rs, err := ReadFile(path, r.Scope)
		if err != nil {
			return err
		}
		for _, x := range rs {
			if x.ID == r.ID {
				return fmt.Errorf("rule %s already exists", r.ID)
			}
		}
		return writeFile(path, append(rs, r))
	})
}

var errTeam = errors.New("team rules come from the team's owner; change them with p0ssess team")

// Remove deletes a rule by id from whichever scope holds it. Team rules are never removed here.
func Remove(f Files, id string) (Rule, error) {
	for _, r := range f.Team {
		if r.ID == id {
			return Rule{}, errTeam
		}
	}
	for _, scope := range []string{ScopeSession, ScopeProject, ScopeGlobal} {
		path := f.path(scope)
		if _, err := os.Stat(path); path == "" || errors.Is(err, os.ErrNotExist) {
			continue
		}
		var found *Rule
		err := paths.WithLock(path, func() error {
			rs, err := ReadFile(path, scope)
			if err != nil {
				return err
			}
			for i, r := range rs {
				if r.ID == id {
					found = &r
					return writeFile(path, append(rs[:i], rs[i+1:]...))
				}
			}
			return nil
		})
		if err != nil {
			return Rule{}, err
		}
		if found != nil {
			return *found, nil
		}
	}
	return Rule{}, fmt.Errorf("no rule %s", id)
}

// Active returns the unexpired rules, narrowest scope first, newest first
// within a scope.
func (s Set) Active(now time.Time) []Rule {
	var out []Rule
	for _, r := range s.Rules {
		if r.Active(now) {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if scopeRank[out[i].Scope] != scopeRank[out[j].Scope] {
			return scopeRank[out[i].Scope] < scopeRank[out[j].Scope]
		}
		return out[i].Created.After(out[j].Created)
	})
	return out
}

// Decision is what the rules say about one use case.
type Decision struct {
	ForbidDelegation   *Rule
	ForbidClaudeAgents *Rule
	ForbidOpenCode     *Rule
	Forbidden          map[string]*Rule // model -> rule
	Allowed            map[string]*Rule // model -> allow rule
	AllowAll           *Rule            // allow delegation
	// Only is the narrowest only or mix rule of the user's own (session, then global), else the
	// team's, else the project's. Restrict holds team and project only and mix rules that narrow it
	// further: a model must be in Only and in every Restrict rule.
	Only     *Rule
	Restrict []*Rule
	// Budget holds the narrowest session or global override per field; ProjectBudget the lowest team or
	// project override, which only ever lowers a limit.
	Budget        map[string]Rule
	ProjectBudget map[string]Rule
	// ProjectCaps lists every team and project budget override, applied or not.
	ProjectCaps []Rule
	Approvals   []Rule // narrowest first
	// Asks are the ask rules that cover the use case; AskFor says which apply to a model.
	Asks []Rule
}

// Decide applies the rules to one use case. With no use case and no kind (a call that did not say
// what it is), forbid, only and mix rules apply whatever they are limited to, and only approvals
// that cover all work count.
func (s Set) Decide(useCase, kind string, now time.Time) Decision {
	d := Decision{Forbidden: map[string]*Rule{}, Allowed: map[string]*Rule{}, Budget: map[string]Rule{}, ProjectBudget: map[string]Rule{}}
	unknown := useCase == "" && kind == ""
	var narrowing []*Rule
	for _, r := range s.Active(now) {
		r := r
		if r.Kind == KindBudget {
			if tightenOnly(r.Scope) {
				// Every team or project override is a cap; the lowest one holds.
				d.ProjectCaps = append(d.ProjectCaps, r)
				if old, ok := d.ProjectBudget[r.Field]; !ok || r.Value < old.Value {
					d.ProjectBudget[r.Field] = r
				}
			} else if _, ok := d.Budget[r.Field]; !ok {
				d.Budget[r.Field] = r
			}
			continue
		}
		restricts := r.Kind == KindForbid || r.Kind == KindOnly || r.Kind == KindMix || r.Kind == KindAsk
		if !r.Covers(useCase, kind) && !(unknown && restricts) {
			continue
		}
		switch r.Kind {
		case KindForbid:
			switch r.Target {
			case TargetDelegation:
				if d.ForbidDelegation == nil {
					d.ForbidDelegation = &r
				}
			case TargetClaudeAgents:
				if d.ForbidClaudeAgents == nil {
					d.ForbidClaudeAgents = &r
				}
			case TargetOpenCode:
				if d.ForbidOpenCode == nil {
					d.ForbidOpenCode = &r
				}
			case TargetModel:
				for _, m := range r.Models {
					if d.Forbidden[m] == nil {
						d.Forbidden[m] = &r
					}
				}
			}
		case KindAllow:
			if r.Target == TargetModel {
				for _, m := range r.Models {
					if d.Allowed[m] == nil {
						d.Allowed[m] = &r
					}
				}
			} else if r.Target == TargetDelegation && d.AllowAll == nil {
				d.AllowAll = &r
			}
		case KindOnly, KindMix:
			if tightenOnly(r.Scope) {
				narrowing = append(narrowing, &r)
			} else if d.Only == nil {
				d.Only = &r
			}
		case KindApprove:
			d.Approvals = append(d.Approvals, r)
		case KindAsk:
			d.Asks = append(d.Asks, r)
		}
	}
	if len(narrowing) > 0 {
		if d.Only == nil {
			d.Only, narrowing = narrowing[0], narrowing[1:]
		}
		d.Restrict = narrowing
	}
	return d
}

// OnlyAllows reports whether the only, mix and project restrictions leave model in play, and the rule
// that excludes it otherwise.
func (d Decision) OnlyAllows(model string) (bool, *Rule) {
	if d.Only != nil && !contains(d.Only.Models, model) {
		return false, d.Only
	}
	for _, r := range d.Restrict {
		if !contains(r.Models, model) {
			return false, r
		}
	}
	return true, nil
}

// MaxStakesFor is the highest stakes level an allow rule lifts for a model. Only a rule that names the
// model can lift stakes 4; "allow delegation" lifts at most stakes 3.
func (d Decision) MaxStakesFor(model string) int {
	best := 0
	if r := d.Allowed[model]; r != nil && r.MaxStakes > best {
		best = r.MaxStakes
	}
	if d.AllowAll != nil && min(d.AllowAll.MaxStakes, 3) > best {
		best = min(d.AllowAll.MaxStakes, 3)
	}
	return best
}

// AskFor returns the ask rule that covers a call on model, or nil.
func (d Decision) AskFor(model string) *Rule {
	for i := range d.Asks {
		if d.Asks[i].hasModel(model) {
			return &d.Asks[i]
		}
	}
	return nil
}

// Use is how much of a standing approval has been spent.
type Use struct {
	Count int     `json:"count"`
	Total float64 `json:"total_usd"`
}

// Approval finds the narrowest standing approval covering a run of cash
// dollars on model, given each approval's use so far.
func (d Decision) Approval(model string, cash float64, used map[string]Use) (*Rule, string) {
	var why string
	for i := range d.Approvals {
		r := d.Approvals[i]
		if !r.hasModel(model) {
			continue
		}
		u := used[r.ID]
		switch {
		case cash > r.MaxPerRunUSD+1e-12:
			why = fmt.Sprintf("above the $%.2f per-run approval", r.MaxPerRunUSD)
		case r.MaxCount > 0 && u.Count >= r.MaxCount:
			why = "standing approval used up"
		case r.MaxTotalUSD > 0 && u.Total+cash > r.MaxTotalUSD+1e-12:
			why = fmt.Sprintf("standing approval has $%.2f left", math.Max(0, r.MaxTotalUSD-u.Total))
		default:
			return &d.Approvals[i], ""
		}
	}
	return nil, why
}

// PeriodStart is where an approval's count and total start.
func (r Rule) PeriodStart(now time.Time) time.Time {
	if r.Period == "day" {
		y, m, dd := now.Date()
		return time.Date(y, m, dd, 0, 0, 0, 0, now.Location())
	}
	return r.Created
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
