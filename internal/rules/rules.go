// Package rules holds the user's routing rules, standing approvals and budget
// overrides, at session, project and global scope.
//
// Precedence: a forbid rule at any scope beats any allow. For everything that
// picks one value (only, mix, budget overrides) the narrowest scope wins
// (session, then project, then global), and within a scope the newest rule.
// Rules are only ever created by the user; the engine may suggest one.
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

	"p0ssess/internal/pme/catalog"
	"p0ssess/internal/pme/paths"
)

const (
	KindForbid  = "forbid"
	KindAllow   = "allow"
	KindOnly    = "only"
	KindMix     = "mix"
	KindApprove = "approve"
	KindBudget  = "budget"

	TargetModel        = "model"
	TargetClaudeAgents = "claude_agents"
	TargetOpenCode     = "opencode"
	TargetDelegation   = "delegation"

	ScopeSession = "session"
	ScopeProject = "project"
	ScopeGlobal  = "global"
)

var scopeRank = map[string]int{ScopeSession: 0, ScopeProject: 1, ScopeGlobal: 2}

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
	case KindBudget:
		if !budgetFields[r.Field] {
			return errors.New("field must be per_task_usd, daily_usd or monthly_usd")
		}
		if r.Value < 0 && r.Field != "daily_usd" {
			return errors.New("value must be 0 or more")
		}
	default:
		return fmt.Errorf("kind must be forbid, allow, only, mix, approve or budget, not %q", r.Kind)
	}
	for _, k := range r.Kinds {
		switch k {
		case catalog.KindMedia, catalog.KindGenerate, catalog.KindAgent, catalog.KindReview, catalog.KindCode, catalog.KindText, "all":
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
			if catalog.KindMatches(k, kind) {
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
		fmt.Fprintf(&b, "auto-approve%s up to $%.2f each", orAll(what), r.MaxPerRunUSD)
		if len(r.Models) > 0 {
			fmt.Fprintf(&b, " on %s", strings.Join(short(r.Models), ", "))
		}
		per := ""
		if r.Period == "day" {
			per = " a day"
		}
		if r.MaxCount > 0 {
			fmt.Fprintf(&b, ", %d runs%s", r.MaxCount, per)
		}
		if r.MaxTotalUSD > 0 {
			fmt.Fprintf(&b, ", $%.2f total%s", r.MaxTotalUSD, per)
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

// Files names the rule file for each scope; empty paths are skipped.
type Files struct {
	Session, Project, Global string
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

// Set is every rule across scopes.
type Set struct {
	Rules []Rule
}

func Load(f Files) (Set, error) {
	var s Set
	var errs []error
	for _, scope := range []string{ScopeSession, ScopeProject, ScopeGlobal} {
		rs, err := ReadFile(f.path(scope), scope)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		s.Rules = append(s.Rules, rs...)
	}
	return s, errors.Join(errs...)
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
	rs, err := ReadFile(path, r.Scope)
	if err != nil {
		return r, err
	}
	for _, x := range rs {
		if x.ID == r.ID {
			return r, fmt.Errorf("rule %s already exists", r.ID)
		}
	}
	return r, writeFile(path, append(rs, r))
}

// Remove deletes a rule by id from whichever scope holds it.
func Remove(f Files, id string) (Rule, error) {
	for _, scope := range []string{ScopeSession, ScopeProject, ScopeGlobal} {
		path := f.path(scope)
		rs, err := ReadFile(path, scope)
		if err != nil {
			return Rule{}, err
		}
		for i, r := range rs {
			if r.ID == id {
				return r, writeFile(path, append(rs[:i], rs[i+1:]...))
			}
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
	Only               *Rule            // narrowest only or mix rule
	Budget             map[string]Rule  // field -> narrowest override
	Approvals          []Rule           // narrowest first
}

func (s Set) Decide(useCase, kind string, now time.Time) Decision {
	d := Decision{Forbidden: map[string]*Rule{}, Allowed: map[string]*Rule{}, Budget: map[string]Rule{}}
	for _, r := range s.Active(now) {
		r := r
		if r.Kind == KindBudget {
			if _, ok := d.Budget[r.Field]; !ok {
				d.Budget[r.Field] = r
			}
			continue
		}
		if !r.Covers(useCase, kind) {
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
			if d.Only == nil {
				d.Only = &r
			}
		case KindApprove:
			d.Approvals = append(d.Approvals, r)
		}
	}
	return d
}

// MaxStakesFor is the highest stakes level an allow rule lifts for a model.
func (d Decision) MaxStakesFor(model string) int {
	best := 0
	if r := d.Allowed[model]; r != nil && r.MaxStakes > best {
		best = r.MaxStakes
	}
	if d.AllowAll != nil && d.AllowAll.MaxStakes > best {
		best = d.AllowAll.MaxStakes
	}
	return best
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
