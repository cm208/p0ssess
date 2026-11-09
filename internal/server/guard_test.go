package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"p0ssess/internal/config"
	"p0ssess/internal/direct"
	"p0ssess/internal/history"
	"p0ssess/internal/opencode"
	"p0ssess/internal/pme/engine"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/relay"
)

const testModel = "test/cheap"

// fakeRouter is an OpenRouter stand-in for the direct path.
type fakeRouter struct {
	mu     sync.Mutex
	chat   func() (int, string)
	gen    func() (int, string)
	bodies []string
}

func withFake(t *testing.T, f *fakeRouter) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.bodies = append(f.bodies, string(b))
		h := f.chat
		if strings.HasSuffix(r.URL.Path, "/generation") {
			h = f.gen
		}
		f.mu.Unlock()
		if h == nil {
			w.WriteHeader(404)
			io.WriteString(w, `{"error":{"code":404,"message":"not found"}}`)
			return
		}
		code, body := h()
		w.WriteHeader(code)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	old := newClient
	newClient = func(key string) *direct.Client {
		c := direct.New(key)
		c.BaseURL = srv.URL
		c.Catalog = direct.NewCatalog(srv.URL, srv.Client())
		c.Catalog.NoLive = true
		c.Catalog.Add(direct.ModelInfo{ID: testModel, Input: []string{"text"}, Output: []string{"text"},
			Routes: []string{"/api/v1/chat/completions"}, InPerM: 1, OutPerM: 1})
		c.StateDir = t.TempDir()
		c.GenerationWait = 0
		return c
	}
	t.Cleanup(func() { newClient = old })
}

func directEnv(t *testing.T, budgetTOML string) (config.Config, *engine.Engine, string) {
	home := t.TempDir()
	t.Setenv("P0SSESS_HOME", home)
	t.Setenv("P0SSESS_OPENROUTER_KEY_FILE", filepath.Join(home, "key"))
	os.WriteFile(filepath.Join(home, "key"), []byte("test-key\n"), 0o600)
	os.WriteFile(filepath.Join(home, "budget.toml"), []byte(budgetTOML), 0o600)
	proj := filepath.Join(home, "proj")
	os.MkdirAll(filepath.Join(proj, ".git"), 0o755)
	t.Setenv("CLAUDE_PROJECT_DIR", proj)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	eng, err := engine.Load(paths.Default())
	if err != nil {
		t.Fatal(err)
	}
	return cfg, eng, proj
}

func ledger(t *testing.T, eng *engine.Engine) (*history.Ledger, history.Spend) {
	es, err := history.Read(eng.Dirs.HistoryFile())
	if err != nil {
		t.Fatal(err)
	}
	l := history.NewLedger(es)
	return l, l.Spend(time.Now())
}

const okChat = `{"id":"gen-1","model":"test/cheap","choices":[{"message":{"content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"cost":0.00002}}`
const noCostChat = `{"id":"gen-9","model":"test/cheap","choices":[{"message":{"content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`

func TestRunDirectMeasuredEstimatedAndReleased(t *testing.T) {
	f := &fakeRouter{chat: func() (int, string) { return 200, okChat }}
	withFake(t, f)
	cfg, eng, _ := directEnv(t, "monthly_usd = 1\nper_task_usd = 0.05\ndaily_usd = -1\n")
	req := direct.Request{Model: testModel, Prompt: "hi", MaxCostUSD: 0.05, MaxTokens: 10}

	if _, id, err := RunDirect(context.Background(), cfg, eng, req, DirectJob{}); err != nil || id == "" {
		t.Fatalf("measured run: %q %v", id, err)
	}
	l, sp := ledger(t, eng)
	if sp.Month != 0.00002 || sp.Reserved != 0 || len(l.Open()) != 0 {
		t.Errorf("measured: %+v open %d", sp, len(l.Open()))
	}

	// No cost in the answer and the generation lookup fails: the reserved maximum is recorded.
	f.chat = func() (int, string) { return 200, noCostChat }
	res, id, err := RunDirect(context.Background(), cfg, eng, req, DirectJob{})
	if err != nil || id == "" {
		t.Fatal(err)
	}
	if !strings.Contains(direct.Format(res), "cost not measured") {
		t.Errorf("no warning about the estimate:\n%s", direct.Format(res))
	}
	_, sp = ledger(t, eng)
	if sp.Estimated != 0.05 || sp.Month < 0.05 {
		t.Errorf("unknown cost must count at the reserved maximum: %+v", sp)
	}
	// Later the generation lookup works and the estimate is replaced.
	f.gen = func() (int, string) { return 200, `{"data":{"total_cost":0.00003}}` }
	Reconcile(context.Background(), cfg, eng)
	_, sp = ledger(t, eng)
	if sp.Estimated != 0 || sp.Month != 0.00005 {
		t.Errorf("after reconcile: %+v", sp)
	}

	// An error answer is not billed: the reservation is released and nothing is spent.
	f.chat = func() (int, string) { return 400, `{"error":{"code":400,"message":"bad"}}` }
	if _, id, err := RunDirect(context.Background(), cfg, eng, req, DirectJob{}); err == nil || id != "" {
		t.Errorf("error answer: id %q err %v", id, err)
	}
	l, sp2 := ledger(t, eng)
	if sp2.Month != sp.Month || sp2.Reserved != 0 || len(l.Open()) != 0 {
		t.Errorf("an unbilled call changed spend: %+v -> %+v", sp, sp2)
	}
}

func TestRunDirectEnforcesAtTheTool(t *testing.T) {
	f := &fakeRouter{chat: func() (int, string) { return 200, okChat }}
	withFake(t, f)
	cfg, eng, proj := directEnv(t, "monthly_usd = 1\nper_task_usd = 0.02\ndaily_usd = -1\n[privacy]\nmode = \"zdr\"\n")
	req := direct.Request{Model: testModel, Prompt: "hi", MaxCostUSD: 0.05, MaxTokens: 10}
	_, _, err := RunDirect(context.Background(), cfg, eng, req, DirectJob{})
	var r *engine.Refusal
	if !errors.As(err, &r) || !r.NeedsApproval {
		t.Errorf("above the per-task limit: %v", err)
	}
	req.MaxCostUSD = 0.01
	if _, _, err := RunDirect(context.Background(), cfg, eng, req, DirectJob{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.bodies[len(f.bodies)-1], `"zdr":true`) {
		t.Errorf("zdr mode did not force zero-retention routing: %s", f.bodies[len(f.bodies)-1])
	}
	_, _, err = RunDirect(context.Background(), cfg, eng, direct.Request{Model: testModel, Prompt: "hi", MaxCostUSD: 0.01, MaxTokens: 10}, DirectJob{Stakes: 4})
	if !errors.As(err, &r) || !strings.Contains(err.Error(), "stakes 4") {
		t.Errorf("stakes 4: %v", err)
	}
	// A prompt-only call from a private repository is refused before anything is sent.
	os.WriteFile(filepath.Join(proj, ".p0ssess-private"), nil, 0o600)
	n := len(f.bodies)
	_, _, err = RunDirect(context.Background(), cfg, eng, direct.Request{Model: testModel, Prompt: "hi", MaxCostUSD: 0.01, MaxTokens: 10}, DirectJob{})
	if err == nil || !strings.Contains(err.Error(), ".p0ssess-private") || len(f.bodies) != n {
		t.Errorf("private repo: %v (sent %d)", err, len(f.bodies)-n)
	}
}

type recordingObserver struct {
	relay.Observer
	released []string
}

func (o *recordingObserver) Release(g relay.Grant) {
	o.released = append(o.released, g.ReserveID)
	o.Observer.Release(g)
}

// TestRelayRunEnforcesLimits checks the agent path: the relay refuses without a guard, the guard
// refuses over the limits before any jail work, and a run that fails before its prompt is sent gives
// its reservation back.
func TestRelayRunEnforcesLimits(t *testing.T) {
	_, eng, proj := directEnv(t, "monthly_usd = 1\nper_task_usd = 0.02\ndaily_usd = -1\n")
	os.Remove(filepath.Join(os.Getenv("P0SSESS_HOME"), "key")) // the jail cannot start without a key
	cfg, _ := config.Load()
	re := relay.NewEngine(cfg, "test", io.Discard)
	req := relay.RunRequest{Task: "x", Model: "deepseek/deepseek-v4.1-flash", Cwd: proj}
	if _, err := re.Run(context.Background(), req, nil); !errors.Is(err, relay.ErrNoObserver) {
		t.Errorf("unguarded relay ran: %v", err)
	}
	obs := &recordingObserver{Observer: NewObserver(eng)}
	re.SetObserver(obs)
	_, err := re.Run(context.Background(), req, nil)
	if err == nil || !strings.Contains(err.Error(), "needs approval") {
		t.Errorf("a $0.50 run under a $0.02 per-task limit: %v", err)
	}
	req.MaxCostUSD = 0.02
	_, err = re.Run(context.Background(), req, nil)
	if err == nil || !strings.Contains(err.Error(), "jail") {
		t.Fatalf("want a jail error after authorizing, got %v", err)
	}
	l, sp := ledger(t, eng)
	if len(obs.released) != 1 || len(l.Open()) != 0 || sp.Reserved != 0 {
		t.Errorf("reservation not released: released %v, open %d, %+v", obs.released, len(l.Open()), sp)
	}
	os.WriteFile(filepath.Join(proj, ".p0ssess-private"), nil, 0o600)
	if _, err := re.Run(context.Background(), req, nil); err == nil || !strings.Contains(err.Error(), "p0ssess-private") {
		t.Errorf("private repo: %v", err)
	}
}

// TestRelayUnknownPriceCountsAtCap covers M6 on the agent path: a model with no known price needs an
// explicit max_cost_usd, and a run OpenCode reports at $0 despite billed tokens is counted at its cap.
func TestRelayUnknownPriceCountsAtCap(t *testing.T) {
	_, eng, proj := directEnv(t, "monthly_usd = 1\nper_task_usd = 0.05\ndaily_usd = -1\n")
	obs := NewObserver(eng)
	auth := relay.RunAuth{Model: "newco/unpriced", MaxCostUSD: 0.05, MaxRuntime: time.Minute, Cwd: proj}
	if _, err := obs.Authorize(auth); err == nil || !strings.Contains(err.Error(), "no price is known") {
		t.Fatalf("default cap on an unpriced model: %v", err)
	}
	auth.ExplicitMax, auth.MaxCostUSD = true, 0.03
	g, err := obs.Authorize(auth)
	if err != nil || !g.PriceUnknown {
		t.Fatalf("explicit cap: %+v %v", g, err)
	}
	res := &relay.Result{Status: "done", Tokens: &opencode.Tokens{Input: 1200, Output: 300}}
	if err := obs.RunFinished(relay.RunInfo{SessionID: "ses-1", Turn: 1, Model: auth.Model, Grant: g}, res); err != nil {
		t.Fatal(err)
	}
	l, sp := ledger(t, eng)
	if !res.CostEstimated || sp.Estimated != 0.03 || sp.Reserved != 0 || len(l.Open()) != 0 {
		t.Errorf("a $0 report for billed tokens must count at the cap: estimated %v, %+v", res.CostEstimated, sp)
	}
}
