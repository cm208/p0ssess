package me

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"p0ssess/internal/pme/engine"
	"p0ssess/internal/pme/paths"
)

func start(t *testing.T) (*Server, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("P0SSESS_HOME", home)
	proj := filepath.Join(home, "proj")
	os.MkdirAll(filepath.Join(proj, ".git"), 0o755)
	e, err := engine.Load(paths.Default())
	if err != nil {
		t.Fatal(err)
	}
	s, err := Start(e, engine.Scope{Cwd: proj}, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, proj
}

func get(t *testing.T, url string, host string) (int, string, http.Header) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if host != "" {
		req.Host = host
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, string(b), r.Header
}

func TestPageAndGuards(t *testing.T) {
	s, _ := start(t)
	if !strings.HasPrefix(s.Addr, "127.0.0.1:") {
		t.Fatalf("must bind loopback only, got %s", s.Addr)
	}
	code, body, h := get(t, s.URL(), "")
	if code != 200 || !strings.Contains(body, "<title>p0ssess</title>") {
		t.Fatalf("page: %d", code)
	}
	if !strings.Contains(h.Get("Content-Security-Policy"), "default-src 'none'") {
		t.Error("missing CSP")
	}
	for _, asset := range []string{"app.js", "app.css", "ghost.svg"} {
		if c, _, _ := get(t, s.URL()+asset, ""); c != 200 {
			t.Errorf("%s: %d", asset, c)
		}
	}
	if strings.Contains(body, "http://") || strings.Contains(body, "https://") {
		t.Error("page must not reference external assets")
	}
	if c, _, _ := get(t, "http://"+s.Addr+"/t/wrong/", ""); c != 404 {
		t.Errorf("wrong token: %d", c)
	}
	if c, _, _ := get(t, s.URL(), "evil.example:80"); c != 403 {
		t.Errorf("foreign Host must be refused: %d", c)
	}
}

func TestStateAndEdits(t *testing.T) {
	s, _ := start(t)
	_, body, _ := get(t, s.URL()+"api/state", "")
	var st map[string]any
	if err := json.Unmarshal([]byte(body), &st); err != nil {
		t.Fatal(err)
	}
	if st["status"] == nil || st["token"] != s.Token {
		t.Fatalf("state = %s", body)
	}
	post := func(path, body, token string) int {
		req, _ := http.NewRequest("POST", s.URL()+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("X-P0ssess-Token", token)
		}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r.StatusCode
	}
	if c := post("api/settings", `{"changes":{"monthly_usd":"20"}}`, ""); c != 403 {
		t.Errorf("POST without token: %d", c)
	}
	if c := post("api/settings", `{"changes":{"monthly_usd":"20","lambda.preset":"gentle"}}`, s.Token); c != 200 {
		t.Errorf("settings: %d", c)
	}
	b, _ := s.E.Budget()
	cfg, _ := s.E.Config()
	if b.MonthlyUSD != 20 || cfg.Lambda.Preset != "gentle" {
		t.Errorf("settings not saved: %v %v", b.MonthlyUSD, cfg.Lambda.Preset)
	}
	if c := post("api/settings", `{"changes":{"plan":"monthly"}}`, s.Token); c != 400 {
		t.Errorf("bad value: %d", c)
	}
	if c := post("api/rules/add", `{"rule":{"kind":"forbid","target":"opencode","scope":"project"}}`, s.Token); c != 200 {
		t.Fatalf("add rule: %d", c)
	}
	rs, _ := s.E.RulesList(s.Scope, "")
	if len(rs) != 1 {
		t.Fatalf("rules = %+v", rs)
	}
	if c := post("api/rules/remove", `{"id":"`+rs[0].ID+`"}`, s.Token); c != 200 {
		t.Errorf("remove: %d", c)
	}
}

// TestScriptMatchesPage catches a renamed id or form field without a browser.
func TestScriptMatchesPage(t *testing.T) {
	html, _ := web.ReadFile("web/index.html")
	js, _ := web.ReadFile("web/app.js")
	for _, m := range regexp.MustCompile(`\$\("#([a-z-]+)`).FindAllStringSubmatch(string(js), -1) {
		if !strings.Contains(string(html), `id="`+m[1]+`"`) {
			t.Errorf("app.js uses #%s, which index.html lacks", m[1])
		}
	}
	for _, m := range regexp.MustCompile(`<(?:input|select)[^>]*name="([a-z_.]+)"`).FindAllStringSubmatch(string(html), -1) {
		if !contains(engine.SettingKeys(), m[1]) {
			t.Errorf("form field %s is not a setting key", m[1])
		}
		if !strings.Contains(string(js), `"`+m[1]+`"`) && !strings.Contains(string(js), m[1]+":") {
			t.Errorf("form field %s is never filled by app.js", m[1])
		}
	}
	code := strings.ReplaceAll(string(js), `/ \((session|project|global)[^)]*\)$/`, "")
	if strings.Count(code, "(") != strings.Count(code, ")") || strings.Count(code, "{") != strings.Count(code, "}") || strings.Count(code, "[") != strings.Count(code, "]") {
		t.Error("app.js brackets do not balance")
	}
}
