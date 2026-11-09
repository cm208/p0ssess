package rules

import (
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
	if d.Only == nil || d.Only.Models[0] != "xiaomi/m" {
		t.Errorf("narrowest only should win, got %+v", d.Only)
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
