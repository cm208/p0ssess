package hook

import (
	"crypto/ed25519"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"p0ssess/internal/pme/paths"
	"p0ssess/internal/pme/table"
	"p0ssess/internal/rules"
	"p0ssess/internal/team"
)

// teamKeys are the keys a cached team policy must be signed with; tests replace them.
var teamKeys = func() map[string]ed25519.PublicKey { return table.PinnedKeys() }

// ruleFiles are the rule scopes in force for a prompt: the user's global and session rules, the project's
// rules and the cached, signed team policy. All local files; nothing here touches the network.
func ruleFiles(d paths.Dirs, cwd, session string) rules.Files {
	f := rules.Files{Global: d.RulesFile(), Team: team.Load(d, teamKeys()).LocalRules()}
	if root := paths.ProjectRoot(cwd); root != "" {
		f.Project = paths.ProjectRulesFile(root)
	}
	if session != "" {
		f.Session = d.SessionRulesFile(session)
	}
	return f
}

// routed is a rule that sends the work a prompt looks like to particular models.
type routed struct {
	work   *work
	models []string
	team   bool
	mix    bool
}

// ruleRoute finds an only or mix rule, limited to use cases or kinds, that covers work the prompt looks
// like, and the models the rules leave for it. A rule that covers all work only narrows the models when
// Claude chooses to delegate; it never pulls a prompt out on its own.
func ruleRoute(prompt string, set rules.Set, now time.Time) *routed {
	var specific []rules.Rule
	for _, r := range set.Active(now) {
		if (r.Kind == rules.KindOnly || r.Kind == rules.KindMix) && targeted(r) {
			specific = append(specific, r)
		}
	}
	if len(specific) == 0 {
		return nil
	}
	for _, w := range matchWork(prompt) {
		covered := false
		for _, r := range specific {
			if !r.Covers(w.id, w.kind) {
				continue
			}
			// A rule limited to kinds leaves the work the catalog keeps on Claude alone; one that names the
			// use case means it.
			if len(r.UseCases) == 0 && w.keep {
				continue
			}
			covered = true
		}
		if !covered {
			continue
		}
		d := set.Decide(w.id, w.kind, now)
		if d.ForbidDelegation != nil || d.Only == nil {
			continue
		}
		var models []string
		for _, m := range d.Only.Models {
			if ok, _ := d.OnlyAllows(m); ok && d.Forbidden[m] == nil {
				models = append(models, m)
			}
		}
		if len(models) == 0 {
			continue
		}
		rt := &routed{work: w, models: models, mix: d.Only.Kind == rules.KindMix, team: d.Only.Scope == rules.ScopeTeam}
		for _, r := range d.Restrict {
			rt.team = rt.team || r.Scope == rules.ScopeTeam
		}
		if rt.mix {
			rt.models = []string{pickMix(*d.Only, models)}
		}
		return rt
	}
	return nil
}

// targeted reports whether a rule is limited to some use cases or kinds.
func targeted(r rules.Rule) bool {
	if len(r.UseCases) > 0 {
		return true
	}
	for _, k := range r.Kinds {
		if k != "" && k != "all" && k != "*" {
			return true
		}
	}
	return false
}

// pickMix draws one of the allowed models by the mix rule's weights.
func pickMix(r rules.Rule, allowed []string) string {
	weight := map[string]float64{}
	for i, m := range r.Models {
		w := 1.0
		if len(r.Weights) == len(r.Models) {
			w = r.Weights[i]
		}
		weight[m] = w
	}
	total := 0.0
	for _, m := range allowed {
		total += weight[m]
	}
	x := rand.Float64() * total
	for _, m := range allowed {
		if x -= weight[m]; x < 0 {
			return m
		}
	}
	return allowed[len(allowed)-1]
}

// ruleHint is the hint for work a rule routes. It names the rule's work and model, never the prompt.
func ruleHint(rt *routed, ready bool) string {
	whose := "the user's p0ssess rule"
	if rt.team {
		whose = "a team rule (set by the user's team owner; the user cannot loosen it)"
	}
	what := rt.work.label + " (use case " + rt.work.id + ")"
	if !ready {
		return "p0ssess: " + whose + " sends " + what + " to " + rt.models[0] +
			", but p0ssess has no OpenRouter key yet; tell the user to run /p0ssess:p0ssess-me."
	}
	return fmt.Sprintf("p0ssess: %s sends %s to %s. If this request is that work, hand it to the p0ssess agent now "+
		`(Agent tool, subagent_type "p0ssess:p0ssess"): one sentence with the request and absolute paths, plus "model %s, use case %s", `+
		`and "edit mode" if files should change. Do not do it yourself, and do not re-decide the model or price the route. `+
		"When it returns, review it as the routing skill says for its stakes. If the request is not %s, ignore this line.",
		whose, what, rt.models[0], rt.models[0], rt.work.id, strings.TrimSpace(rt.work.label))
}
