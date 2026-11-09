package mcpserver

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"p0ssess/internal/pme/engine"
	"p0ssess/internal/pme/paths"
)

func run(t *testing.T, lines ...string) []map[string]any {
	t.Helper()
	home := t.TempDir()
	t.Setenv("P0SSESS_HOME", home)
	proj := filepath.Join(home, "proj")
	os.MkdirAll(filepath.Join(proj, ".git"), 0o755)
	t.Setenv("CLAUDE_PROJECT_DIR", proj)
	e, err := engine.Load(paths.Default())
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	s := &Server{E: e}
	if err := s.Serve(strings.NewReader(strings.Join(lines, "\n")+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	var res []map[string]any
	sc := bufio.NewScanner(strings.NewReader(out.String()))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("bad response %q", sc.Text())
		}
		res = append(res, m)
	}
	return res
}

func text(t *testing.T, r map[string]any) string {
	t.Helper()
	res, _ := r["result"].(map[string]any)
	if res == nil {
		t.Fatalf("no result: %v", r)
	}
	c := res["content"].([]any)[0].(map[string]any)
	return c["text"].(string)
}

func call(id int, name, args string) string {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": json.RawMessage(args)}})
	return string(b)
}

func TestProtocol(t *testing.T) {
	res := run(t,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"nope"}`,
		`garbage`,
	)
	if len(res) != 4 {
		t.Fatalf("want 4 responses, got %d", len(res))
	}
	init := res[0]["result"].(map[string]any)
	if init["protocolVersion"] != "2025-06-18" || init["instructions"] == "" {
		t.Errorf("initialize = %v", init)
	}
	names := map[string]bool{}
	for _, tl := range res[1]["result"].(map[string]any)["tools"].([]any) {
		names[tl.(map[string]any)["name"].(string)] = true
	}
	for _, n := range []string{"recommend", "budget_status", "rules_list", "rules_add", "rules_remove", "approvals_add", "approvals_list", "history_record_outcome", "tendencies", "settings_set"} {
		if !names[n] {
			t.Errorf("missing tool %s", n)
		}
	}
	if res[2]["error"] == nil || res[3]["error"] == nil {
		t.Error("unknown method and parse errors must return errors")
	}
}

func TestToolsRoundTrip(t *testing.T) {
	res := run(t,
		call(1, "recommend", `{"use_case":"video-understand","stakes":1}`),
		call(2, "rules_add", `{"kind":"forbid","target":"model","models":["z-ai/glm-5.3-flash"],"kinds":["media"],"scope":"project","dry_run":true}`),
		call(3, "rules_list", `{}`),
		call(4, "approvals_add", `{"kinds":["media"],"max_per_run_usd":0.20,"max_total_usd":5,"period":"day","scope":"global"}`),
		call(5, "approvals_list", `{}`),
		call(6, "budget_status", `{}`),
		call(7, "settings_set", `{"key":"monthly_usd","value":"25"}`),
		call(8, "rules_add", `{"kind":"forbid","target":"robots"}`),
	)
	var rec engine.Recommendation
	if err := json.Unmarshal([]byte(text(t, res[0])), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.OfferID == "" || rec.Pick == nil || rec.ClaudeFeasible {
		t.Fatalf("recommend = %+v", rec)
	}
	if dry := text(t, res[1]); !strings.Contains(dry, "never use glm-5.3-flash for media work (project)") || !strings.Contains(dry, "not saved") {
		t.Errorf("dry run = %s", dry)
	}
	if !strings.Contains(text(t, res[2]), `"rules": null`) {
		t.Errorf("dry run must not save: %s", text(t, res[2]))
	}
	if !strings.Contains(text(t, res[3]), "auto-approve for media work up to $0.20 each, $5.00 total a day (global)") {
		t.Errorf("approval = %s", text(t, res[3]))
	}
	if !strings.Contains(text(t, res[4]), `"count": 0`) {
		t.Errorf("approvals_list = %s", text(t, res[4]))
	}
	if !strings.Contains(text(t, res[5]), `"monthly_usd": 10`) {
		t.Errorf("budget_status = %s", text(t, res[5]))
	}
	if !strings.Contains(text(t, res[6]), "monthly_usd = 25") {
		t.Errorf("settings_set = %s", text(t, res[6]))
	}
	if r := res[7]["result"].(map[string]any); r["isError"] != true {
		t.Errorf("invalid rule should be a tool error: %v", r)
	}
}

func TestOutcomeAndTendencies(t *testing.T) {
	var lines []string
	for i := 0; i < 3; i++ {
		lines = append(lines, call(10+i, "history_record_outcome",
			`{"model":"google/gemini-3.8-flash","outcome":"accepted","cash_usd":0.05,"use_case":"video-understand"}`))
	}
	lines = append(lines, call(20, "tendencies", `{}`),
		call(21, "history_record_outcome", `{"model":"x","outcome":"great"}`),
		call(22, "history_record_outcome", `{"model":"x","outcome":"accepted","offer_id":"o-missing"}`))
	res := run(t, lines...)
	if !strings.Contains(text(t, res[3]), "accepts gemini-3.8-flash for media 3/3") {
		t.Errorf("tendencies = %s", text(t, res[3]))
	}
	for _, i := range []int{4, 5} {
		if r := res[i]["result"].(map[string]any); r["isError"] != true {
			t.Errorf("response %d should be a tool error", i)
		}
	}
}
