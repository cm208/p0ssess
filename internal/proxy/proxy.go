// Package proxy is the OpenRouter forwarder that runs in its own container inside the jail pod. It holds
// the API key so the OpenCode container (and anything a model runs there) never sees it, and it forces
// provider.data_collection = "deny" on every model request, plus provider.zdr = true in zero-retention mode.
package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const Upstream = "https://openrouter.ai"

const maxBody = 64 << 20

// Handler returns the forwarding handler. upstream is normally Upstream; tests pass a local server. zdr
// requires zero-retention endpoints on every model request.
func Handler(upstream, key string, zdr bool) http.Handler {
	u, err := url.Parse(upstream)
	if err != nil {
		panic(err)
	}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(u)
			pr.Out.Host = u.Host
			pr.Out.Header.Set("Authorization", "Bearer "+key)
			pr.Out.Header.Del("Cookie")
		},
		FlushInterval: -1,
		ErrorLog:      log.New(io.Discard, "", 0),
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/") {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if enc := r.Header.Get("Content-Encoding"); enc != "" && enc != "identity" {
			http.Error(w, "compressed request bodies are not supported", http.StatusUnsupportedMediaType)
			return
		}
		start := time.Now()
		model := ""
		if r.Method == http.MethodPost && r.Body != nil {
			body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
			if err != nil || len(body) > maxBody {
				http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
				return
			}
			body = forceProvider(body, zdr)
			model = modelOf(body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
			r.Header.Set("Content-Length", strconv.Itoa(len(body)))
		}
		sw := &statusWriter{ResponseWriter: w, status: 200}
		rp.ServeHTTP(sw, r)
		if logRequests {
			log.Printf("%s %s model=%q status=%d %s", r.Method, r.URL.Path, model, sw.status, time.Since(start).Round(time.Millisecond))
		}
	})
}

var logRequests = true

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(c int) { s.status = c; s.ResponseWriter.WriteHeader(c) }

func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func modelOf(body []byte) string {
	var m struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &m)
	return m.Model
}

// ForceNoDataCollection sets provider.data_collection = "deny" in a JSON request that names a model.
// Bodies that are not JSON objects with a "model" field pass through unchanged.
func ForceNoDataCollection(body []byte) []byte { return forceProvider(body, false) }

// ForceZDR also sets provider.zdr = true.
func ForceZDR(body []byte) []byte { return forceProvider(body, true) }

func forceProvider(body []byte, zdr bool) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return body
	}
	if _, ok := m["model"]; !ok {
		if _, ok := m["models"]; !ok {
			return body
		}
	}
	prov := map[string]json.RawMessage{}
	if raw, ok := m["provider"]; ok {
		_ = json.Unmarshal(raw, &prov)
	}
	prov["data_collection"] = json.RawMessage(`"deny"`)
	if zdr {
		prov["zdr"] = json.RawMessage(`true`)
	}
	pb, _ := json.Marshal(prov)
	m["provider"] = pb
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// WithDeadline refuses every request once the time in deadlineFile (Unix seconds, written by the relay)
// has passed, and when the file cannot be read. The relay moves the deadline forward for each prompt, so a
// run whose relay process died stops spending at its maximum runtime even though nothing interrupts it.
func WithDeadline(h http.Handler, deadlineFile string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !beforeDeadline(deadlineFile, time.Now()) {
			http.Error(w, "this jail's deadline has passed", http.StatusServiceUnavailable)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func beforeDeadline(file string, now time.Time) bool {
	b, err := os.ReadFile(file)
	if err != nil {
		return false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return err == nil && now.Unix() < n
}

// Serve listens on addr until the process exits. With deadlineFile set, requests stop at its deadline.
func Serve(addr, key string, zdr bool, deadlineFile string) error {
	return ServeOptions(Options{Addr: addr, Key: key, ZDR: zdr, DeadlineFile: deadlineFile})
}
