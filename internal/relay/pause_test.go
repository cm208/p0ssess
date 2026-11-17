package relay

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"p0ssess/internal/config"
	"p0ssess/internal/opencode"
)

// An abort on a run that waits on a permission request reports the run as stopped, not as still waiting.
func TestAbortWhilePausedIsNotNeedsPermission(t *testing.T) {
	t.Setenv("P0SSESS_HOME", t.TempDir())
	w := newTamperWatch()
	w.rec.PromptAt = time.Now()
	w.pending = &opencode.PermissionRequest{ID: "req_1", Action: "edit"}
	w.markRelayInterrupt()
	go func() {
		time.Sleep(100 * time.Millisecond)
		w.finish("interrupted", "interrupted (caller)")
	}()
	res, err := w.e.block(context.Background(), w, 5*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "interrupted" || res.Permission != nil {
		t.Fatalf("status %q permission %+v", res.Status, res.Permission)
	}
}

// Even before the interrupt lands, a run being aborted does not report a pending request.
func TestAbortingRunHidesPendingRequest(t *testing.T) {
	t.Setenv("P0SSESS_HOME", t.TempDir())
	w := newTamperWatch()
	w.rec.PromptAt = time.Now()
	w.pending = &opencode.PermissionRequest{ID: "req_1", Action: "edit"}
	w.markRelayInterrupt()
	res, err := w.e.block(context.Background(), w, 200*time.Millisecond, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status == "needs_permission" || res.Permission != nil {
		t.Fatalf("status %q permission %+v", res.Status, res.Permission)
	}
}

// A request nobody answers within permission_wait_s is rejected and the run is stopped with the reason.
func TestUnansweredPermissionTimesOut(t *testing.T) {
	t.Setenv("P0SSESS_HOME", t.TempDir())
	w := newTamperWatch()
	w.rec.PromptAt = time.Now()
	w.pending = &opencode.PermissionRequest{ID: "req_1", Action: "edit", Resources: []string{"a.go"}}
	w.pendingSince = time.Now().Add(-40 * time.Minute)
	if w.expirePending(30 * time.Minute) {
		// reached the dead server, which is fine; the state is what matters
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.pending != nil || !w.relayInterrupt || !strings.Contains(w.note, "no answer") || !strings.Contains(w.note, "30m") {
		t.Fatalf("pending %v interrupt %v note %q", w.pending, w.relayInterrupt, w.note)
	}
}

func TestFreshPermissionRequestIsNotExpired(t *testing.T) {
	w := newTamperWatch()
	w.pending = &opencode.PermissionRequest{ID: "req_1"}
	w.pendingSince = time.Now().Add(-time.Minute)
	if w.expirePending(30 * time.Minute) {
		t.Fatal("expired too early")
	}
	if w.pending == nil {
		t.Fatal("pending cleared")
	}
}

type fakeChooser struct {
	got ChooseRequest
	out Choice
	err error
}

func (f *fakeChooser) Authorize(RunAuth) (Grant, error)   { return Grant{}, errors.New("unused") }
func (f *fakeChooser) Release(Grant)                      {}
func (f *fakeChooser) RunStarted(RunInfo)                 {}
func (f *fakeChooser) RunFinished(RunInfo, *Result) error { return nil }
func (f *fakeChooser) Choose(c ChooseRequest) (Choice, error) {
	f.got = c
	return f.out, f.err
}

// With no model, the routing engine picks one; its reason comes back when nothing fits.
func TestRunWithNoModelAsksTheEngine(t *testing.T) {
	t.Setenv("P0SSESS_HOME", t.TempDir())
	e := NewEngine(config.Config{MaxRunCostUSD: 0.4}, "test", io.Discard)
	f := &fakeChooser{err: errors.New("no model fits code-debug-fix within your rules and limits (considered 3 options)")}
	e.SetObserver(f)
	_, err := e.Run(context.Background(), RunRequest{Task: "fix the failing test in the parser", Mode: "edit"}, nil)
	if err == nil || !strings.Contains(err.Error(), "no model fits code-debug-fix") || strings.Contains(err.Error(), "model must be") {
		t.Fatalf("err %v", err)
	}
	if f.got.Task == "" || f.got.Mode != "edit" {
		t.Fatalf("chooser saw %+v", f.got)
	}

	f.err, f.out = nil, Choice{Model: "deepseek/deepseek-v4.1-flash", UseCase: "code-debug-fix", Stakes: 3, Why: "cheapest direct route"}
	req := RunRequest{Task: "fix it", Mode: "edit"}
	note, err := e.chooseModel(&req)
	if err != nil || req.Model != "deepseek/deepseek-v4.1-flash" || req.UseCase != "code-debug-fix" || req.Stakes != 3 || !strings.Contains(note, "deepseek-v4.1-flash") {
		t.Fatalf("%+v %q %v", req, note, err)
	}

	// An explicit model wins and the engine is not asked.
	f.got = ChooseRequest{}
	req = RunRequest{Task: "fix it", Model: "z-ai/glm-5.3-flash"}
	if note, err := e.chooseModel(&req); err != nil || note != "" || req.Model != "z-ai/glm-5.3-flash" || f.got.Task != "" {
		t.Fatalf("%+v %q %v %+v", req, note, err, f.got)
	}
}

// Without an observer that can choose, the refusal says how to name a model.
func TestRunWithNoModelAndNoChooser(t *testing.T) {
	e := NewEngine(config.Config{MaxRunCostUSD: 0.4}, "test", io.Discard)
	_, err := e.Run(context.Background(), RunRequest{Task: "x"}, nil)
	if err == nil || strings.Contains(err.Error(), "model must be") {
		t.Fatalf("err %v", err)
	}
}
