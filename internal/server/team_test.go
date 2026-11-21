package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"p0ssess/internal/account"
	"p0ssess/internal/mcp"
	"p0ssess/internal/pme/local"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/pme/table/tabletest"
	"p0ssess/internal/team"
)

// teamCloud is the Team side of the accounts API for one org that the caller owns.
type teamCloud struct {
	mu      sync.Mutex
	rules   []team.Rule
	invites []map[string]any
	bodies  map[string][]string
	n       int
}

func newTeamCloud(t *testing.T) (*teamCloud, *teamTool) {
	t.Helper()
	t.Setenv("P0SSESS_HOME", t.TempDir())
	_, priv := tabletest.Key()
	c := &teamCloud{rules: []team.Rule{}, bodies: map[string][]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		key := r.Method + " " + r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		c.bodies[key] = append(c.bodies[key], string(raw))
		enc := json.NewEncoder(w)
		switch key {
		case "GET /v1/account":
			enc.Encode(map[string]any{"account": map[string]any{"id": "acc_1"},
				"entitlements": map[string]any{"plan": "team", "status": "active", "org_id": "org_1"}})
		case "GET /v1/team":
			enc.Encode(map[string]any{"org": map[string]any{"id": "org_1", "name": "Acme", "seats": 2, "active": true},
				"role": "owner", "account_id": "acc_1", "monthly_share_usd": 25, "rules": c.rules, "seats_used": 2,
				"members": []map[string]any{
					{"account_id": "acc_1", "email": "a@example.com", "role": "owner", "has_seat": true},
					{"account_id": "acc_2", "email": "b@example.com", "role": "member", "has_seat": true}},
				"invites": c.invites})
		case "POST /v1/team/invites":
			var in map[string]any
			json.Unmarshal(raw, &in)
			inv := map[string]any{"id": "inv_1", "url": "https://example.com/team/join?invite=p0v_x", "max_uses": 1,
				"expires_at": "2026-11-27T00:00:00Z", "email": in["email"], "emailed": true}
			c.invites = append(c.invites, map[string]any{"id": "inv_1", "email": in["email"], "max_uses": 1, "uses": 0, "expires_at": "2026-11-27T00:00:00Z"})
			w.WriteHeader(201)
			enc.Encode(inv)
		case "GET /v1/team/spend":
			w.Write([]byte(`{"month":"2026-11","today":"2026-11-20","today_usd":0.6,"total_usd":3.42,
 "members":[{"account_id":"acc_1","email":"a@example.com","role":"owner","monthly_share_usd":25,"spent_usd":3,"runs":9,"today_usd":0.5},
            {"account_id":"acc_2","email":"b@example.com","role":"member","monthly_share_usd":null,"spent_usd":0.42,"runs":2,"today_usd":0.1}],
 "by_model":[{"model":"moonshotai/kimi-k3.1-pro","runs":3,"spent_usd":2.9,"today_usd":0.5},{"model":"openai/whisper-large-v3","runs":8,"spent_usd":0.52,"today_usd":0.1}],
 "by_use_case":[],"by_day":[]}`))
		case "PUT /v1/team/rules":
			var in struct {
				Rules []team.Rule `json:"rules"`
			}
			json.Unmarshal(raw, &in)
			for i := range in.Rules {
				if in.Rules[i].ID == "" {
					c.n++
					in.Rules[i].ID = fmt.Sprintf("t-%08x", c.n)
				}
			}
			c.rules = in.Rules
			enc.Encode(map[string]any{"rules": c.rules})
		case "GET /v1/team/policy":
			p, _ := json.Marshal(map[string]any{"v": 1, "type": "team-policy", "org_id": "org_1", "org_name": "Acme",
				"account_id": "acc_1", "role": "owner", "issued": time.Now().UTC().Format(time.RFC3339), "rules": c.rules, "report_spend": true})
			w.Write(tabletest.Seal(priv, p))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("P0SSESS_CLOUD_URL", srv.URL)
	d := paths.Default()
	eng, err := local.Load(d)
	if err != nil {
		t.Fatal(err)
	}
	eng.Table = tabletest.Store(d.Cache)
	eng.Table.Checked(time.Now())
	account.SaveToken(d, "p0d_abc")
	account.Refresh(context.Background(), d, account.NewClient("test"))
	return c, &teamTool{eng: eng, version: "test"}
}

func callTeam(t *testing.T, tt *teamTool, name string, args any) (string, bool) {
	t.Helper()
	b, _ := json.Marshal(args)
	res := tt.call(name)(context.Background(), b, func(float64, string) {})
	if len(res.Content) == 0 {
		t.Fatalf("%s: empty result", name)
	}
	return res.Content[0].Text, res.IsError
}

func TestTeamToolsInviteStatusSpend(t *testing.T) {
	c, tt := newTeamCloud(t)
	out, isErr := callTeam(t, tt, "team_invite", map[string]any{"email": "sam@example.com"})
	if isErr || !strings.Contains(out, "Invited sam@example.com") || strings.Contains(out, "p0v_") {
		t.Fatalf("invite: %v %q", isErr, out)
	}
	if got := c.bodies["POST /v1/team/invites"][0]; got != `{"email":"sam@example.com"}` {
		t.Errorf("invite body %s", got)
	}
	// Two seats, two members and one pending invite: the owner is told someone cannot join yet.
	if !strings.Contains(out, "1 pending invites but 0 free seats") {
		t.Errorf("no seat hint: %q", out)
	}

	out, _ = callTeam(t, tt, "team_status", nil)
	for _, want := range []string{"Team Acme (org_1); you are its owner", "Seats: 2 of 2 used, 0 free, 1 pending invites",
		"b@example.com", "Pending invites:", "inv_1  sam@example.com  0 of 1 used, expires Nov 27, 2026", "No team rules."} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}

	out, _ = callTeam(t, tt, "team_spend", map[string]any{})
	for _, want := range []string{"Team spend 2026-11: $3.42 this month, $0.60 today (2026-11-20 UTC)", "Member", "Today", "b@example.com",
		"$0.42", "moonshotai/kimi-k3.1-pro", "$2.90"} {
		if !strings.Contains(out, want) {
			t.Errorf("spend lacks %q:\n%s", want, out)
		}
	}
	if _, isErr := callTeam(t, tt, "team_spend", map[string]any{"month": "Nov"}); !isErr {
		t.Error("bad month accepted")
	}
}

func TestTeamToolsRules(t *testing.T) {
	c, tt := newTeamCloud(t)
	out, isErr := callTeam(t, tt, "team_rules_add", map[string]any{"kind": "forbid", "target": "model", "models": []string{"x-ai/grok-4.20"}, "dry_run": true})
	if isErr || !strings.Contains(out, "never use grok-4.20") || !strings.Contains(out, "Not saved") || len(c.bodies["PUT /v1/team/rules"]) != 0 {
		t.Fatalf("dry run: %v %q", isErr, out)
	}
	out, isErr = callTeam(t, tt, "team_rules_add", map[string]any{"kind": "forbid", "target": "model", "models": []string{"x-ai/grok-4.20"}})
	if isErr || !strings.Contains(out, "Added t-00000001") {
		t.Fatalf("add: %v %q", isErr, out)
	}
	callTeam(t, tt, "team_rules_add", map[string]any{"kind": "ask", "kinds": []string{"generate"}})
	// The owner's own seat has the new rules at once.
	if p := team.Load(tt.eng.Dirs, tt.eng.Table.Keys()); p == nil || len(p.Rules) != 2 {
		t.Fatalf("policy after add: %+v", p)
	}
	for _, bad := range []map[string]any{{"kind": "allow", "target": "delegation"}, {"kind": "approve"},
		{"kind": "budget", "field": "daily_usd", "value": 0}} {
		if out, isErr := callTeam(t, tt, "team_rules_add", bad); !isErr {
			t.Errorf("%v accepted: %q", bad, out)
		}
	}
	out, _ = callTeam(t, tt, "team_rules", nil)
	if !strings.Contains(out, "t-00000001  never use grok-4.20") || !strings.Contains(out, "t-00000002") {
		t.Errorf("rules: %q", out)
	}
	out, isErr = callTeam(t, tt, "team_rules_remove", map[string]any{"id": "t-00000001"})
	if isErr || !strings.Contains(out, "Removed t-00000001") || len(c.rules) != 1 {
		t.Fatalf("remove: %v %q %+v", isErr, out, c.rules)
	}
	if _, isErr := callTeam(t, tt, "team_rules_remove", map[string]any{"id": "t-ffffffff"}); !isErr {
		t.Error("unknown id removed")
	}
}

func TestTeamToolsNotLinkedOrNotInTeam(t *testing.T) {
	_, tt := newTeamCloud(t)
	account.DeleteToken(tt.eng.Dirs)
	out, isErr := callTeam(t, tt, "team_status", nil)
	if !isErr || !strings.Contains(out, "account link") {
		t.Errorf("unlinked: %v %q", isErr, out)
	}
}

func TestTeamToolsAreRegistered(t *testing.T) {
	s := mcp.NewServer("t", "0")
	_, tt := newTeamCloud(t)
	addTeamTools(s, tt.eng, "test")
	want := map[string]bool{"team_status": true, "team_invite": true, "team_spend": true, "team_rules": true, "team_rules_add": true, "team_rules_remove": true}
	for _, tool := range teamTools() {
		delete(want, tool.Name)
		var schema map[string]any
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil || schema["type"] != "object" {
			t.Errorf("%s schema %s", tool.Name, tool.InputSchema)
		}
	}
	if len(want) != 0 {
		t.Errorf("missing tools %v", want)
	}
}
