package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeOpenRouter answers like OpenRouter. cost "" leaves usage.cost out (the answer still reports tokens);
// it refuses requests that do not ask for usage.
func fakeOpenRouter(t *testing.T, cost string, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		if u, _ := req["usage"].(map[string]any); u["include"] != true {
			http.Error(w, "usage.include missing", 400)
			return
		}
		if req["model"] == "bad/model" {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":{"message":"no such model","code":400}}`)
			return
		}
		usage := `"prompt_tokens":1000,"completion_tokens":200`
		if cost != "" {
			usage += `,"cost":` + cost
		}
		switch {
		case r.URL.Path == "/api/v1/responses":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"SECRET-ANSWER\"}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"usage\":{\"input_tokens\":10,\"output_tokens\":5"+
				strings.TrimPrefix(usage, `"prompt_tokens":1000,"completion_tokens":200`)+"}}}\n\n")
		case req["stream"] == true:
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"id\":\"gen-1\",\"choices\":[{\"delta\":{\"content\":\"SECRET-ANSWER\"}}]}\n\n")
			w.(http.Flusher).Flush()
			fmt.Fprint(w, "data: {\"id\":\"gen-1\",\"choices\":[],\"usage\":{"+usage+"}}\n\ndata: [DONE]\n\n")
		default:
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"gen-2","choices":[{"message":{"content":"SECRET-ANSWER"}}],"usage":{`+usage+`}}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

type meterEnv struct {
	m       *Meter
	h       http.Handler
	runFile string
	file    string
}

func newMeterEnv(t *testing.T, upstream string) *meterEnv {
	dir := t.TempDir()
	m := &Meter{Dir: dir, RunFile: filepath.Join(dir, "run.json")}
	return &meterEnv{m: m, h: m.Wrap(Handler(upstream, "k", false)), runFile: m.RunFile, file: filepath.Join(dir, MeterFile)}
}

func (e *meterEnv) start(t *testing.T, run string, capUSD float64) {
	t.Helper()
	if err := WriteRunSpec(e.runFile, RunSpec{Run: run, CapUSD: capUSD, Model: "x/y"}); err != nil {
		t.Fatal(err)
	}
}

func (e *meterEnv) post(path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w
}

const prompt = `{"model":"x/y","messages":[{"role":"user","content":"SECRET-PROMPT"}]`

func TestMeterRecordsJSONAndStreamedCost(t *testing.T) {
	var calls atomic.Int32
	e := newMeterEnv(t, fakeOpenRouter(t, "0.004", &calls).URL)
	e.start(t, "run-1", 1)
	if w := e.post("/api/v1/chat/completions", prompt+`}`); w.Code != 200 || !strings.Contains(w.Body.String(), "SECRET-ANSWER") {
		t.Fatalf("json: %d %s", w.Code, w.Body)
	}
	if w := e.post("/api/v1/chat/completions", prompt+`,"stream":true}`); w.Code != 200 || !strings.Contains(w.Body.String(), "[DONE]") {
		t.Fatalf("sse: %d %s", w.Code, w.Body)
	}
	if w := e.post("/api/v1/responses", prompt+`,"stream":true}`); w.Code != 200 {
		t.Fatalf("responses: %d %s", w.Code, w.Body)
	}
	rc, ok := ReadRunCost(e.file, "run-1")
	if !ok || rc.Calls != 3 || rc.Unknown != 0 || rc.KnownUSD < 0.01199 || rc.KnownUSD > 0.01201 {
		t.Fatalf("run cost %+v %v", rc, ok)
	}
	b, _ := os.ReadFile(e.file)
	if strings.Contains(string(b), "SECRET") {
		t.Fatalf("the meter file must never hold prompt or answer text:\n%s", b)
	}
	var c Call
	json.Unmarshal([]byte(strings.SplitN(string(b), "\n", 2)[0]), &c)
	if c.Model != "x/y" || !c.Known || c.InputTokens != 1000 || c.OutputTokens != 200 || c.Run != "run-1" {
		t.Fatalf("call line %+v", c)
	}
	if other, _ := ReadRunCost(e.file, "run-2"); other.Calls != 0 {
		t.Fatalf("another run's calls counted: %+v", other)
	}
}

func TestMeterLooksUpMissingCost(t *testing.T) {
	var calls atomic.Int32
	e := newMeterEnv(t, fakeOpenRouter(t, "", &calls).URL)
	e.start(t, "run-1", 1)
	var looked string
	e.m.Lookup = func(_ context.Context, id string) (float64, bool) { looked = id; return 0.0021, true }
	e.post("/api/v1/chat/completions", prompt+`}`)
	rc, _ := ReadRunCost(e.file, "run-1")
	if looked != "gen-2" || rc.Calls != 1 || rc.Unknown != 0 || rc.KnownUSD != 0.0021 {
		t.Fatalf("lookup %q, %+v", looked, rc)
	}
}

func TestMeterUnknownCostChargesTheRestAndStops(t *testing.T) {
	var calls atomic.Int32
	e := newMeterEnv(t, fakeOpenRouter(t, "", &calls).URL)
	e.m.Lookup = func(context.Context, string) (float64, bool) { return 0, false }
	e.start(t, "run-1", 0.5)
	if w := e.post("/api/v1/chat/completions", prompt+`}`); w.Code != 200 {
		t.Fatalf("%d", w.Code)
	}
	rc, _ := ReadRunCost(e.file, "run-1")
	if rc.Calls != 1 || rc.Unknown != 1 || rc.ChargedUSD != 0.5 || rc.UnknownInput != 1000 || rc.UnknownOutput != 200 {
		t.Fatalf("an unknown cost must be recorded as unknown and charge the rest of the cap: %+v", rc)
	}
	if w := e.post("/api/v1/chat/completions", prompt+`}`); w.Code != http.StatusPaymentRequired || calls.Load() != 1 {
		t.Fatalf("after the cap: %d, %d upstream calls", w.Code, calls.Load())
	}
	// A new run gets its own cap.
	e.start(t, "run-2", 0.5)
	if w := e.post("/api/v1/chat/completions", prompt+`}`); w.Code != 200 {
		t.Fatalf("next run: %d", w.Code)
	}
}

func TestMeterStopsAtTheCap(t *testing.T) {
	var calls atomic.Int32
	e := newMeterEnv(t, fakeOpenRouter(t, "0.004", &calls).URL)
	e.start(t, "run-1", 0.01)
	for i := 0; i < 3; i++ {
		if w := e.post("/api/v1/chat/completions", prompt+`}`); w.Code != 200 {
			t.Fatalf("call %d: %d", i, w.Code)
		}
	}
	w := e.post("/api/v1/chat/completions", prompt+`}`)
	if w.Code != http.StatusPaymentRequired || calls.Load() != 3 || !strings.Contains(w.Body.String(), "cost cap") {
		t.Fatalf("over the cap: %d %s after %d upstream calls", w.Code, w.Body, calls.Load())
	}
	// A proxy that restarts mid-run still knows what the run spent.
	m2 := &Meter{Dir: e.m.Dir, RunFile: e.runFile}
	h2 := m2.Wrap(Handler("http://127.0.0.1:1", "k", false))
	r := httptest.NewRequest("POST", "/api/v1/chat/completions", strings.NewReader(prompt+`}`))
	rw := httptest.NewRecorder()
	h2.ServeHTTP(rw, r)
	if rw.Code != http.StatusPaymentRequired {
		t.Fatalf("after a restart: %d", rw.Code)
	}
}

func TestMeterSkipsUnbilledAndOtherRequests(t *testing.T) {
	var calls atomic.Int32
	e := newMeterEnv(t, fakeOpenRouter(t, "0.004", &calls).URL)
	e.start(t, "run-1", 1)
	e.post("/api/v1/chat/completions", `{"model":"bad/model"}`)
	r := httptest.NewRequest("GET", "/api/v1/models", nil)
	e.h.ServeHTTP(httptest.NewRecorder(), r)
	if w := e.post("/api/v1/chat/completions", `{"model":"x/y","models":["a/b"]}`); w.Code != 400 {
		t.Fatalf("fallback list: %d", w.Code)
	}
	if rc, _ := ReadRunCost(e.file, "run-1"); rc.Calls != 0 {
		t.Fatalf("an OpenRouter refusal or a GET bills nothing: %+v", rc)
	}
}

func TestMeterWithoutRunFileStillRecords(t *testing.T) {
	var calls atomic.Int32
	e := newMeterEnv(t, fakeOpenRouter(t, "0.004", &calls).URL)
	if w := e.post("/api/v1/chat/completions", prompt+`}`); w.Code != 200 {
		t.Fatalf("%d", w.Code)
	}
	if rc, ok := ReadRunCost(e.file, ""); !ok || rc.Calls != 1 {
		t.Fatalf("%+v", rc)
	}
}
