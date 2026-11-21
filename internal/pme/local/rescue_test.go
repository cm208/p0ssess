package local

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"p0ssess/internal/account"
	"p0ssess/internal/rules"
)

// startRescue makes the install Pro and opens a rescue period that lasts an hour.
func startRescue(t *testing.T, e *Engine) {
	t.Helper()
	account.SaveToken(e.Dirs, "p0d_test")
	os.MkdirAll(e.Dirs.State, 0o700)
	os.WriteFile(filepath.Join(e.Dirs.State, "account.json"), []byte(`{"plan":"pro","status":"active"}`), 0o600)
	now := time.Now().UTC()
	p := `{"active":{"window":"5-hour","start":"` + now.Add(-time.Minute).Format(time.RFC3339) + `","until":"` + now.Add(time.Hour).Format(time.RFC3339) + `","used":0.9}}`
	if err := os.WriteFile(filepath.Join(e.Dirs.State, "rescue.json"), []byte(p), 0o600); err != nil {
		t.Fatal(err)
	}
}

func delegated(r *Recommendation) *Option {
	for i := range r.Options {
		if paid(r.Options[i].Route) {
			return &r.Options[i]
		}
	}
	return nil
}

func TestStakes4StaysOnClaudeOutsideRescue(t *testing.T) {
	e, proj := setup(t, "monthly_usd = 10\nper_task_usd = 1\ndaily_usd = -1\n")
	r, err := e.Recommend(Request{UseCase: "code-implement-feature", Stakes: 4, Scope: Scope{Cwd: proj, Session: "s1"}})
	if err != nil {
		t.Fatal(err)
	}
	if o := delegated(r); o != nil {
		t.Fatalf("stakes 4 offered %s outside rescue", o.Model)
	}
	c := charge(proj, 0.01)
	c.Stakes = 4
	_, err = e.Authorize(c)
	refused(t, err, false, "stakes 4 stays on Claude")
	// A period that is not Pro's does nothing either.
	startRescue(t, e)
	os.Remove(filepath.Join(e.Dirs.State, "account.json"))
	account.DeleteToken(e.Dirs)
	_, err = e.Authorize(c)
	refused(t, err, false, "stakes 4 stays on Claude")
}

func TestRescueOffersStakes4WithOneYesForOneTask(t *testing.T) {
	e, proj := setup(t, "monthly_usd = 10\nper_task_usd = 1\ndaily_usd = -1\n")
	startRescue(t, e)
	sc := Scope{Cwd: proj, Session: "s1"}
	r, err := e.Recommend(Request{UseCase: "code-implement-feature", Stakes: 4, Scope: sc})
	if err != nil {
		t.Fatal(err)
	}
	o := delegated(r)
	if o == nil {
		t.Fatalf("no delegated option in rescue: %+v", r.Options)
	}
	if !o.NeedsApproval || o.Model == "" || o.EstimateUSD <= 0 || !strings.Contains(strings.Join(o.ApprovalReasons, ";"), "stakes 4") {
		t.Fatalf("not an ask with model and cost: %+v", o)
	}
	if r.Pick == nil || r.Pick.Model != o.Model || !r.Pick.NeedsApproval {
		t.Fatalf("pick is not the ask: %+v", r.Pick)
	}

	c := charge(proj, 0.05)
	c.Model, c.Stakes, c.UseCase = o.Model, 4, "code-implement-feature"
	_, err = e.Authorize(c)
	refused(t, err, true, "approvals_add")
	if _, err := e.Authorize(c); err == nil {
		t.Fatal("ran without the yes")
	}

	// The yes: one run on that model, this session.
	addRule(t, e, proj, rules.Rule{Kind: rules.KindApprove, Models: []string{o.Model}, MaxPerRunUSD: 0.10, MaxCount: 1, Scope: rules.ScopeSession})
	if _, err := e.Authorize(c); err != nil {
		t.Fatalf("the yes did not cover the task: %v", err)
	}
	// It covered one run: a second stakes-4 run, and one on another model, ask again.
	_, err = e.Authorize(c)
	refused(t, err, true, "used up")
	other := c
	other.Model = "google/gemini-3.8-flash"
	_, err = e.Authorize(other)
	refused(t, err, true, "approvals_add")
	// No standing rule was written: the approval is the one session rule.
	rs, _ := e.Rules(sc)
	for _, x := range rs.Rules {
		if x.Kind == rules.KindAllow {
			t.Errorf("rescue wrote an allow rule: %s", x.String())
		}
	}
}
