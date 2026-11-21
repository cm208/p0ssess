package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"p0ssess/internal/mcp"
	"p0ssess/internal/pme/local"
	"p0ssess/internal/team"
)

// Team tools: the user's Team org from the main conversation. They answer in short plain text, the
// same lines as p0ssess team on the command line.

const teamInstructions = `- team_*: the user's p0ssess Team (invite by email, status with seats and pending invites, spend by member and model, shared rules and approvals). Show the tool's text as it is. Shared rule changes: team_rules_add with dry_run true, show the one line, save after the user confirms.
`

var teamRuleProps = map[string]any{
	"kind":      map[string]any{"type": "string", "enum": []string{"forbid", "only", "mix", "ask", "budget"}, "description": "ask: every seat asks before matching work (a team approval)"},
	"target":    map[string]any{"type": "string", "enum": []string{"model", "claude_agents", "opencode", "delegation"}, "description": "For forbid"},
	"models":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "OpenRouter ids, e.g. x-ai/grok-4.20"},
	"weights":   map[string]any{"type": "array", "items": map[string]any{"type": "number"}, "description": "mix only, one per model"},
	"use_cases": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Catalog ids; empty covers all"},
	"kinds":     map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"media", "generate", "agent", "review", "code", "text"}}},
	"field":     map[string]any{"type": "string", "enum": []string{"per_task_usd", "daily_usd", "monthly_usd"}, "description": "budget only; the limit for every seat"},
	"value":     map[string]any{"type": "number", "description": "budget only, USD"},
	"note":      map[string]any{"type": "string", "description": "Shown to members; the owner's own words, short"},
	"dry_run":   map[string]any{"type": "boolean", "description": "Check and render without saving"},
}

func teamTools() []mcp.Tool {
	schema := func(props map[string]any, required ...string) json.RawMessage {
		m := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			m["required"] = required
		}
		b, _ := json.Marshal(m)
		return b
	}
	ro := map[string]any{"readOnlyHint": true}
	return []mcp.Tool{
		{Name: "team_status", Description: "The user's p0ssess Team: role, seats used and total, pending invites, members and their monthly shares, shared rules.",
			InputSchema: schema(map[string]any{}), Annotations: ro},
		{Name: "team_invite", Description: "Invite someone to the user's Team by email (owner only): p0ssess cloud emails them a one-use join link that lasts 7 days. Without email it makes an invite link to pass on instead.",
			InputSchema: schema(map[string]any{
				"email": map[string]any{"type": "string", "description": "The address to invite"},
				"uses":  map[string]any{"type": "integer", "description": "Link only: how many people may join with it, 1-100 (default 1)"},
				"days":  map[string]any{"type": "integer", "description": "Link only: days until it expires, 1-30 (default 7)"},
			})},
		{Name: "team_spend", Description: "Team spend by member and by model for a month, with today's column in the current month (owner: every member; member: their own).",
			InputSchema: schema(map[string]any{"month": map[string]any{"type": "string", "description": "YYYY-MM; default this month (UTC)"}}), Annotations: ro},
		{Name: "team_rules", Description: "The Team's shared rules: routing rules, approvals (ask rules) and spending limits that apply to every seat.",
			InputSchema: schema(map[string]any{}), Annotations: ro},
		{Name: "team_rules_add", Description: "Add one shared rule for every seat (owner only). Rules can only tighten: forbid, only, mix, ask (needs the member's yes first) or budget (per_task_usd, daily_usd, monthly_usd). Use dry_run first and show the line.",
			InputSchema: schema(teamRuleProps, "kind")},
		{Name: "team_rules_remove", Description: "Remove one shared rule by its id (t-xxxxxxxx; owner only).",
			InputSchema: schema(map[string]any{"id": map[string]any{"type": "string"}}, "id")},
	}
}

// teamTool runs the Team tools against the accounts API with this install's device token.
type teamTool struct {
	eng     *local.Engine
	version string
}

func (t *teamTool) client() *team.Client { return team.NewClient(t.eng.Dirs, t.version) }

// resync fetches the policy after a rule change, so the owner's own seat has it now.
func (t *teamTool) resync(ctx context.Context, c *team.Client) {
	sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	team.Sync(sctx, c, t.eng.Table.Keys())
}

func teamFail(err error) mcp.CallResult {
	if team.Code(err) == "not_in_team" {
		return mcp.Text("Not in a team. The owner of one can invite this account; or create one with p0ssess team create NAME.")
	}
	return mcp.Error(team.Explain(err).Error())
}

func (t *teamTool) call(name string) mcp.Handler {
	return func(ctx context.Context, raw json.RawMessage, _ mcp.Progress) mcp.CallResult {
		if len(raw) == 0 || string(raw) == "null" {
			raw = []byte("{}")
		}
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		c := t.client()
		var b strings.Builder
		switch name {
		case "team_status":
			v, err := c.Get(ctx)
			if err != nil {
				return teamFail(err)
			}
			team.WriteStatus(&b, v)
		case "team_invite":
			var a struct {
				Email string `json:"email"`
				Uses  int    `json:"uses"`
				Days  int    `json:"days"`
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return mcp.Error(err.Error())
			}
			if a.Email = strings.TrimSpace(a.Email); a.Email != "" {
				inv, err := c.InviteEmail(ctx, a.Email)
				if err != nil {
					return teamFail(err)
				}
				if inv.Emailed != nil && !*inv.Emailed {
					fmt.Fprintf(&b, "The invite email to %s could not be sent. Pass them this link (works once, until %s): %s\n", inv.Email, team.Day(inv.ExpiresAt), inv.URL)
				} else {
					fmt.Fprintf(&b, "Invited %s: the join link in the email works once, until %s.\n", inv.Email, team.Day(inv.ExpiresAt))
				}
				if v, err := c.Get(ctx); err == nil {
					if line := team.SeatHint(v); line != "" {
						fmt.Fprintln(&b, line)
					}
				}
				break
			}
			if a.Uses < 0 || a.Uses > 100 || a.Days < 0 || a.Days > 30 {
				return mcp.Error("uses is 1 to 100 and days 1 to 30")
			}
			inv, err := c.Invite(ctx, a.Uses, a.Days)
			if err != nil {
				return teamFail(err)
			}
			fmt.Fprintf(&b, "%s\nGood for %d join(s) until %s. It is shown once.\n", inv.URL, inv.MaxUses, team.Day(inv.ExpiresAt))
		case "team_spend":
			var a struct {
				Month string `json:"month"`
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return mcp.Error(err.Error())
			}
			if a.Month != "" {
				if _, err := time.Parse("2006-01", a.Month); err != nil {
					return mcp.Error("month must be YYYY-MM")
				}
			}
			d, err := c.Spend(ctx, a.Month)
			if err != nil {
				return teamFail(err)
			}
			team.WriteSpend(&b, d, false)
		case "team_rules":
			v, err := c.Get(ctx)
			if err != nil {
				return teamFail(err)
			}
			team.WriteRules(&b, v.Rules)
		case "team_rules_add":
			var a struct {
				team.Rule
				DryRun bool `json:"dry_run"`
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return mcp.Error(err.Error())
			}
			r := a.Rule
			r.ID = ""
			if err := team.Check([]team.Rule{r}); err != nil {
				return mcp.Error(strings.TrimPrefix(err.Error(), "rule 1: "))
			}
			if a.DryRun {
				fmt.Fprintf(&b, "Every seat: %s. Not saved: show the user this line and save after they confirm.\n", r.String())
				break
			}
			v, err := c.Get(ctx)
			if err != nil {
				return teamFail(err)
			}
			all := append(v.Rules, r)
			if err := team.Check(all); err != nil {
				return mcp.Error(err.Error())
			}
			saved, err := c.PutRules(ctx, all)
			if err != nil {
				return teamFail(err)
			}
			t.resync(ctx, c)
			if len(saved) > 0 {
				n := saved[len(saved)-1]
				fmt.Fprintf(&b, "Added %s: %s. Every seat gets it within 5 minutes.\n", n.ID, n.String())
			}
		case "team_rules_remove":
			var a struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return mcp.Error(err.Error())
			}
			v, err := c.Get(ctx)
			if err != nil {
				return teamFail(err)
			}
			keep := []team.Rule{}
			var gone *team.Rule
			for i, r := range v.Rules {
				if r.ID == a.ID {
					gone = &v.Rules[i]
					continue
				}
				keep = append(keep, r)
			}
			if gone == nil {
				return mcp.Error(fmt.Sprintf("no team rule %s; team_rules lists them", a.ID))
			}
			if _, err := c.PutRules(ctx, keep); err != nil {
				return teamFail(err)
			}
			t.resync(ctx, c)
			fmt.Fprintf(&b, "Removed %s: %s\n", gone.ID, gone.String())
		default:
			return mcp.Error("unknown tool " + name)
		}
		return mcp.Text(strings.TrimRight(b.String(), "\n"))
	}
}

func addTeamTools(s *mcp.Server, eng *local.Engine, version string) {
	t := &teamTool{eng: eng, version: version}
	for _, tool := range teamTools() {
		s.AddTool(tool, t.call(tool.Name))
	}
}
