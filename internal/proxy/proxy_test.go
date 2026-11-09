package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestForceNoDataCollection(t *testing.T) {
	cases := map[string]string{
		`{"model":"x/y","messages":[]}`:                                        "deny",
		`{"model":"x/y","provider":{"data_collection":"allow","order":["a"]}}`: "deny",
		`{"models":["x/y"]}`:                                                   "deny",
	}
	for in, want := range cases {
		var m struct {
			Provider map[string]any `json:"provider"`
		}
		if err := json.Unmarshal(ForceNoDataCollection([]byte(in)), &m); err != nil {
			t.Fatal(err)
		}
		if m.Provider["data_collection"] != want {
			t.Errorf("%s: data_collection = %v", in, m.Provider["data_collection"])
		}
	}
	var m struct {
		Provider map[string]any `json:"provider"`
	}
	json.Unmarshal(ForceNoDataCollection([]byte(`{"model":"a","provider":{"order":["b"]}}`)), &m)
	if o, _ := m.Provider["order"].([]any); len(o) != 1 {
		t.Errorf("other provider fields lost: %v", m.Provider)
	}
	for _, in := range []string{`not json`, `{"query":"no model"}`, `[1,2]`} {
		if got := string(ForceNoDataCollection([]byte(in))); got != in {
			t.Errorf("%q changed to %q", in, got)
		}
	}
}

func TestHandlerInjectsKeyAndPolicy(t *testing.T) {
	logRequests = false
	var gotAuth, gotBody, gotPath string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer up.Close()
	h := httptest.NewServer(Handler(up.URL, "real-key", false))
	defer h.Close()

	req, _ := http.NewRequest("POST", h.URL+"/api/v1/chat/completions", strings.NewReader(`{"model":"a/b","messages":[]}`))
	req.Header.Set("Authorization", "Bearer placeholder")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if gotAuth != "Bearer real-key" {
		t.Errorf("upstream Authorization = %q", gotAuth)
	}
	if gotPath != "/api/v1/chat/completions" {
		t.Errorf("upstream path = %q", gotPath)
	}
	if !strings.Contains(gotBody, `"data_collection":"deny"`) {
		t.Errorf("body = %s", gotBody)
	}

	for _, p := range []string{"/", "/api/v2/x", "/admin"} {
		r, _ := http.Get(h.URL + p)
		r.Body.Close()
		if r.StatusCode != 404 {
			t.Errorf("%s: status %d, want 404", p, r.StatusCode)
		}
	}
	req, _ = http.NewRequest("POST", h.URL+"/api/v1/chat/completions", strings.NewReader("x"))
	req.Header.Set("Content-Encoding", "gzip")
	r, _ := http.DefaultClient.Do(req)
	r.Body.Close()
	if r.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("compressed body: status %d", r.StatusCode)
	}
}

func TestForceZDR(t *testing.T) {
	var m struct {
		Provider map[string]any `json:"provider"`
	}
	if err := json.Unmarshal(ForceZDR([]byte(`{"model":"a","provider":{"zdr":false}}`)), &m); err != nil {
		t.Fatal(err)
	}
	if m.Provider["zdr"] != true || m.Provider["data_collection"] != "deny" {
		t.Errorf("provider = %v", m.Provider)
	}
}
