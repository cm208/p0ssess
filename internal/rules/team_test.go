package rules

import (
	"strings"
	"testing"
)

func TestTeamScopeTightenOnly(t *testing.T) {
	f := files(t)
	f.Team = []Rule{
		{ID: "t-00000001", Kind: KindForbid, Target: TargetModel, Models: []string{"x-ai/grok"}},
		{ID: "t-00000002", Kind: KindAllow, Target: TargetDelegation, MaxStakes: 3},
		{ID: "t-00000003", Kind: KindApprove, MaxPerRunUSD: 5},
		{ID: "t-00000004", Kind: KindBudget, Field: "daily_usd", Value: 0},
		{ID: "t-00000005", Kind: KindBudget, Field: "monthly_usd", Value: 25},
		{ID: "t-00000006", Kind: KindOnly},
	}
	s, err := Load(f)
	if err != nil {
		t.Fatal(err)
	}
	kept := map[string]string{}
	for _, r := range s.Rules {
		kept[r.ID] = r.Scope
	}
	if len(kept) != 2 || kept["t-00000001"] != ScopeTeam || kept["t-00000005"] != ScopeTeam {
		t.Errorf("kept %v, want the forbid and the budget cap at team scope", kept)
	}
	ignored := map[string]bool{}
	for _, ig := range s.Ignored {
		if ig.Rule.Scope != ScopeTeam {
			t.Errorf("ignored rule lost its scope: %+v", ig.Rule)
		}
		ignored[ig.Rule.ID] = true
	}
	for _, id := range []string{"t-00000002", "t-00000003", "t-00000004", "t-00000006"} {
		if !ignored[id] {
			t.Errorf("%s should be ignored: %v", id, ignored)
		}
	}
	d := s.Decide("video-understand", "media", now)
	if d.Forbidden["x-ai/grok"] == nil || d.AllowAll != nil || len(d.Approvals) != 0 {
		t.Errorf("team rules applied wrongly: %+v", d)
	}
	if _, ok := d.Budget["monthly_usd"]; ok {
		t.Error("a team budget rule must not set the value outright")
	}
	if d.ProjectBudget["monthly_usd"].Value != 25 || len(d.ProjectCaps) != 1 {
		t.Errorf("team budget should be a cap: %+v", d.ProjectBudget)
	}
}

func TestTeamScopeOrder(t *testing.T) {
	f := files(t)
	if _, err := Add(f, Rule{Kind: KindForbid, Target: TargetOpenCode, Scope: ScopeProject}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := Add(f, Rule{Kind: KindForbid, Target: TargetOpenCode, Scope: ScopeGlobal}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := Add(f, Rule{Kind: KindForbid, Target: TargetOpenCode, Scope: ScopeSession}, now); err != nil {
		t.Fatal(err)
	}
	f.Team = []Rule{{ID: "t-1", Kind: KindForbid, Target: TargetOpenCode}}
	s, _ := Load(f)
	var got []string
	for _, r := range s.Active(now) {
		got = append(got, r.Scope)
	}
	if strings.Join(got, ",") != "session,team,project,global" {
		t.Errorf("order %v", got)
	}
}

func TestTeamOnlyRestricts(t *testing.T) {
	// With no only rule of the user's own, the team's picks; the project's narrows it further.
	f := files(t)
	f.Team = []Rule{{ID: "t-1", Kind: KindOnly, Models: []string{"openai/whisper", "google/g"}, UseCases: []string{"audio-transcribe"}}}
	if _, err := Add(f, Rule{Kind: KindOnly, Models: []string{"google/g", "xiaomi/m"}, Scope: ScopeProject}, now); err != nil {
		t.Fatal(err)
	}
	s, _ := Load(f)
	d := s.Decide("audio-transcribe", "media", now)
	if d.Only == nil || d.Only.Scope != ScopeTeam || len(d.Restrict) != 1 || d.Restrict[0].Scope != ScopeProject {
		t.Fatalf("team only should be Only with the project's as a restriction: %+v %+v", d.Only, d.Restrict)
	}
	for m, want := range map[string]bool{"google/g": true, "openai/whisper": false, "xiaomi/m": false} {
		if ok, _ := d.OnlyAllows(m); ok != want {
			t.Errorf("%s allowed=%v want %v", m, ok, want)
		}
	}
	// Other use cases are untouched.
	if d := s.Decide("image-make", "generate", now); d.Only == nil || d.Only.Scope != ScopeProject {
		t.Errorf("the team rule covers only its use case: %+v", d.Only)
	}

	// The user's own only rule holds; the team's narrows it, and the two can leave nothing.
	if _, err := Add(f, Rule{Kind: KindOnly, Models: []string{"xiaomi/m"}, Scope: ScopeGlobal}, now); err != nil {
		t.Fatal(err)
	}
	s, _ = Load(f)
	d = s.Decide("audio-transcribe", "media", now)
	if d.Only == nil || d.Only.Scope != ScopeGlobal || len(d.Restrict) != 2 || d.Restrict[0].Scope != ScopeTeam {
		t.Fatalf("user only should hold with team then project restricting: %+v %+v", d.Only, d.Restrict)
	}
	for _, m := range []string{"xiaomi/m", "google/g", "openai/whisper"} {
		if ok, _ := d.OnlyAllows(m); ok {
			t.Errorf("%s should be excluded", m)
		}
	}
}

func TestTeamRulesAreReadOnly(t *testing.T) {
	f := files(t)
	if _, err := Add(f, Rule{Kind: KindForbid, Target: TargetOpenCode, Scope: ScopeTeam}, now); err == nil {
		t.Error("Add must refuse team scope")
	}
	f.Team = []Rule{{ID: "t-1", Kind: KindForbid, Target: TargetOpenCode}}
	if _, err := Remove(f, "t-1"); err == nil {
		t.Error("Remove must refuse a team rule")
	}
	if r := (Rule{Kind: KindForbid, Target: TargetOpenCode, Scope: ScopeTeam}); !strings.HasSuffix(r.String(), "(team)") {
		t.Errorf("team rules should say so: %q", r.String())
	}
}
