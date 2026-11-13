package remote

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"p0ssess/internal/pme/paths"
)

func serve(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	t.Setenv("P0SSESS_CLOUD_URL", srv.URL+"/")
	home := t.TempDir()
	t.Setenv("P0SSESS_HOME", home)
	return New(paths.Default())
}

func TestInstallTokenAndBearer(t *testing.T) {
	var got []http.Header
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Clone())
		w.Header().Set("P0ssess-Plan", "free")
		w.Write([]byte(`{"v":1,"use_case":{"id":"x"},"stakes":2,"pick":0,"options":[]}`))
	})
	Version = "1.0.0"
	if _, err := c.Recommend(context.Background(), Request{UseCase: "x"}); err != nil {
		t.Fatal(err)
	}
	h := got[0]
	tok := h.Get("P0ssess-Install")
	if !installRe.MatchString(tok) || h.Get("Authorization") != "" || h.Get("User-Agent") != "p0ssess/1.0.0" {
		t.Fatalf("headers %v", h)
	}
	st, err := os.Stat(InstallTokenFile(c.Dirs))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("install token file: %v %v", st, err)
	}
	c2 := New(c.Dirs)
	if again, _ := c2.InstallToken(); again != tok {
		t.Error("the install token must be generated once")
	}

	p := filepath.Join(c.Dirs.Config, "secrets", "p0ssess_token")
	os.MkdirAll(filepath.Dir(p), 0o700)
	os.WriteFile(p, []byte("p0d_abc\n"), 0o600)
	c.Recommend(context.Background(), Request{UseCase: "x"})
	if h := got[1]; h.Get("Authorization") != "Bearer p0d_abc" || h.Get("P0ssess-Install") != "" {
		t.Errorf("linked headers %v", h)
	}
	if !c.Linked() {
		t.Error("Linked")
	}
}

func TestRequestBodyIsExactlyTheContract(t *testing.T) {
	var body map[string]any
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(`{"v":1,"use_case":{"id":"x"},"stakes":2,"pick":0,"options":[]}`))
	})
	c.Recommend(context.Background(), Request{UseCase: "x", Stakes: 2, Lambda: 1})
	for _, k := range []string{"v", "use_case", "stakes", "lambda", "rules", "lists", "privacy", "history"} {
		if _, ok := body[k]; !ok {
			t.Errorf("missing %s", k)
		}
	}
	if body["rules"] == nil || body["history"] == nil {
		t.Error("rules and history are lists, never null")
	}
	if _, ok := body["tuning"]; ok {
		t.Error("tuning is sent only when set")
	}
}

func TestErrors(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/recommend":
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(429)
			w.Write([]byte(`{"error":"rate_limited"}`))
		case "/v1/outcomes":
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(429)
		case "/v1/table":
			if r.Header.Get("If-None-Match") == `"t1"` {
				w.WriteHeader(304)
				return
			}
			w.Header().Set("ETag", `"t1"`)
			w.Write([]byte(`{"v":1}`))
		}
	})
	_, err := c.Recommend(context.Background(), Request{UseCase: "x"})
	if e, ok := err.(*Error); !ok || e.Status != 429 || e.Code != "rate_limited" || e.RetryAfter != 30*time.Second {
		t.Errorf("recommend: %v", err)
	}
	if st, ra, err := c.PostOutcomes(context.Background(), []byte(`{"v":1,"records":[]}`)); st != 429 || ra != time.Minute || err == nil {
		t.Errorf("outcomes: %d %v %v", st, ra, err)
	}
	env, etag, same, err := c.FetchTable(context.Background(), "")
	if err != nil || same || etag != `"t1"` || string(env) != `{"v":1}` {
		t.Errorf("table: %s %s %v %v", env, etag, same, err)
	}
	if _, _, same, err := c.FetchTable(context.Background(), `"t1"`); !same || err != nil {
		t.Errorf("304: %v %v", same, err)
	}
}

func TestBadJSONAndTimeout(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		{
			w.Write([]byte(`{"v":1,`))
		}
	})
	if _, err := c.Recommend(context.Background(), Request{UseCase: "x"}); err == nil {
		t.Error("bad JSON accepted")
	}
	slow := serve(t, func(w http.ResponseWriter, r *http.Request) { time.Sleep(300 * time.Millisecond) })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := slow.Recommend(ctx, Request{UseCase: "x"}); err == nil {
		t.Error("no timeout")
	}
}

func TestNoNetworkInTests(t *testing.T) {
	t.Setenv("P0SSESS_CLOUD_URL", "")
	t.Setenv("P0SSESS_HOME", t.TempDir())
	if _, err := New(paths.Default()).Recommend(context.Background(), Request{UseCase: "x"}); err != errTestNetwork {
		t.Errorf("want the test guard, got %v", err)
	}
}
