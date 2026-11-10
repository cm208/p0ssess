package rules

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func files(t *testing.T) Files {
	d := t.TempDir()
	return Files{Session: filepath.Join(d, "s.toml"), Project: filepath.Join(d, "p.toml"), Global: filepath.Join(d, "g.toml")}
}

var now = time.Date(2026, 11, 8, 20, 0, 0, 0, time.UTC)

func TestValidate(t *testing.T) {
	cases := []struct {
		r  Rule
		ok bool
	}{
		{Rule{Kind: KindForbid, Target: TargetModel, Models: []string{"a/b"}}, true},
		{Rule{Kind: KindForbid, Target: TargetModel}, false},
		{Rule{Kind: KindForbid, Target: TargetOpenCode}, true},
		{Rule{Kind: KindForbid, Target: "robots"}, false},
		{Rule{Kind: KindAllow, Target: TargetModel, Models: []string{"a/b"}, MaxStakes: 4}, true},
		{Rule{Kind: KindForbid, Target: TargetModel, Models: []string{"a/b"}, MaxStakes: 4}, false},
		{Rule{Kind: KindMix, Models: []string{"a/b"}}, false},
		{Rule{Kind: KindMix, Models: []string{"a/b", "c/d"}, Weights: []float64{1}}, false},
		{Rule{Kind: KindApprove, MaxPerRunUSD: 0.2, MaxCount: 10}, true},
		{Rule{Kind: KindApprove}, false},
		{Rule{Kind: KindApprove, MaxPerRunUSD: 0.2, Period: "week"}, false},
		{Rule{Kind: KindBudget, Field: "per_task_usd", Value: 0.1}, true},
		{Rule{Kind: KindBudget, Field: "yearly", Value: 1}, false},
		{Rule{Kind: KindOnly, Models: []string{"a/b"}, Kinds: []string{"media"}}, true},
		{Rule{Kind: KindOnly, Models: []string{"a/b"}, Kinds: []string{"audio"}}, false},
	}
	for i, tc := range cases {
		if err := tc.r.Validate(); (err == nil) != tc.ok {
			t.Errorf("case %d: err=%v want ok=%v", i, err, tc.ok)
		}
	}
}

func TestPrecedence(t *testing.T) {
	f := files(t)
	add := func(r Rule) Rule {
		t.Helper()
		out, err := Add(f, r, now)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	add(Rule{Kind: KindForbid, Target: TargetModel, Models: []string{"deepseek/x"}, Scope: ScopeGlobal})
	add(Rule{Kind: KindAllow, Target: TargetModel, Models: []string{"deepseek/x"}, Scope: ScopeSession})
	add(Rule{Kind: KindOnly, Models: []string{"google/g"}, Kinds: []string{"media"}, Scope: ScopeGlobal})
	add(Rule{Kind: KindOnly, Models: []string{"xiaomi/m"}, Kinds: []string{"media"}, Scope: ScopeProject})
	add(Rule{Kind: KindBudget, Field: "per_task_usd", Value: 0.10, Scope: ScopeGlobal})
	add(Rule{Kind: KindBudget, Field: "per_task_usd", Value: 0.05, Scope: ScopeSession})
	exp := now.Add(-time.Minute)
	add(Rule{Kind: KindForbid, Target: TargetDelegation, Scope: ScopeSession, Expires: &exp})

	s, err := Load(f)
	if err != nil {
		t.Fatal(err)
	}
	d := s.Decide("video-understand", "media", now)
	if d.Forbidden["deepseek/x"] == nil {
		t.Error("forbid must beat a narrower allow")
	}
	// The user's global only rule picks; the project's only rule can narrow it further but never
	// replace it, so here nothing is left.
	if d.Only == nil || d.Only.Models[0] != "google/g" || len(d.Restrict) != 1 {
		t.Errorf("the user's only should hold with the project's as a restriction, got %+v %+v", d.Only, d.Restrict)
	}
	if ok, _ := d.OnlyAllows("google/g"); ok {
		t.Error("project only rule should narrow the global one")
	}
	if d.Budget["per_task_usd"].Value != 0.05 {
		t.Errorf("narrowest budget override should win: %+v", d.Budget)
	}
	if d.ForbidDelegation != nil {
		t.Error("expired rule should not apply")
	}
	if d2 := s.Decide("sql-query-write", "code", now); d2.Only != nil {
		t.Error("media-only rule should not cover code")
	}
}

func TestApprovals(t *testing.T) {
	d := Decision{Approvals: []Rule{
		{ID: "a1", Kind: KindApprove, MaxPerRunUSD: 0.20, MaxCount: 2, Scope: ScopeSession},
		{ID: "a2", Kind: KindApprove, MaxPerRunUSD: 0.50, MaxTotalUSD: 1, Models: []string{"google/g"}, Scope: ScopeGlobal},
	}}
	cases := []struct {
		model string
		cash  float64
		used  map[string]Use
		want  string
	}{
		{"any/m", 0.15, nil, "a1"},
		{"any/m", 0.15, map[string]Use{"a1": {Count: 2}}, ""},
		{"google/g", 0.30, nil, "a2"},
		{"google/g", 0.30, map[string]Use{"a2": {Total: 0.8}}, ""},
		{"any/m", 0.30, nil, ""},
	}
	for i, tc := range cases {
		r, _ := d.Approval(tc.model, tc.cash, tc.used)
		got := ""
		if r != nil {
			got = r.ID
		}
		if got != tc.want {
			t.Errorf("case %d: got %q want %q", i, got, tc.want)
		}
	}
}

func TestRemoveAndString(t *testing.T) {
	f := files(t)
	r, err := Add(f, Rule{Kind: KindApprove, Kinds: []string{"media"}, MaxPerRunUSD: 0.2, MaxTotalUSD: 5, Period: "day", Scope: ScopeProject}, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.String(); !strings.Contains(got, "auto-approve for media work up to $0.20 each, $5.00 total a day (project)") {
		t.Errorf("String = %q", got)
	}
	mix := Rule{Kind: KindMix, Models: []string{"deepseek/a", "z-ai/b"}, Weights: []float64{3, 1}, Kinds: []string{"code"}}
	if got := mix.String(); got != "mix a 75%, b 25% for code work" {
		t.Errorf("mix String = %q", got)
	}
	if _, err := Remove(f, r.ID); err != nil {
		t.Fatal(err)
	}
	s, _ := Load(f)
	if len(s.Rules) != 0 {
		t.Error("rule not removed")
	}
	if _, err := Remove(f, "r-nope"); err == nil {
		t.Error("missing id should fail")
	}
	if _, err := Add(Files{Global: f.Global}, Rule{Kind: KindForbid, Target: TargetOpenCode, Scope: ScopeProject}, now); err == nil {
		t.Error("missing scope path should fail")
	}
}

// TestProjectRulesOnlyTighten checks that a rules file arriving with a repository cannot loosen the
// user's limits: allow, approve and loosening budget rules are ignored and reported, while forbid, only
// and lower limits apply.
func TestProjectRulesOnlyTighten(t *testing.T) {
	f := files(t)
	raw := `
[[rule]]
id = "p1"
kind = "approve"
max_per_run_usd = 1000000.0
[[rule]]
id = "p2"
kind = "allow"
target = "delegation"
max_stakes = 4
[[rule]]
id = "p3"
kind = "budget"
field = "daily_usd"
value = -1.0
[[rule]]
id = "p4"
kind = "budget"
field = "monthly_usd"
value = 500.0
[[rule]]
id = "p5"
kind = "forbid"
target = "model"
models = ["x/y"]
[[rule]]
id = "p6"
kind = "allow"
target = "model"
models = ["x/y"]
max_stakes = 99
[[rule]]
id = "p7"
kind = "budget"
field = "per_task_usd"
value = 5.0
kinds = ["media"]
`
	if err := writeRaw(f.Project, raw); err != nil {
		t.Fatal(err)
	}
	s, err := Load(f)
	if err != nil {
		t.Fatal(err)
	}
	kept := map[string]bool{}
	for _, r := range s.Rules {
		kept[r.ID] = true
	}
	if !kept["p4"] || !kept["p5"] || len(s.Rules) != 2 {
		t.Errorf("kept %v, want only the forbid and the budget override (applied as a cap)", kept)
	}
	ignored := map[string]string{}
	for _, ig := range s.Ignored {
		ignored[ig.Rule.ID] = ig.Why
	}
	for _, id := range []string{"p1", "p2", "p3", "p6", "p7"} {
		if ignored[id] == "" {
			t.Errorf("%s should be ignored: %v", id, ignored)
		}
	}
	d := s.Decide("video-understand", "media", now)
	if len(d.Approvals) != 0 || d.AllowAll != nil || d.MaxStakesFor("x/y") != 0 {
		t.Errorf("project loosening applied: %+v", d)
	}
	if d.Forbidden["x/y"] == nil {
		t.Error("project forbid should apply")
	}
	if _, ok := d.Budget["monthly_usd"]; ok {
		t.Error("project budget override must not set the value outright")
	}
	if d.ProjectBudget["monthly_usd"].Value != 500 {
		t.Errorf("project budget override should be kept as a cap: %+v", d.ProjectBudget)
	}

	// The same rules at global scope keep their full power.
	g := files(t)
	if err := writeRaw(g.Global, strings.ReplaceAll(raw, "max_stakes = 99", "max_stakes = 4")); err != nil {
		t.Fatal(err)
	}
	gs, _ := Load(g)
	gd := gs.Decide("video-understand", "media", now)
	if len(gd.Approvals) != 1 || gd.AllowAll == nil || gd.Budget["daily_usd"].Value != -1 {
		t.Errorf("global rules lost power: %+v", gd)
	}
}

func TestStakesFourNeedsANamedModel(t *testing.T) {
	d := Decision{Allowed: map[string]*Rule{}, AllowAll: &Rule{Kind: KindAllow, Target: TargetDelegation, MaxStakes: 4}}
	if got := d.MaxStakesFor("a/b"); got != 3 {
		t.Errorf("allow delegation lifted stakes to %d, want 3", got)
	}
	d.Allowed["a/b"] = &Rule{Kind: KindAllow, Target: TargetModel, Models: []string{"a/b"}, MaxStakes: 4}
	if got := d.MaxStakesFor("a/b"); got != 4 {
		t.Errorf("named allow = %d, want 4", got)
	}
}

func TestUnknownUseCaseAppliesScopedForbids(t *testing.T) {
	s := Set{Rules: []Rule{
		{ID: "f", Kind: KindForbid, Target: TargetModel, Models: []string{"a/b"}, Kinds: []string{"media"}, Scope: ScopeGlobal},
		{ID: "a", Kind: KindApprove, MaxPerRunUSD: 1, Kinds: []string{"media"}, Scope: ScopeGlobal},
	}}
	d := s.Decide("", "", now)
	if d.Forbidden["a/b"] == nil {
		t.Error("a call that names no use case escaped a scoped forbid")
	}
	if len(d.Approvals) != 0 {
		t.Error("a scoped approval covered a call that names no use case")
	}
}

func writeRaw(path, s string) error { return os.WriteFile(path, []byte(s), 0o600) }

func TestAskRules(t *testing.T) {
	r := Rule{Kind: KindAsk, Kinds: []string{"media"}, Models: []string{"x-ai/grok-5"}, Scope: ScopeProject}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	if r.Loosens() != "" {
		t.Error("an ask rule only tightens, so a project may set it")
	}
	if got := r.String(); got != "ask first for media work on grok-5 (project)" {
		t.Errorf("text = %q", got)
	}
	if err := (Rule{Kind: KindAsk, Field: "per_task_usd"}).Validate(); err == nil {
		t.Error("ask with a budget field accepted")
	}
	d := Set{Rules: []Rule{r}}.Decide("video-understand", "media", time.Now())
	if d.AskFor("x-ai/grok-5") == nil || d.AskFor("google/gemini-3.8-flash") != nil {
		t.Errorf("ask rules match by model: %+v", d.Asks)
	}
	if d := (Set{Rules: []Rule{r}}).Decide("", "", time.Now()); d.AskFor("x-ai/grok-5") == nil {
		t.Error("a call that does not say what it is meets every ask rule")
	}
}
