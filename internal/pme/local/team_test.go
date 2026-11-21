package local

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"p0ssess/internal/account"
	"p0ssess/internal/pme/remote"
	"p0ssess/internal/pme/table/tabletest"
	"p0ssess/internal/rules"
	"p0ssess/internal/team"
)

// joinTeam links the install to acc_1 in org_1 and caches a policy signed with the test key.
func joinTeam(t *testing.T, e *Engine, share any, rs ...map[string]any) {
	t.Helper()
	if err := account.SaveToken(e.Dirs, "p0d_test"); err != nil {
		t.Fatal(err)
	}
	st, _ := json.Marshal(map[string]any{"plan": "team", "status": "active", "account_id": "acc_1", "org_id": "org_1"})
	os.MkdirAll(e.Dirs.State, 0o700)
	os.WriteFile(filepath.Join(e.Dirs.State, "account.json"), st, 0o600)
	if rs == nil {
		rs = []map[string]any{}
	}
	p := map[string]any{"v": 1, "type": "team-policy", "org_id": "org_1", "org_name": "Acme", "account_id": "acc_1",
		"role": "member", "issued": "2026-11-13T07:00:00Z", "rules": rs, "report_spend": true}
	if share != nil {
		p["monthly_share_usd"] = share
	}
	b, _ := json.Marshal(p)
	_, priv := tabletest.Key()
	os.WriteFile(filepath.Join(e.Dirs.State, "team-policy.json"), tabletest.Seal(priv, b), 0o600)
}

func TestTeamShareCapsMonthly(t *testing.T) {
	e, proj := setup(t, "plan = \"api\"\nmonthly_usd = 10\n")
	joinTeam(t, e, 4)
	st, err := e.Status(Scope{Cwd: proj})
	if err != nil {
		t.Fatal(err)
	}
	if st.Budget.MonthlyUSD != 4 || len(st.Notes) != 0 {
		t.Errorf("share below own cap should hold: %v %v", st.Budget.MonthlyUSD, st.Notes)
	}
	joinTeam(t, e, 50)
	st, _ = e.Status(Scope{Cwd: proj})
	if st.Budget.MonthlyUSD != 10 || len(st.Notes) != 0 {
		t.Errorf("share above own cap changes nothing and is not reported: %v %v", st.Budget.MonthlyUSD, st.Notes)
	}
	joinTeam(t, e, 0)
	st, _ = e.Status(Scope{Cwd: proj})
	if st.Budget.MonthlyUSD != 0 {
		t.Errorf("a share of nothing caps at 0: %v", st.Budget.MonthlyUSD)
	}
	// A user's own lower override still wins; a team budget rule only lowers.
	joinTeam(t, e, 8, map[string]any{"id": "t-00000001", "kind": "budget", "field": "per_task_usd", "value": 5})
	if _, err := e.RuleAdd(Scope{Cwd: proj}, rules.Rule{Kind: rules.KindBudget, Field: "monthly_usd", Value: 6, Scope: rules.ScopeGlobal}, false, 0); err != nil {
		t.Fatal(err)
	}
	st, _ = e.Status(Scope{Cwd: proj})
	if st.Budget.MonthlyUSD != 6 || st.Budget.PerTaskUSD != 0.02 {
		t.Errorf("monthly %v per task %v", st.Budget.MonthlyUSD, st.Budget.PerTaskUSD)
	}
	if len(st.Notes) != 1 || !strings.HasPrefix(st.Notes[0], "team rules tried to loosen a limit (set the per-task limit to $5.00 (team)") {
		t.Errorf("notes %v", st.Notes)
	}
}

func TestTeamRulesRouteFromTable(t *testing.T) {
	e, proj := setup(t, "plan = \"api\"\n")
	joinTeam(t, e, nil,
		map[string]any{"id": "t-00000001", "kind": "only", "models": []string{"google/gemini-3.8-flash"}, "use_cases": []string{"video-understand"}},
		map[string]any{"id": "t-00000002", "kind": "allow", "target": "delegation"},
	)
	r, err := e.Recommend(Request{UseCase: "video-understand", Scope: Scope{Cwd: proj, Session: "s1"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Pick == nil || r.Pick.Model != "google/gemini-3.8-flash" {
		t.Fatalf("team only rule should pick: %+v", r.Pick)
	}
	found := false
	for _, x := range r.Excluded {
		if x.Model == "qwen/qwen3.8-omni-flash" && strings.Contains(x.Why, "(team)") {
			found = true
		}
	}
	if !found {
		t.Errorf("exclusion should name the team rule: %+v", r.Excluded)
	}
	if !strings.Contains(strings.Join(r.Notes, "\n"), "team rules tried to loosen a limit (allow delegation (team): allow rules widen") {
		t.Errorf("notes %v", r.Notes)
	}

	vs, _ := e.RulesList(Scope{Cwd: proj}, "")
	var scopes []string
	for _, v := range vs {
		scopes = append(scopes, v.Scope)
	}
	if strings.Join(scopes, ",") != "team,team" || vs[1].Ignored == "" {
		t.Errorf("rules list %+v", vs)
	}
	if _, err := e.RuleAdd(Scope{Cwd: proj}, rules.Rule{Kind: rules.KindForbid, Target: rules.TargetOpenCode, Scope: rules.ScopeTeam}, true, 0); err == nil {
		t.Error("team scope accepted from the client")
	}
	if _, err := e.RuleRemove(Scope{Cwd: proj}, "t-00000001"); err == nil {
		t.Error("team rule removed")
	}
}

func TestTeamRulesOnTheWire(t *testing.T) {
	e, proj := setup(t, "plan = \"api\"\n")
	srv := tabletest.Serve(t, func(req remote.Request) (int, any) { return 200, okAnswer(req) })
	joinTeam(t, e, 5,
		map[string]any{"id": "t-00000001", "kind": "forbid", "target": "model", "models": []string{"x-ai/grok-4.20"}, "note": "ours"},
		map[string]any{"id": "t-00000002", "kind": "ask", "kinds": []string{"media"}},
	)
	if _, err := e.Recommend(Request{UseCase: "video-understand", Scope: Scope{Cwd: proj}}); err != nil {
		t.Fatal(err)
	}
	req := srv.Last()
	if len(req.Rules) != 1 || req.Rules[0].Scope != "team" || req.Rules[0].ID != "t-00000001" {
		t.Errorf("wire rules %+v", req.Rules)
	}
}

func TestPaidRunsAreReportedToTheTeam(t *testing.T) {
	e, proj := setup(t, "plan = \"api\"\n")
	if err := e.RecordRun(Run{ID: "d-0", Route: "direct", Model: "google/gemini-3.8-flash", CashUSD: 0.03, Cwd: proj}); err != nil {
		t.Fatal(err)
	}
	if len(team.Queued(e.Dirs)) != 0 {
		t.Error("reported without a team")
	}
	joinTeam(t, e, nil)
	e.RecordRun(Run{ID: "d-1", Route: "direct", Model: "google/gemini-3.8-flash", CashUSD: 0.03, UseCase: "video-understand", Cwd: proj, Session: "s1"})
	e.RecordRun(Run{ID: "d-2", Route: "direct", Model: "google/gemini-3.8-flash", CashUSD: 0, Cwd: proj})
	e.RecordRun(Run{ID: "d-3", Route: "agent", Model: "openai/gpt-6.1-sol", CashUSD: 0.5, Estimated: true, Cwd: proj})
	priv := filepath.Join(proj, "secret")
	os.MkdirAll(priv, 0o755)
	os.WriteFile(filepath.Join(priv, ".p0ssess-private"), nil, 0o600)
	e.RecordRun(Run{ID: "d-4", Route: "direct", Model: "google/gemini-3.8-flash", CashUSD: 0.03, Cwd: priv})
	if err := e.Reconcile("d-3", 0.2); err != nil {
		t.Fatal(err)
	}
	q := team.Queued(e.Dirs)
	if len(q) != 2 || q[0].UseCase != "video-understand" || q[0].Route != "direct" || q[1].CostUSD != 0.2 || q[1].Route != "agent" {
		t.Fatalf("queue %+v", q)
	}
	if q[0].Day != time.Now().UTC().Format("2006-01-02") {
		t.Errorf("day %q", q[0].Day)
	}
	raw, _ := os.ReadFile(filepath.Join(e.Dirs.State, "team-spend.jsonl"))
	for _, s := range []string{proj, "s1", "d-1"} {
		if strings.Contains(string(raw), s) {
			t.Errorf("queue holds %q: %s", s, raw)
		}
	}
}

// A member's run goes through Authorize, the gate every entry point (MCP direct and run, the CLI,
// agents) passes: the team's rules and share must hold there, not only in recommend.
func TestTeamPolicyGatesAMembersRun(t *testing.T) {
	e, proj := setup(t, "plan = \"api\"\nmonthly_usd = 10\nper_task_usd = 1\ndaily_usd = -1\n")
	if _, err := e.Authorize(charge(proj, 0.01)); err != nil {
		t.Fatalf("no team yet: %v", err)
	}

	// forbid model
	joinTeam(t, e, nil, map[string]any{"id": "t-00000001", "kind": "forbid", "target": "model", "models": []string{cheap}, "note": "not approved"})
	_, err := e.Authorize(charge(proj, 0.01))
	refused(t, err, false, "never use deepseek-v4.1-flash")

	// keep work on Claude
	joinTeam(t, e, nil, map[string]any{"id": "t-00000002", "kind": "forbid", "target": "delegation"})
	_, err = e.Authorize(charge(proj, 0.01))
	refused(t, err, false, "rule:")

	// only: another model is refused, the named one runs
	joinTeam(t, e, nil, map[string]any{"id": "t-00000003", "kind": "only", "models": []string{"google/gemini-3.8-flash"}})
	_, err = e.Authorize(charge(proj, 0.01))
	refused(t, err, false, "only use gemini-3.8-flash")
	c := charge(proj, 0.01)
	c.Model = "google/gemini-3.8-flash"
	if _, err := e.Authorize(c); err != nil {
		t.Errorf("the team's only model was refused: %v", err)
	}

	// ask: a team approval needs the member's yes for this run; their own standing approval does not cover it
	joinTeam(t, e, nil, map[string]any{"id": "t-00000004", "kind": "ask", "models": []string{cheap}})
	addRule(t, e, proj, rules.Rule{Kind: rules.KindApprove, Models: []string{cheap}, MaxPerRunUSD: 1, Scope: rules.ScopeGlobal})
	_, err = e.Authorize(charge(proj, 0.01))
	refused(t, err, true, "your rule says to ask")
	os.Remove(e.Dirs.RulesFile())

	// budget per task: lowers the member's auto-approve limit, so a run above it needs a yes
	joinTeam(t, e, nil, map[string]any{"id": "t-00000005", "kind": "budget", "field": "per_task_usd", "value": 0.05})
	if _, err := e.Authorize(charge(proj, 0.04)); err != nil {
		t.Errorf("under the team's per-task limit: %v", err)
	}
	_, err = e.Authorize(charge(proj, 0.2))
	refused(t, err, true, "above your $0.05 auto-approve limit")

	// the member's share caps the month below their own monthly limit
	joinTeam(t, e, 0.5)
	_, err = e.Authorize(charge(proj, 0.9))
	refused(t, err, false, "$0.50 monthly cap")

	// leaving the team lifts it all
	os.Remove(filepath.Join(e.Dirs.State, "team-policy.json"))
	if _, err := e.Authorize(charge(proj, 0.9)); err != nil {
		t.Errorf("after leaving: %v", err)
	}
}

// writePolicy caches a signed policy issued at issued, with the team's budget and month total.
func writePolicy(t *testing.T, e *Engine, issued time.Time, cap, spent float64, rs ...map[string]any) {
	t.Helper()
	joinTeam(t, e, nil)
	if rs == nil {
		rs = []map[string]any{}
	}
	p := map[string]any{"v": 1, "type": "team-policy", "org_id": "org_1", "org_name": "Acme", "account_id": "acc_1",
		"role": "member", "issued": issued.UTC().Format(time.RFC3339), "rules": rs, "report_spend": true,
		"team_monthly_usd": cap, "team_spent_usd": spent, "month": issued.UTC().Format("2006-01")}
	b, _ := json.Marshal(p)
	_, priv := tabletest.Key()
	os.WriteFile(filepath.Join(e.Dirs.State, "team-policy.json"), tabletest.Seal(priv, b), 0o600)
}

func TestTeamApprovalCoversEverySeat(t *testing.T) {
	e, proj := setup(t, "plan = \"api\"\nmonthly_usd = 10\nper_task_usd = 0.05\ndaily_usd = -1\n")
	_, err := e.Authorize(charge(proj, 0.3))
	refused(t, err, true, "above your $0.05 auto-approve limit")

	// The owner pre-approves up to $0.50 a task: the member is not asked below it.
	joinTeam(t, e, nil, map[string]any{"id": "t-00000001", "kind": "approve", "max_per_run_usd": 0.5})
	g, err := e.Authorize(charge(proj, 0.3))
	if err != nil || g.ApprovalID != "t-00000001" {
		t.Fatalf("team approval did not cover it: %+v %v", g, err)
	}
	e.Release(g)
	_, err = e.Authorize(charge(proj, 0.8))
	refused(t, err, true, "above the $0.50 per-run approval")

	// A team or member ask rule still needs a yes for the run; a team approval does not lift stakes 4.
	joinTeam(t, e, nil, map[string]any{"id": "t-00000001", "kind": "approve", "max_per_run_usd": 0.5},
		map[string]any{"id": "t-00000002", "kind": "ask", "models": []string{cheap}})
	_, err = e.Authorize(charge(proj, 0.3))
	refused(t, err, true, "your rule says to ask")
	joinTeam(t, e, nil, map[string]any{"id": "t-00000001", "kind": "approve", "max_per_run_usd": 0.5})
	c := charge(proj, 0.3)
	c.Stakes = 4
	if _, err := e.Authorize(c); err == nil {
		t.Error("a team approval ran stakes 4 work")
	}
	// It applies within the team budget, not past it.
	writePolicy(t, e, time.Now().Add(-time.Minute), 1, 0.9, map[string]any{"id": "t-00000001", "kind": "approve", "max_per_run_usd": 0.5})
	_, err = e.Authorize(charge(proj, 0.3))
	refused(t, err, false, "would pass the team's $1.00 monthly budget")
}

func TestTeamBudgetIsAHardSharedCap(t *testing.T) {
	e, proj := setup(t, "plan = \"api\"\nmonthly_usd = 10\nper_task_usd = 1\ndaily_usd = -1\n")
	issued := time.Now().Add(-time.Minute)
	writePolicy(t, e, issued, 2, 1.5)
	g, err := e.Authorize(charge(proj, 0.4))
	if err != nil {
		t.Fatalf("under the team budget: %v", err)
	}
	// This seat's run after the policy was issued counts toward the team total.
	if err := e.Settle(g, Run{ID: "d-1", Route: "direct", Model: cheap, CashUSD: 0.4, Cwd: proj}); err != nil {
		t.Fatal(err)
	}
	_, err = e.Authorize(charge(proj, 0.2))
	refused(t, err, false, "would pass the team's $2.00 monthly budget ($1.90 spent this month)")
	// Fit lowers the ceiling to the room left when the estimate fits.
	c := charge(proj, 0.2)
	c.Fit, c.EstimateUSD = true, 0.05
	if g, err := e.Authorize(c); err != nil || g.MaxUSD > 0.1+1e-9 {
		t.Errorf("fit: %+v %v", g, err)
	} else {
		e.Release(g)
	}

	writePolicy(t, e, time.Now().Add(-time.Minute), 2, 2)
	_, err = e.Authorize(charge(proj, 0.01))
	refused(t, err, false, "the team's $2.00 monthly budget is used up")

	// A policy from last month does not carry its total into this one.
	last := time.Now().UTC().AddDate(0, -1, -2)
	writePolicy(t, e, last, 2, 2)
	if _, err := e.Authorize(charge(proj, 0.01)); err != nil {
		t.Errorf("new month: %v", err)
	}
	// No team budget, no team cap.
	joinTeam(t, e, nil)
	if _, err := e.Authorize(charge(proj, 0.9)); err != nil {
		t.Errorf("no team budget: %v", err)
	}
}
