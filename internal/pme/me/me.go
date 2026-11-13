// Package me serves the local /p0ssess-me page: spend against limits, Claude
// usage, tendencies, recent delegations, rules and editable settings. It binds
// to 127.0.0.1 only, on a random port, behind a random path token, loads no
// external assets and works offline.
package me

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"p0ssess/internal/outcomes"
	"strconv"
	"strings"
	"time"

	"p0ssess"
	"p0ssess/internal/history"
	"p0ssess/internal/pme/local"
	"p0ssess/internal/pme/statusline"
	"p0ssess/internal/rules"
)

//go:embed web
var web embed.FS

type Server struct {
	E     *local.Engine
	Scope local.Scope
	Token string
	Addr  string // host:port actually bound
	srv   *http.Server
	ln    net.Listener
}

// Start listens on 127.0.0.1:port (0 picks a free port).
func Start(e *local.Engine, scope local.Scope, port int) (*Server, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	s := &Server{E: e, Scope: scope, Token: hex.EncodeToString(b[:]), Addr: ln.Addr().String(), ln: ln}
	s.srv = &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go s.srv.Serve(ln)
	return s, nil
}

func (s *Server) URL() string { return "http://" + s.Addr + "/t/" + s.Token + "/" }

func (s *Server) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.srv.Shutdown(ctx)
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	prefix := "/t/" + s.Token
	static, _ := fs.Sub(web, "web")
	file := func(name, ctype string, src fs.FS) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			b, err := fs.ReadFile(src, name)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", ctype)
			w.Write(b)
		}
	}
	mux.HandleFunc("GET "+prefix+"/{$}", file("index.html", "text/html; charset=utf-8", static))
	mux.HandleFunc("GET "+prefix+"/app.js", file("app.js", "text/javascript; charset=utf-8", static))
	mux.HandleFunc("GET "+prefix+"/app.css", file("app.css", "text/css; charset=utf-8", static))
	// The ghost comes from the one shipped asset, so a new mark in assets/ shows up here unchanged.
	mux.HandleFunc("GET "+prefix+"/ghost.svg", file("assets/ghost.svg", "image/svg+xml", p0ssess.Data))
	mux.HandleFunc("GET "+prefix+"/fonts/obel-mono.woff2", file("fonts/obel-mono.woff2", "font/woff2", static))
	mux.HandleFunc("GET "+prefix+"/fonts/plex-sans-400.woff2", file("fonts/plex-sans-400.woff2", "font/woff2", static))
	mux.HandleFunc("GET "+prefix+"/api/state", s.state)
	mux.HandleFunc("POST "+prefix+"/api/settings", s.post(s.settings))
	mux.HandleFunc("POST "+prefix+"/api/rules/remove", s.post(s.removeRule))
	mux.HandleFunc("POST "+prefix+"/api/rules/add", s.post(s.addRule))
	return s.guard(mux)
}

// guard rejects other hosts (DNS rebinding) and sets a strict CSP.
func (s *Server) guard(next http.Handler) http.Handler {
	_, port, _ := net.SplitHostPort(s.Addr)
	ok := map[string]bool{"127.0.0.1:" + port: true, "localhost:" + port: true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ok[r.Host] {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; font-src 'self'; img-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) post(fn func(json.RawMessage) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := r.Header.Get("X-P0ssess-Token")
		if subtle.ConstantTimeCompare([]byte(tok), []byte(s.Token)) != 1 || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		out, err := fn(body)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

type window struct {
	Name      string  `json:"name"`
	Used      float64 `json:"used"`
	Projected float64 `json:"projected"`
	ResetsAt  int64   `json:"resets_at"`
}

// outcomesView is the pending batch of anonymous outcome records, one JSON line each.
type outcomesView struct {
	Send    bool     `json:"send"`
	Pending []string `json:"pending"`
}

type state struct {
	Status     *local.Status       `json:"status"`
	Windows    []window            `json:"windows"`
	ReadingAge int64               `json:"reading_age_seconds"`
	Learned    local.Learned       `json:"learned"`
	Runs       []history.Entry     `json:"runs"`
	Rules      []local.RuleView    `json:"rules"`
	Preset     string              `json:"lambda_preset"`
	Presets    []string            `json:"presets"`
	Statusline statusline.Settings `json:"statusline"`
	Outcomes   outcomesView        `json:"outcomes"`
	Token      string              `json:"token"`
	Now        time.Time           `json:"now"`
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	st, err := s.E.Status(s.Scope)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	cfg, _ := s.E.Config()
	out := state{Status: st, Learned: s.E.Learned(s.Scope), Preset: cfg.Lambda.Preset,
		Presets: []string{"list", "gentle", "balanced", "eager"}, Statusline: statusline.LoadSettings(s.E.Dirs.EngineFile()),
		Token: s.Token, Now: s.E.Now()}
	out.Rules, _ = s.E.RulesList(s.Scope, "")
	out.Outcomes = outcomesView{Send: s.E.OutcomesEnabled(), Pending: outcomes.Lines(s.E.PendingOutcomes())}
	if out.Outcomes.Pending == nil {
		out.Outcomes.Pending = []string{}
	}
	out.Runs = s.E.Ledger().Runs(20)
	if out.Runs == nil {
		out.Runs = []history.Entry{}
	}
	us := s.E.Usage()
	p := st.Lambda.Usage
	out.ReadingAge = p.AgeSec
	if us.RateLimits != nil && p.Known {
		if w := us.RateLimits.FiveHour; w != nil && w.UsedPercentage != nil && w.ResetsAt != nil {
			out.Windows = append(out.Windows, window{"5-hour", *w.UsedPercentage / 100, p.FiveHour, *w.ResetsAt})
		}
		if w := us.RateLimits.SevenDay; w != nil && w.UsedPercentage != nil && w.ResetsAt != nil {
			out.Windows = append(out.Windows, window{"weekly", *w.UsedPercentage / 100, p.SevenDay, *w.ResetsAt})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) settings(body json.RawMessage) (any, error) {
	var req struct {
		Changes map[string]string `json:"changes"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	var saved []string
	for _, k := range local.SettingKeys() {
		v, ok := req.Changes[k]
		if !ok {
			continue
		}
		msg, err := s.E.SetSetting(k, v)
		if err != nil {
			return map[string]any{"saved": saved}, err
		}
		saved = append(saved, msg)
	}
	for k := range req.Changes {
		if !contains(local.SettingKeys(), k) {
			return map[string]any{"saved": saved}, fmt.Errorf("unknown setting %q", k)
		}
	}
	return map[string]any{"saved": saved}, nil
}

func (s *Server) removeRule(body json.RawMessage) (any, error) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.ID == "" {
		return nil, errors.New("id required")
	}
	return s.E.RuleRemove(s.Scope, req.ID)
}

// addRule saves a rule the user picked on the page (from a suggestion).
func (s *Server) addRule(body json.RawMessage) (any, error) {
	var req struct {
		Rule rules.Rule `json:"rule"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	return s.E.RuleAdd(s.Scope, req.Rule, false, 0)
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
