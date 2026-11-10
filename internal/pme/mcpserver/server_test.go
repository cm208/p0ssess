package mcpserver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"p0ssess/internal/pme/engine"
	"p0ssess/internal/pme/paths"
)

// run answers each tools/call line through Call, in the shape the MCP server returns.
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
	s := &Server{E: e}
	var res []map[string]any
	for _, line := range lines {
		var req struct {
			Params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"params"`
		}
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			t.Fatal(err)
		}
		out, err := s.Call(req.Params.Name, req.Params.Arguments)
		r := map[string]any{}
		if err != nil {
			r["isError"] = true
			r["content"] = []any{map[string]any{"text": err.Error()}}
		} else {
			b, _ := json.MarshalIndent(out, "", " ")
			r["content"] = []any{map[string]any{"text": string(b)}}
		}
		res = append(res, map[string]any{"result": r})
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

func TestToolList(t *testing.T) {
	names := map[string]bool{}
	for _, tl := range Tools() {
		names[tl["name"].(string)] = true
	}
	for _, n := range []string{"recommend", "budget_status", "rules_list", "rules_add", "rules_remove", "approvals_add", "approvals_list", "history_record_outcome", "tendencies", "settings_set"} {
		if !names[n] {
			t.Errorf("missing tool %s", n)
		}
	}
	if _, err := (&Server{}).Call("nope", nil); err == nil {
		t.Error("unknown tool must be an error")
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
	var rec CompactRecommendation
	if err := json.Unmarshal([]byte(text(t, res[0])), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.OfferID == "" || len(rec.Options) == 0 || !strings.HasPrefix(rec.Options[0], "direct ") || len(text(t, res[0])) > 2000 {
		t.Fatalf("recommend = %+v (%d bytes)", rec, len(text(t, res[0])))
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
