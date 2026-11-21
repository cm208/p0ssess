package local

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"p0ssess/internal/calibrate"
	"p0ssess/internal/catalog"
	"p0ssess/internal/pme/table"
	"p0ssess/internal/rules"
)

const newModel = "google/gemini-4-carbon"

// liveCatalog serves OpenRouter's model list with one model newer than the route table.
func liveCatalog(t *testing.T, e *Engine) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"` + newModel + `","name":"Gemini 4 Carbon","context_length":1048576,
 "architecture":{"input_modalities":["text"],"output_modalities":["text"]},
 "pricing":{"prompt":"0.000002","completion":"0.00001","input_cache_read":"0.0000001"},
 "top_provider":{"max_completion_tokens":943718}}]}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("P0SSESS_MODELS_URL", srv.URL)
	e.Catalog = catalog.Open(e.Dirs.Cache)
}

// TestListedModelIsPricedAndApprovable: a model OpenRouter lists but the route table does not is named,
// priced and approved (1.0.4 item 10: "no price is known", "unknown model").
func TestListedModelIsPricedAndApprovable(t *testing.T) {
	e, proj := setup(t, "monthly_usd = 10\nper_task_usd = 0.10\ndaily_usd = 1\n")
	if e.Facts().Model(newModel) != nil {
		t.Fatal("fixture: the route table already has the model")
	}
	if _, err := e.RuleAdd(Scope{Cwd: proj, Session: "s1"}, rules.Rule{Kind: rules.KindApprove, MaxPerRunUSD: 0.5, Models: []string{newModel}}, true, 0); err == nil ||
		!strings.Contains(err.Error(), "unknown model") {
		t.Fatalf("without the live list an unlisted id must stay unknown, got %v", err)
	}
	liveCatalog(t, e)
	if _, err := e.RuleAdd(Scope{Cwd: proj, Session: "s1"}, rules.Rule{Kind: rules.KindApprove, Scope: rules.ScopeSession, MaxPerRunUSD: 0.5, Models: []string{newModel}}, false, 0); err != nil {
		t.Fatalf("approval for a listed model refused: %v", err)
	}
	m := e.Model(newModel)
	if m == nil || !m.Live {
		t.Fatalf("model = %+v", m)
	}
	est, ok := e.AgentEstimate(newModel, "")
	if !ok || est <= 0 {
		t.Fatalf("agent estimate = %v %v", est, ok)
	}
	g, err := e.Authorize(Charge{Route: "agent", Model: newModel, MaxUSD: 0.5, Fit: true, Scope: Scope{Cwd: proj, Session: "s1"}})
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if g.PriceUnknown {
		t.Error("a listed model's price is known")
	}
	if r, ok := e.Rates(newModel); !ok || r.Out != 10 || r.In != 2 {
		t.Errorf("rates = %+v %v", r, ok)
	}
	if _, err := e.RuleAdd(Scope{Cwd: proj, Session: "s1"}, rules.Rule{Kind: rules.KindApprove, MaxPerRunUSD: 0.5, Models: []string{"nobody/not-listed"}}, true, 0); err == nil {
		t.Error("an id OpenRouter does not list must stay unknown")
	}
}

func TestProvisionalQualityUntilCalibrated(t *testing.T) {
	e, _ := setup(t, "")
	p, why := Provisional(e.Dirs, newModel, "quick-code-question")
	if p != table.ProvisionalPFail || !strings.Contains(why, "provisional") {
		t.Errorf("uncalibrated = %v %q", p, why)
	}
	rs := &calibrate.Results{List: []calibrate.Result{
		{Sample: "a", UseCase: "quick-code-question", Model: newModel, Correct: true},
		{Sample: "b", UseCase: "quick-code-question", Model: newModel, Correct: true},
		{Sample: "c", UseCase: "quick-code-question", Model: newModel, Correct: false},
	}}
	if err := calibrate.SaveResults(e.Dirs, rs); err != nil {
		t.Fatal(err)
	}
	if p, why := Provisional(e.Dirs, newModel, "quick-code-question"); p != 2.0/5 || !strings.Contains(why, "3 calibration runs") {
		t.Errorf("calibrated = %v %q", p, why)
	}
}
