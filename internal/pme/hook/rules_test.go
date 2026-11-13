package hook

import (
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"p0ssess/internal/account"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/pme/table"
	"p0ssess/internal/pme/table/tabletest"
	"p0ssess/internal/rules"
)

func TestWorkMatchesCatalog(t *testing.T) {
	tb := table.Open(t.TempDir()).Table()
	if tb.Empty() {
		t.Fatal("the built-in route table did not load")
	}
	seen := map[string]bool{}
	for _, w := range catalog {
		if seen[w.id] {
			t.Errorf("%s listed twice", w.id)
		}
		seen[w.id] = true
		uc := tb.UseCase(w.id)
		if uc == nil {
			t.Errorf("%s is not in the route table", w.id)
			continue
		}
		if uc.Kind != w.kind {
			t.Errorf("%s: kind %q, table says %q", w.id, w.kind, uc.Kind)
		}
		if w.keep != (uc.Path == "keep_on_claude") {
			t.Errorf("%s: keep %v, table path %q", w.id, w.keep, uc.Path)
		}
		if w.label == "" || len(w.pats) == 0 {
			t.Errorf("%s: needs a label and a pattern", w.id)
		}
	}
	for _, id := range unmatched {
		if seen[id] {
			t.Errorf("%s is both matched and unmatched", id)
		}
		seen[id] = true
	}
	for _, uc := range tb.UseCases {
		if !seen[uc.ID] {
			t.Errorf("use case %s is neither in the hook's catalog nor in unmatched", uc.ID)
		}
	}
}

// Requests worded the way a team hands Claude its daily batch, and the use case each must be seen as.
func TestMatchWork(t *testing.T) {
	cases := []struct{ prompt, want string }{
		{"Our support inbox export is in tickets.jsonl. Please triage every ticket according to POLICY.md and write triage.json.", "bulk-classify-tag"},
		{"The vendor invoices in invoices/ need to go into our AP sheet. Please extract vendor, number, dates and totals from every file.", "invoice-receipt-extract"},
		{"Each log in logs/ has exactly one root cause. Work out the category and the service for every log.", "error-triage"},
		{"The German and French locale files are behind; add every key missing from locales/de.json and locales/fr.json.", "i18n-catalog-sync"},
		{"Go through every file in review/ and check it for a real bug; write review.json.", "code-review-diff"},
		{"Please write unit tests for every module in src/, one file per module under tests/.", "test-generation"},
		{"Procurement needs the key commercial terms from the vendor agreements in contracts/; write terms.json.", "data-extract-structured"},
		{"Normalize every listing in listings.jsonl into our catalog format following SCHEMA.md.", "data-transform-format"},
		{"The recordings are in voicemails/ (vm-01.mp3 and so on); triage each one by ROUTING.md.", "audio-transcribe"},
		{"summarize demo.mp4 for the changelog", "video-understand"},
	}
	for _, tc := range cases {
		var ids []string
		for _, w := range matchWork(tc.prompt) {
			ids = append(ids, w.id)
		}
		if !contains(ids, tc.want) {
			t.Errorf("%.50q: matched %v, want %s", tc.prompt, ids, tc.want)
		}
	}
	for _, p := range []string{"what does this function do?", "thanks, that looks good", "rename foo to bar in parse.go"} {
		for _, w := range matchWork(p) {
			if w.id == "test-generation" || w.id == "error-triage" || w.id == "bulk-classify-tag" {
				t.Errorf("%q should not look like %s", p, w.id)
			}
		}
	}
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func writeRules(t *testing.T, path, body string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o700)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

const sessionOnly = `[[rule]]
id = "r-0000aaaa"
kind = "only"
models = ["deepseek/deepseek-v4.1-flash-1017"]
use_cases = ["test-generation"]
created = 2026-11-13T07:00:00Z
note = "always send unit-test writing to deepseek flash"
`

func TestSessionRuleHint(t *testing.T) {
	d, proj := dirs(t)
	now := time.Now()
	writeRules(t, d.SessionRulesFile("s1"), sessionOnly)
	in := Input{SessionID: "s1", Cwd: proj, Prompt: "Please write unit tests for every module in src/zebra_pkg, using unittest."}
	got := hint(in, d, now, true)
	for _, want := range []string{`subagent_type "p0ssess:p0ssess"`, "model deepseek/deepseek-v4.1-flash-1017, use case test-generation",
		"the user's p0ssess rule", "Do not do it yourself"} {
		if !strings.Contains(got, want) {
			t.Errorf("hint lacks %q: %q", want, got)
		}
	}
	if strings.Contains(got, "team rule") || strings.Contains(got, "zebra") || strings.Contains(got, "\n") {
		t.Errorf("hint must be one line, name no team and echo no prompt text: %q", got)
	}
	for _, p := range []string{"what does this function do?", "fix the off-by-one in parse.go", "explain the build cache"} {
		if got := hint(Input{SessionID: "s1", Cwd: proj, Prompt: p}, d, now, true); strings.Contains(got, "use case test-generation") {
			t.Errorf("%q: unexpected rule hint %q", p, got)
		}
	}
	if got := hint(Input{SessionID: "s1", Cwd: proj, Prompt: "fix the off-by-one in parse.go"}, d, now, true); got != "" {
		t.Errorf("non-matching prompt: %q", got)
	}
	// Another session does not have the rule.
	if got := hint(Input{SessionID: "s2", Cwd: proj, Prompt: in.Prompt}, d, now, true); strings.Contains(got, "use case test-generation") {
		t.Errorf("session rule leaked into another session: %q", got)
	}
	if got := hint(in, d, now, false); !strings.Contains(got, "no OpenRouter key") {
		t.Errorf("no key: %q", got)
	}
	// Keeping work on Claude beats the rule.
	writeRules(t, d.SessionRulesFile("s1"), sessionOnly+`
[[rule]]
id = "r-0000bbbb"
kind = "forbid"
target = "delegation"
created = 2026-11-13T07:01:00Z
`)
	if got := hint(in, d, now, true); strings.Contains(got, "use case test-generation") {
		t.Errorf("forbid delegation should silence the rule: %q", got)
	}
	os.WriteFile(filepath.Join(proj, ".p0ssess-private"), nil, 0o600)
	writeRules(t, d.SessionRulesFile("s1"), sessionOnly)
	if got := Hint(in, d, now); got != "" {
		t.Errorf("private repo: %q", got)
	}
}

func TestRuleHintThroughRun(t *testing.T) {
	d, proj := dirs(t)
	writeRules(t, d.RulesFile(), sessionOnly)
	cfg := filepath.Join(os.Getenv("P0SSESS_HOME"), "config.json")
	key := filepath.Join(os.Getenv("P0SSESS_HOME"), "key")
	os.WriteFile(key, []byte("sk-test"), 0o600)
	b, _ := json.Marshal(map[string]string{"openrouter_key_file": key})
	os.WriteFile(cfg, b, 0o600)
	raw, _ := json.Marshal(Input{SessionID: "s9", Cwd: proj, Prompt: "add tests for the parser module", Event: "UserPromptSubmit"})
	if got := Run(raw, d, time.Now()); !strings.Contains(got, "use case test-generation") {
		t.Errorf("global rule through Run: %q", got)
	}
}

// joinTeam links the install to a team whose cached, signed policy holds rs.
func joinTeam(t *testing.T, d paths.Dirs, rs ...map[string]any) {
	t.Helper()
	pub, priv := tabletest.Key()
	old := teamKeys
	teamKeys = func() map[string]ed25519.PublicKey { return map[string]ed25519.PublicKey{table.KeyID(pub): pub} }
	t.Cleanup(func() { teamKeys = old })
	if err := account.SaveToken(d, "p0d_test"); err != nil {
		t.Fatal(err)
	}
	st, _ := json.Marshal(map[string]any{"plan": "team", "status": "active", "account_id": "acc_1", "org_id": "org_1"})
	os.MkdirAll(d.State, 0o700)
	os.WriteFile(filepath.Join(d.State, "account.json"), st, 0o600)
	p := map[string]any{"v": 1, "type": "team-policy", "org_id": "org_1", "org_name": "Acme", "account_id": "acc_1",
		"role": "member", "issued": "2026-11-13T07:00:00Z", "rules": rs, "report_spend": true}
	b, _ := json.Marshal(p)
	os.WriteFile(filepath.Join(d.State, "team-policy.json"), tabletest.Seal(priv, b), 0o600)
}

func TestTeamRuleHint(t *testing.T) {
	d, proj := dirs(t)
	now := time.Now()
	joinTeam(t, d, map[string]any{"id": "t-0000aaaa", "kind": "only", "models": []string{"z-ai/glm-5.3-flash"},
		"use_cases": []string{"error-triage"}, "note": "always send log triage to glm flash"})
	in := Input{SessionID: "s1", Cwd: proj, Prompt: "Several services misbehaved overnight. For every log in logs/ find the root cause and the service."}
	got := hint(in, d, now, true)
	for _, want := range []string{"a team rule", "model z-ai/glm-5.3-flash, use case error-triage", `subagent_type "p0ssess:p0ssess"`} {
		if !strings.Contains(got, want) {
			t.Errorf("hint lacks %q: %q", want, got)
		}
	}
	if strings.Contains(got, "overnight") || strings.Contains(got, "\n") {
		t.Errorf("hint must be one line and echo no prompt text: %q", got)
	}
	for _, p := range []string{"rename the variable foo to bar", "what does this function do?", "write a haiku about autumn"} {
		if got := hint(Input{SessionID: "s1", Cwd: proj, Prompt: p}, d, now, true); strings.Contains(got, "team rule") {
			t.Errorf("%q: unexpected team rule hint %q", p, got)
		}
	}
	// A policy that does not verify is not in force.
	teamKeys = func() map[string]ed25519.PublicKey { return table.PinnedKeys() }
	if got := hint(in, d, now, true); strings.Contains(got, "team rule") {
		t.Errorf("unverified policy produced a hint: %q", got)
	}
}

func TestKindRuleHint(t *testing.T) {
	d, proj := dirs(t)
	now := time.Now()
	writeRules(t, d.RulesFile(), `[[rule]]
id = "r-0000cccc"
kind = "mix"
models = ["z-ai/glm-5.3-flash", "deepseek/deepseek-v4.1-flash-1017"]
weights = [1, 1]
kinds = ["review", "text"]
created = 2026-11-13T07:00:00Z
`)
	got := hint(Input{Cwd: proj, Prompt: "please review this diff before I merge it"}, d, now, true)
	if !strings.Contains(got, "use case code-review-diff") || !(strings.Contains(got, "model z-ai/glm-5.3-flash,") || strings.Contains(got, "model deepseek/deepseek-v4.1-flash-1017,")) {
		t.Errorf("kind rule: %q", got)
	}
	// Support replies stay on Claude by default; a kind-wide rule does not pull them out.
	if got := hint(Input{Cwd: proj, Prompt: "draft replies to these three support tickets"}, d, now, true); strings.Contains(got, "use case") {
		t.Errorf("kind rule pulled out keep-on-Claude work: %q", got)
	}
	// A rule over all work only narrows models; it never triggers by itself.
	writeRules(t, d.RulesFile(), `[[rule]]
id = "r-0000dddd"
kind = "only"
models = ["z-ai/glm-5.3-flash"]
created = 2026-11-13T07:00:00Z
`)
	if got := hint(Input{Cwd: proj, Prompt: "please review this diff before I merge it"}, d, now, true); strings.Contains(got, "use case") {
		t.Errorf("an all-work rule triggered a hint: %q", got)
	}
}

func TestRuleRouteNarrowing(t *testing.T) {
	now := time.Now()
	set := rules.Set{Rules: []rules.Rule{
		{ID: "a", Kind: "only", Models: []string{"x/a", "x/b"}, UseCases: []string{"test-generation"}, Scope: rules.ScopeGlobal, Created: now},
		{ID: "t", Kind: "only", Models: []string{"x/b"}, UseCases: []string{"test-generation"}, Scope: rules.ScopeTeam, Created: now},
	}}
	rt := ruleRoute("write tests for util.go", set, now)
	if rt == nil || len(rt.models) != 1 || rt.models[0] != "x/b" || !rt.team {
		t.Fatalf("team narrowing: %+v", rt)
	}
	set.Rules = append(set.Rules, rules.Rule{ID: "f", Kind: "forbid", Target: "model", Models: []string{"x/b"}, Scope: rules.ScopeSession, Created: now})
	if rt := ruleRoute("write tests for util.go", set, now); rt != nil {
		t.Fatalf("forbidden model still routed: %+v", rt)
	}
}
