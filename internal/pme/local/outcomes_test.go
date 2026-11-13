package local

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"p0ssess/internal/history"
)

func outcomeServer(t *testing.T, status int) (*int32, *[]byte) {
	t.Helper()
	var calls int32
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/outcomes" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		atomic.AddInt32(&calls, 1)
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
		w.Write([]byte(`{"error":"bad_request","message":"no"}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("P0SSESS_CLOUD_URL", srv.URL)
	return &calls, &body
}

func lastWeekRun(t *testing.T, e *Engine) {
	t.Helper()
	ts := e.Now().AddDate(0, 0, -7)
	err := history.Append(e.Dirs.HistoryFile(), history.Entry{TS: ts, Type: history.TypeRun, RunID: "d-1", Route: "direct",
		Model: "google/gemini-3.8-flash", CashUSD: 0.004, UseCase: "video-understand", Stakes: 2, Status: "done",
		Session: "sess-secret", Project: "/home/someone/project-secret"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSendOutcomes(t *testing.T) {
	e, _ := setup(t, "")
	calls, body := outcomeServer(t, http.StatusAccepted)
	lastWeekRun(t, e)
	if p := e.PendingOutcomes(); len(p) != 1 || p[0].Kind != "video.understand" || p[0].Cost != "<1c" {
		t.Fatalf("pending: %+v", p)
	}
	n, err := e.SendOutcomes(context.Background())
	if err != nil || n != 1 || atomic.LoadInt32(calls) != 1 {
		t.Fatalf("sent %d, err %v, calls %d", n, err, *calls)
	}
	var batch struct {
		Records []map[string]any `json:"records"`
	}
	json.Unmarshal(*body, &batch)
	var keys []string
	for k := range batch.Records[0] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != "cost,kind,model,outcome,route,stakes,v,week" {
		t.Errorf("record keys: %v", keys)
	}
	if strings.Contains(string(*body), "secret") || strings.Contains(string(*body), "d-1") {
		t.Errorf("batch carries ledger details: %s", *body)
	}
	if n, _ := e.SendOutcomes(context.Background()); n != 0 || atomic.LoadInt32(calls) != 1 {
		t.Error("one batch a week")
	}
}

func TestRefusedBatchIsDropped(t *testing.T) {
	e, _ := setup(t, "")
	calls, _ := outcomeServer(t, http.StatusBadRequest)
	lastWeekRun(t, e)
	if _, err := e.SendOutcomes(context.Background()); err != nil {
		t.Fatalf("a refused batch is not an error: %v", err)
	}
	e.Now = func() time.Time { return time.Now().AddDate(0, 0, 7) }
	e.SendOutcomes(context.Background())
	if c := atomic.LoadInt32(calls); c != 1 {
		t.Errorf("a refused batch is never resent: %d posts", c)
	}
}

func TestOutcomesOff(t *testing.T) {
	e, _ := setup(t, "[outcomes]\nsend = false\n")
	calls, _ := outcomeServer(t, http.StatusAccepted)
	lastWeekRun(t, e)
	if e.PendingOutcomes() != nil {
		t.Error("nothing is built when outcome records are off")
	}
	if n, _ := e.SendOutcomes(context.Background()); n != 0 || atomic.LoadInt32(calls) != 0 {
		t.Error("nothing is sent when outcome records are off")
	}
}
