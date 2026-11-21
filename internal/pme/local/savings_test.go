package local

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"p0ssess/internal/account"
	"p0ssess/internal/notices"
)

func setPlan(t *testing.T, e *Engine, plan, status string) {
	t.Helper()
	if err := os.WriteFile(account.Files(e.Dirs)[1], []byte(`{"plan":"`+plan+`","status":"`+status+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

func lastMonthReceipt(e *Engine) {
	at := e.Now().AddDate(0, -1, 0)
	notices.Append(e.Dirs.NoticesFile(), notices.Record{TS: at, Receipt: "(m, $0.10)", Model: "a/m", Kind: "qa-factual", CostUSD: 0.1, ClaudeUSD: 1, SavedUSD: 0.9,
		Basis: "measured", Tok: &notices.Tokens{Prompt: 100}})
}

// Free installs, unlinked or not, never send the monthly report; a linked Pro install sends it once,
// with the device token and figures only.
func TestSendSavingsReport(t *testing.T) {
	e, _ := setup(t, "")
	var calls int32
	var auth, body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		auth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("P0SSESS_CLOUD_URL", srv.URL)
	lastMonthReceipt(e)
	ctx := context.Background()

	if sent, err := e.SendSavingsReport(ctx); sent || err != nil || calls != 0 {
		t.Fatalf("unlinked: %v %v %d", sent, err, calls)
	}
	account.SaveToken(e.Dirs, "p0d_test")
	setPlan(t, e, "free", "none")
	if sent, _ := e.SendSavingsReport(ctx); sent || calls != 0 {
		t.Fatalf("a linked Free install sent the report")
	}
	setPlan(t, e, "pro", "active")
	if sent, err := e.SendSavingsReport(ctx); !sent || err != nil || calls != 1 {
		t.Fatalf("pro: %v %v %d", sent, err, calls)
	}
	if auth != "Bearer p0d_test" || !strings.Contains(body, `"saved_usd":0.9`) || strings.Contains(body, "qa-factual\":") {
		t.Errorf("auth %q body %s", auth, body)
	}
	if sent, _ := e.SendSavingsReport(ctx); sent || calls != 1 {
		t.Error("sent twice")
	}
}

func TestSavingsReportOptOut(t *testing.T) {
	e, _ := setup(t, "[report]\nsend = false\n")
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&calls, 1) }))
	t.Cleanup(srv.Close)
	t.Setenv("P0SSESS_CLOUD_URL", srv.URL)
	lastMonthReceipt(e)
	account.SaveToken(e.Dirs, "p0d_test")
	setPlan(t, e, "pro", "active")
	if sent, _ := e.SendSavingsReport(context.Background()); sent || calls != 0 {
		t.Fatal("sent with the report off")
	}
	if msg, err := e.SetSetting("report.send", "true"); err != nil || !strings.Contains(msg, "monthly savings report is on") {
		t.Fatalf("%q %v", msg, err)
	}
}
