package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"p0ssess/internal/account"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/pme/table"
	"p0ssess/internal/pme/table/tabletest"
	"p0ssess/internal/team"
)

// teamFake is the Team side of the cloud service, holding one org.
type teamFake struct {
	mu      sync.Mutex
	inOrg   bool
	role    string
	rules   []team.Rule
	puts    int
	bodies  map[string][]string // "METHOD path" -> request bodies
	failOn  map[string]string   // "METHOD path" -> error code
	spendQs []string
}

func (f *teamFake) view() map[string]any {
	v := map[string]any{"org": map[string]any{"id": "org_1", "name": "Acme", "seats": 5, "active": true}, "role": f.role,
		"account_id": "acc_1", "monthly_share_usd": 25, "rules": f.rules}
	if f.role == "owner" {
		v["seats_used"] = 2
		v["default_monthly_usd"] = 40
		v["members"] = []map[string]any{
			{"account_id": "acc_1", "email": "a@example.com", "role": "owner", "has_seat": true, "monthly_share_usd": 25},
			{"account_id": "acc_2", "github_login": "octo", "role": "member", "has_seat": true, "monthly_share_usd": nil}}
	}
	return v
}

func teamStub(t *testing.T) (*teamFake, paths.Dirs) {
	t.Helper()
	t.Setenv("P0SSESS_HOME", t.TempDir())
	pub, priv := tabletest.Key()
	old := teamKeys
	teamKeys = func() map[string]ed25519.PublicKey { return map[string]ed25519.PublicKey{table.KeyID(pub): pub} }
	t.Cleanup(func() { teamKeys = old })
	f := &teamFake{inOrg: true, role: "owner", rules: []team.Rule{}, bodies: map[string][]string{}, failOn: map[string]string{}}
	status := map[string]int{"not_in_team": 404, "not_owner": 403, "already_in_team": 409, "seats_full": 409,
		"invite_invalid": 400, "bad_request": 400, "not_entitled": 403}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		key := r.Method + " " + r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		f.bodies[key] = append(f.bodies[key], string(raw))
		if r.Header.Get("Authorization") != "Bearer p0d_abc" {
			w.WriteHeader(401)
			w.Write([]byte(`{"error":"invalid_token"}`))
			return
		}
		if code := f.failOn[key]; code != "" {
			w.WriteHeader(status[code])
			json.NewEncoder(w).Encode(map[string]string{"error": code})
			return
		}
		if !f.inOrg && strings.HasPrefix(r.URL.Path, "/v1/team") && key != "POST /v1/team" && key != "POST /v1/team/join" {
			w.WriteHeader(404)
			w.Write([]byte(`{"error":"not_in_team"}`))
			return
		}
		enc := json.NewEncoder(w)
		switch key {
		case "GET /v1/account":
			org := any(nil)
			if f.inOrg {
				org = "org_1"
			}
			enc.Encode(map[string]any{"account": map[string]any{"id": "acc_1"},
				"entitlements": map[string]any{"plan": "team", "status": "active", "org_id": org}})
		case "POST /v1/team":
			f.inOrg, f.role = true, "owner"
			w.WriteHeader(201)
			enc.Encode(f.view())
		case "POST /v1/team/join":
			f.inOrg, f.role = true, "member"
			enc.Encode(f.view())
		case "GET /v1/team":
			enc.Encode(f.view())
		case "DELETE /v1/team", "DELETE /v1/team/members/acc_1", "DELETE /v1/team/members/acc_2":
			if key != "DELETE /v1/team/members/acc_2" {
				f.inOrg = false
			}
			w.WriteHeader(204)
		case "POST /v1/team/invites":
			w.WriteHeader(201)
			enc.Encode(map[string]any{"id": "inv_1", "url": "https://example.com/team/join?invite=p0v_x",
				"expires_at": "2026-11-20T00:00:00Z", "max_uses": 3})
		case "PUT /v1/team/rules":
			var in struct {
				Rules []team.Rule `json:"rules"`
			}
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&in); err != nil {
				w.WriteHeader(400)
				enc.Encode(map[string]string{"error": "bad_request", "message": err.Error()})
				return
			}
			for i := range in.Rules {
				if in.Rules[i].ID == "" {
					f.puts++
					in.Rules[i].ID = fmt.Sprintf("t-%08x", f.puts)
				}
			}
			f.rules = in.Rules
			enc.Encode(map[string]any{"rules": f.rules})
		case "PUT /v1/team/budget":
			enc.Encode(f.view())
		case "GET /v1/team/policy":
			p, _ := json.Marshal(map[string]any{"v": 1, "type": "team-policy", "org_id": "org_1", "org_name": "Acme",
				"account_id": "acc_1", "role": f.role, "issued": "2026-11-13T07:00:00Z", "rules": f.rules,
				"monthly_share_usd": 25, "report_spend": true})
			w.Write(tabletest.Seal(priv, p))
		case "POST /v1/team/spend":
			w.WriteHeader(202)
			w.Write([]byte(`{"accepted":1}`))
		case "GET /v1/team/spend":
			f.spendQs = append(f.spendQs, r.URL.RawQuery)
			w.Write([]byte(`{"month":"2026-11","currency":"usd",
 "members":[{"account_id":"acc_1","email":"a@example.com","role":"owner","monthly_share_usd":25,"spent_usd":3.42,"runs":118}],
 "by_use_case":[{"use_case":"audio-transcribe","model":"openai/whisper-large-v3","route":"direct","runs":40,"spent_usd":0.49}],
 "by_day":[{"day":"2026-11-13","runs":12,"spent_usd":0.31}],"total_usd":3.42}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("P0SSESS_CLOUD_URL", srv.URL)
	d := paths.Default()
	account.SaveToken(d, "p0d_abc")
	return f, d
}

func runT(t *testing.T, args ...string) string {
	t.Helper()
	var b bytes.Buffer
	if err := runTeam(context.Background(), args, &b); err != nil {
		t.Fatalf("%v: %v\n%s", args, err, b.String())
	}
	return b.String()
}

func runTErr(t *testing.T, args ...string) error {
	t.Helper()
	return runTeam(context.Background(), args, io.Discard)
}

func contains2(t *testing.T, out string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in\n%s", w, out)
		}
	}
}

func TestTeamCreateInviteStatus(t *testing.T) {
	f, d := teamStub(t)
	f.inOrg = false
	out := runT(t, "create", "Acme", "Inc")
	contains2(t, out, "Created team Acme (org_1); you are its owner.", "/team/checkout")
	if f.bodies["POST /v1/team"][0] != `{"name":"Acme Inc"}` {
		t.Errorf("create body %s", f.bodies["POST /v1/team"])
	}
	if account.Current(d).OrgID != "org_1" || team.Load(d, teamKeys()) == nil {
		t.Error("create should refresh the account and fetch the policy")
	}

	out = runT(t, "invite", "--uses", "3", "--days", "5")
	contains2(t, out, "https://example.com/team/join?invite=p0v_x", "Good for 3 joins until Nov 20, 2026")
	var body map[string]int
	json.Unmarshal([]byte(f.bodies["POST /v1/team/invites"][0]), &body)
	if body["max_uses"] != 3 || body["expires_days"] != 5 || len(body) != 2 {
		t.Errorf("invite body %v", body)
	}
	runT(t, "invite")
	if f.bodies["POST /v1/team/invites"][1] != `{}` {
		t.Errorf("defaults are left to the server: %s", f.bodies["POST /v1/team/invites"][1])
	}

	out = runT(t, "status")
	contains2(t, out, "Team Acme (org_1); you are its owner", "Seats: 2 of 5 used", "Default monthly share: $40.00",
		"Your monthly share: $25.00", "a@example.com", "octo", "share default", "No team rules.")
}

func TestTeamJoinSyncLeave(t *testing.T) {
	f, d := teamStub(t)
	f.inOrg = false
	f.rules = []team.Rule{{ID: "t-0000000a", Kind: "forbid", Target: "model", Models: []string{"x-ai/grok-4.20"}, Note: "not here"}}
	out := runT(t, "join", "https://example.com/team/join?invite=p0v_x")
	contains2(t, out, "Joined Acme as a member.", "Monthly share: $25.00", "t-0000000a  never use grok-4.20  (not here)",
		"reported to the team")
	if f.bodies["POST /v1/team/join"][0] != `{"invite":"https://example.com/team/join?invite=p0v_x"}` {
		t.Errorf("join body %s", f.bodies["POST /v1/team/join"])
	}
	if p := team.Load(d, teamKeys()); p == nil || len(p.Rules) != 1 {
		t.Fatalf("policy not cached: %+v", p)
	}

	out = runT(t, "sync")
	contains2(t, out, "Team Acme: you are its member.", "never use grok-4.20")

	out = runT(t, "leave")
	contains2(t, out, "Left the team.")
	if len(f.bodies["DELETE /v1/team/members/acc_1"]) != 1 || team.Load(d, teamKeys()) != nil {
		t.Error("leave should remove the caller and forget the policy")
	}
	out = runT(t, "sync")
	contains2(t, out, "Not in a team")
	out = runT(t, "status")
	contains2(t, out, "Not in a team.")
}

func TestTeamRules(t *testing.T) {
	f, _ := teamStub(t)
	out := runT(t, "rule", "add", "--kind", "only", "--models", "openai/whisper-large-v3", "--use-cases", "audio-transcribe", "--note", "ours")
	contains2(t, out, "Added t-00000001: only use whisper-large-v3 for audio-transcribe")
	if got := f.bodies["PUT /v1/team/rules"][0]; got != `{"rules":[{"kind":"only","models":["openai/whisper-large-v3"],"use_cases":["audio-transcribe"],"note":"ours"}]}` {
		t.Errorf("put body %s", got)
	}
	runT(t, "rule", "add", "--kind", "budget", "--field", "monthly_usd", "--value", "0")
	if got := f.bodies["PUT /v1/team/rules"][1]; !strings.Contains(got, `{"kind":"budget","field":"monthly_usd","value":0}`) ||
		!strings.Contains(got, `"id":"t-00000001"`) {
		t.Errorf("second put should keep the first rule and send a zero value: %s", got)
	}

	// Loosening rules are refused here and never sent.
	for _, args := range [][]string{
		{"rule", "add", "--kind", "allow", "--target", "delegation"},
		{"rule", "add", "--kind", "approve"},
		{"rule", "add", "--kind", "budget", "--field", "daily_usd", "--value", "0"},
		{"rule", "add", "--kind", "only"},
	} {
		if err := runTErr(t, args...); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
	if n := len(f.bodies["PUT /v1/team/rules"]); n != 2 {
		t.Errorf("%d puts", n)
	}

	dir := t.TempDir()
	tomlFile := filepath.Join(dir, "team.toml")
	os.WriteFile(tomlFile, []byte(`[[rule]]
id = "r-1234abcd"
kind = "forbid"
target = "opencode"
created = 2026-11-01T00:00:00Z

[[rule]]
kind = "budget"
field = "per_task_usd"
value = 0.05
`), 0o600)
	out = runT(t, "rules", "set", tomlFile)
	contains2(t, out, "Saved 2 team rules.", "never use the OpenCode agent path", "set the per-task limit to $0.05")
	last := f.bodies["PUT /v1/team/rules"][2]
	if strings.Contains(last, "r-1234abcd") || strings.Contains(last, "created") || strings.Contains(last, "scope") {
		t.Errorf("local ids, times and scope must not be sent: %s", last)
	}

	jsonFile := filepath.Join(dir, "team.json")
	os.WriteFile(jsonFile, []byte(`{"rules":[{"kind":"mix","models":["a/b","c/d"],"weights":[2,1]}]}`), 0o600)
	runT(t, "rules", "set", jsonFile)
	if len(f.rules) != 1 || f.rules[0].Kind != "mix" {
		t.Errorf("rules %+v", f.rules)
	}
	bad := filepath.Join(dir, "bad.toml")
	os.WriteFile(bad, []byte("[[rule]]\nkind = \"approve\"\nmax_per_run_usd = 1.0\n"), 0o600)
	if err := runTErr(t, "rules", "set", bad); err == nil {
		t.Error("approve rule file accepted")
	}

	out = runT(t, "rules")
	contains2(t, out, f.rules[0].ID, "mix b 67%, d 33%")
	runT(t, "rule", "remove", f.rules[0].ID)
	if len(f.rules) != 0 {
		t.Errorf("remove left %+v", f.rules)
	}
	if err := runTErr(t, "rule", "remove", "t-ffffffff"); err == nil {
		t.Error("removing an unknown rule succeeded")
	}
}

func TestTeamShareSpendDelete(t *testing.T) {
	f, d := teamStub(t)
	out := runT(t, "share", "default", "40")
	contains2(t, out, "Default monthly share: $40.00")
	runT(t, "share", "acc_2", "none")
	runT(t, "share", "acc_3", "$12.5")
	b := f.bodies["PUT /v1/team/budget"]
	if b[0] != `{"default_monthly_usd":40}` || b[1] != `{"members":{"acc_2":null}}` || b[2] != `{"members":{"acc_3":12.5}}` {
		t.Errorf("budget bodies %v", b)
	}
	if err := runTErr(t, "share", "acc_2", "-1"); err == nil {
		t.Error("negative share accepted")
	}

	out = runT(t, "spend", "--month", "2026-11")
	contains2(t, out, "Team spend 2026-11: $3.42", "a@example.com", "$3.42", "audio-transcribe", "whisper-large-v3", "2026-11-13")
	if f.spendQs[0] != "month=2026-11" {
		t.Errorf("query %q", f.spendQs[0])
	}
	if err := runTErr(t, "spend", "--month", "November"); err == nil {
		t.Error("bad month accepted")
	}

	f.failOn["DELETE /v1/team/members/acc_2"] = "not_owner"
	if err := runTErr(t, "remove", "acc_2"); err == nil || !strings.Contains(err.Error(), "only the team's owner") {
		t.Errorf("not_owner worded as %v", err)
	}
	delete(f.failOn, "DELETE /v1/team/members/acc_2")
	contains2(t, runT(t, "remove", "acc_2"), "Removed acc_2.")

	if err := runTErr(t, "delete"); err == nil || len(f.bodies["DELETE /v1/team"]) != 0 {
		t.Error("delete without --yes went through")
	}
	runT(t, "sync")
	contains2(t, runT(t, "delete", "--yes"), "Deleted the team.")
	if team.Load(d, teamKeys()) != nil || account.Current(d).OrgID != "" {
		t.Error("delete should forget the policy")
	}
}

func TestTeamErrorsWorded(t *testing.T) {
	f, d := teamStub(t)
	f.inOrg = false
	f.failOn["POST /v1/team/join"] = "seats_full"
	if err := runTErr(t, "join", "p0v_x"); err == nil || !strings.Contains(err.Error(), "no free seat") {
		t.Errorf("seats_full worded as %v", err)
	}
	f.failOn["POST /v1/team/join"] = "invite_invalid"
	if err := runTErr(t, "join", "p0v_x"); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Errorf("invite_invalid worded as %v", err)
	}
	os.Remove(filepath.Join(d.Config, "secrets", "p0ssess_token"))
	if err := runTErr(t, "status"); err == nil || !strings.Contains(err.Error(), "account link") {
		t.Errorf("unlinked worded as %v", err)
	}
	if err := runTErr(t, "frobnicate"); err == nil {
		t.Error("unknown command accepted")
	}
}
