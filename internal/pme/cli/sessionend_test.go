package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"p0ssess/internal/account"
	"p0ssess/internal/pme/local"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/pme/table/tabletest"
	"p0ssess/internal/team"
)

func TestSessionEndStartsTheSenderAndReturnsAtOnce(t *testing.T) {
	var started int
	old := startSender
	startSender = func() { started++ }
	t.Cleanup(func() { startSender = old })
	t.Setenv("P0SSESS_HOME", t.TempDir())
	d := paths.Default()

	proj := t.TempDir()
	begin := time.Now()
	if code := sessionEnd(strings.NewReader(`{"hook_event_name":"SessionEnd","reason":"other","cwd":`+quote(proj)+`}`), d); code != 0 || started != 1 {
		t.Fatalf("code %d, started %d", code, started)
	}
	if time.Since(begin) > time.Second {
		t.Error("the hook waited")
	}
	// A bad or empty hook input still sends: the records are not tied to the directory.
	if sessionEnd(strings.NewReader(``), d); started != 2 {
		t.Errorf("started %d", started)
	}
}

func TestSessionEndSendsNothingFromAPrivateRepository(t *testing.T) {
	var started int
	old := startSender
	startSender = func() { started++ }
	t.Cleanup(func() { startSender = old })
	t.Setenv("P0SSESS_HOME", t.TempDir())

	proj := t.TempDir()
	os.WriteFile(filepath.Join(proj, ".p0ssess-private"), nil, 0o600)
	sub := filepath.Join(proj, "sub")
	os.Mkdir(sub, 0o700)
	sessionEnd(strings.NewReader(`{"cwd":`+quote(sub)+`}`), paths.Default())
	if started != 0 {
		t.Error("a private repository's session end started an upload")
	}
}

func quote(s string) string { return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"` }

func TestSessionEndSendsTheTeamSpendQueue(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/team/spend" {
			w.WriteHeader(503)
			return
		}
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		w.WriteHeader(202)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("P0SSESS_HOME", t.TempDir())
	t.Setenv("P0SSESS_CLOUD_URL", srv.URL)
	d := paths.Default()
	account.SaveToken(d, "p0d_test")
	st, _ := json.Marshal(map[string]any{"plan": "team", "status": "active", "account_id": "acc_1", "org_id": "org_1"})
	paths.WriteFileAtomic(filepath.Join(d.State, "account.json"), st, 0o600)
	p, _ := json.Marshal(map[string]any{"v": 1, "type": "team-policy", "account_id": "acc_1", "issued": "2026-11-13T07:00:00Z",
		"rules": []any{}, "report_spend": true})
	_, priv := tabletest.Key()
	paths.WriteFileAtomic(filepath.Join(d.State, "team-policy.json"), tabletest.Seal(priv, p), 0o600)

	e, err := local.Load(d)
	if err != nil {
		t.Fatal(err)
	}
	e.Table = tabletest.Store(d.Cache)
	e.Table.Checked(time.Now())
	e.RecordRun(local.Run{ID: "d-1", Route: "direct", Model: "google/gemini-3.8-flash", CashUSD: 0.03})
	if len(team.Queued(d)) != 1 {
		t.Fatal("run not queued")
	}
	sendAll(context.Background(), e)
	if len(bodies) != 1 || !strings.Contains(bodies[0], `"model":"google/gemini-3.8-flash"`) || len(team.Queued(d)) != 0 {
		t.Errorf("bodies %v queue %d", bodies, len(team.Queued(d)))
	}
}
