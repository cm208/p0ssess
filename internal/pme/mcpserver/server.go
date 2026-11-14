// Package mcpserver defines the engine's MCP tools (recommend, budget_status,
// settings_set, rules_list, rules_add, rules_remove, approvals_add,
// approvals_list, history_record_outcome and tendencies). The plugin's MCP
// server in internal/server serves them.
package mcpserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"p0ssess/internal/pme/local"
	"p0ssess/internal/rules"
)

// Instructions tell Claude how to use the engine tools.
const Instructions = `- Hand every routed task to the p0ssess agent; no recommend first, no confirmation, no narration. Reply with its result plus the model credit in a few words.
- Only "needs approval" asks the user: one line with the cost; on a yes, call approvals_add exactly as the message says. Never add an approval the user has not agreed to.
- Generated files: print their absolute paths, and send them with SendUserFile when the session has it.
- A run you used counts as accepted on its own. Call history_record_outcome only for redo, declined or overridden. Never include prompt content.
- Rules: rules_add or approvals_add with dry_run true, show the one line, save after the user confirms.`

// Server answers the engine tools; the plugin's one MCP server (internal/server) carries them.
type Server struct {
	E *local.Engine
}

var errUnknownTool = errors.New("unknown tool")

// Tools are the engine tool definitions (name, description, inputSchema).
func Tools() []map[string]any { return tools() }

// Call runs one engine tool and returns its JSON-ready result.
func (s *Server) Call(name string, args json.RawMessage) (any, error) { return s.call(name, args) }

type scopeArgs struct {
	Cwd     string `json:"cwd"`
	Session string `json:"session_id"`
}

func (a scopeArgs) scope() local.Scope { return local.Scope{Cwd: a.Cwd, Session: a.Session} }

type ruleArgs struct {
	scopeArgs
	rules.Rule
	ScopeName string `json:"scope"`
	DryRun    bool   `json:"dry_run"`
	ExpiresIn int    `json:"expires_in_minutes"`
}

func decode(raw json.RawMessage, v any) error {
	if len(raw) == 0 || string(raw) == "null" {
		raw = []byte("{}")
	}
	return json.Unmarshal(raw, v)
}

func (s *Server) call(name string, raw json.RawMessage) (any, error) {
	e := s.E
	switch name {
	case "recommend":
		var a struct {
			scopeArgs
			UseCase string         `json:"use_case"`
			Profile *local.Profile `json:"profile"`
			Stakes  int            `json:"stakes"`
			Lead    string         `json:"lead_model"`
			Top     int            `json:"top"`
			Detail  bool           `json:"detail"`
		}
		if err := decode(raw, &a); err != nil {
			return nil, err
		}
		rec, err := e.Recommend(local.Request{UseCase: a.UseCase, Profile: a.Profile, Stakes: a.Stakes, Lead: a.Lead, Top: a.Top, Record: true, Scope: a.scope()})
		if err != nil || a.Detail {
			return rec, err
		}
		return Compact(rec), nil
	case "budget_status":
		var a scopeArgs
		if err := decode(raw, &a); err != nil {
			return nil, err
		}
		return e.Status(a.scope())
	case "settings_set":
		var a struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		}
		if err := decode(raw, &a); err != nil {
			return nil, err
		}
		msg, err := e.SetSetting(a.Key, a.Value)
		return map[string]string{"saved": msg}, err
	case "rules_list", "approvals_list":
		var a scopeArgs
		if err := decode(raw, &a); err != nil {
			return nil, err
		}
		kind := ""
		if name == "approvals_list" {
			kind = rules.KindApprove
		}
		rs, err := e.RulesList(a.scope(), kind)
		return map[string]any{"rules": rs}, err
	case "rules_add", "approvals_add":
		var a ruleArgs
		if err := decode(raw, &a); err != nil {
			return nil, err
		}
		r := a.Rule
		r.Scope = a.ScopeName
		if name == "approvals_add" {
			r.Kind = rules.KindApprove
		}
		v, err := e.RuleAdd(a.scope(), r, a.DryRun, a.ExpiresIn)
		if err != nil {
			return nil, err
		}
		status := "saved"
		if a.DryRun {
			status = "not saved: show the user this line and save after they confirm"
		}
		return map[string]any{"rule": v, "status": status}, nil
	case "rules_remove":
		var a struct {
			scopeArgs
			ID string `json:"id"`
		}
		if err := decode(raw, &a); err != nil {
			return nil, err
		}
		v, err := e.RuleRemove(a.scope(), a.ID)
		return map[string]any{"removed": v}, err
	case "history_record_outcome":
		var a struct {
			scopeArgs
			local.Outcome
		}
		if err := decode(raw, &a); err != nil {
			return nil, err
		}
		o := a.Outcome
		o.Scope = a.scope()
		en, err := e.RecordOutcome(o)
		return map[string]any{"recorded": en}, err
	case "tendencies":
		var a scopeArgs
		if err := decode(raw, &a); err != nil {
			return nil, err
		}
		return e.Learned(a.scope()), nil
	}
	return nil, fmt.Errorf("%w %q", errUnknownTool, name)
}

func obj(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

var (
	str   = map[string]any{"type": "string"}
	num   = map[string]any{"type": "number"}
	integ = map[string]any{"type": "integer"}
	boo   = map[string]any{"type": "boolean"}
	strs  = map[string]any{"type": "array", "items": str}
)

func withScope(p map[string]any) map[string]any {
	p["cwd"] = map[string]any{"type": "string", "description": "Project directory; defaults to the server's"}
	p["session_id"] = map[string]any{"type": "string", "description": "Claude Code session id, for session-scoped rules"}
	return p
}

func ruleProps() map[string]any {
	return withScope(map[string]any{
		"kind":   map[string]any{"type": "string", "enum": []string{"forbid", "allow", "only", "mix", "approve", "ask", "budget"}, "description": "ask: always ask first for matching work"},
		"target": map[string]any{"type": "string", "enum": []string{"model", "claude_agents", "opencode", "delegation"}, "description": "For forbid and allow"},
		"models": strs, "weights": map[string]any{"type": "array", "items": num},
		"use_cases":       map[string]any{"type": "array", "items": str, "description": "Catalog ids; empty covers all"},
		"kinds":           map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"media", "generate", "agent", "review", "code", "text"}}},
		"max_stakes":      map[string]any{"type": "integer", "description": "allow only: lift the stakes gate up to this level"},
		"max_per_run_usd": num, "max_count": integ, "max_total_usd": num,
		"period":             map[string]any{"type": "string", "enum": []string{"day", "total"}},
		"field":              map[string]any{"type": "string", "enum": []string{"per_task_usd", "daily_usd", "monthly_usd"}},
		"value":              num,
		"scope":              map[string]any{"type": "string", "enum": []string{"session", "project", "global"}},
		"expires_in_minutes": integ,
		"note":               map[string]any{"type": "string", "description": "The user's own words, short"},
		"dry_run":            map[string]any{"type": "boolean", "description": "Validate and render without saving"},
	})
}

func tools() []map[string]any {
	profile := obj(map[string]any{
		"text":      map[string]any{"type": "string", "description": "A short description of the job (not the user's content)"},
		"inputs":    map[string]any{"type": "array", "items": str, "description": "text, image, file, audio, video"},
		"outputs":   map[string]any{"type": "array", "items": str, "description": "text, transcript, image, speech, audio, video, embeddings"},
		"path":      map[string]any{"type": "string", "enum": []string{"direct", "agent"}},
		"size":      map[string]any{"type": "string", "enum": []string{"T", "S", "M", "R", "L"}},
		"in_tokens": integ, "out_tokens": integ, "bytes": integ, "turns": integ, "items": integ,
		"audio_seconds": num, "video_seconds": num, "images": integ, "pages": integ, "chars": integ, "gen_seconds": num,
		"needs_tools": boo,
	})
	return []map[string]any{
		{"name": "recommend", "description": "For the p0ssess agent. Rank every route for a task (Claude included) by expected cost per accepted result, with a cost breakdown, a one-line reason and whether it needs the user's approval. Records the offer.",
			"inputSchema": obj(withScope(map[string]any{
				"use_case":   map[string]any{"type": "string", "description": "Catalog id, e.g. video-understand"},
				"profile":    profile,
				"stakes":     map[string]any{"type": "integer", "minimum": 1, "maximum": 4, "description": "1 trivial, 2 routine, 3 important, 4 critical"},
				"lead_model": map[string]any{"type": "string", "description": "The session's model id if known"},
				"top":        integ,
				"detail":     map[string]any{"type": "boolean", "description": "The full breakdown instead of one line per option"},
			}))},
		{"name": "budget_status", "description": "Spend against the monthly cap and daily limit, Claude limit pressure and λ, running jobs, and where the config files are.",
			"inputSchema": obj(withScope(map[string]any{}))},
		{"name": "settings_set", "description": "Change one budget or engine setting permanently. Keys: " + fmt.Sprint(local.SettingKeys()) + ". Lists are comma-separated.",
			"inputSchema": obj(map[string]any{"key": str, "value": str}, "key", "value")},
		{"name": "rules_list", "description": "Every rule with its scope, expiry and one-line text. Rules at team scope come from the user's Team org: read-only here, they only tighten.", "inputSchema": obj(withScope(map[string]any{}))},
		{"name": "rules_add", "description": "Add a structured rule (forbid, allow, only, mix, approve, ask, budget). Use dry_run first and show the text to the user.", "inputSchema": obj(ruleProps(), "kind")},
		{"name": "rules_remove", "description": "Remove a rule by id.", "inputSchema": obj(withScope(map[string]any{"id": str}), "id")},
		{"name": "approvals_add", "description": "Add a standing approval: auto-approve a kind of task up to a per-run price, with optional count or total limits.", "inputSchema": obj(ruleProps(), "max_per_run_usd")},
		{"name": "approvals_list", "description": "Standing approvals and how much of each is used.", "inputSchema": obj(withScope(map[string]any{}))},
		{"name": "history_record_outcome", "description": "Record a delegated result you did not use: redo, overridden or declined (a finished run you used counts as accepted on its own). Pass run_id: the outcome then replaces that run's implied acceptance. A run started with await_outcome counts only once an outcome (accepted included) names it. No prompt content.",
			"inputSchema": obj(withScope(map[string]any{
				"offer_id": str, "model": str,
				"run_id":   map[string]any{"type": "string", "description": "The run the outcome is about: a relay session_id or a direct run id. Its measured cost becomes the outcome's cash"},
				"outcome":  map[string]any{"type": "string", "enum": []string{"accepted", "redo", "overridden", "declined"}},
				"cash_usd": map[string]any{"type": "number", "description": "Only for a run p0ssess did not measure"}, "use_case": str, "chosen": map[string]any{"type": "string", "description": "What ran instead, for overridden"},
				"approval_id": str, "stakes": integ,
				"reason": map[string]any{"type": "string", "description": "A few words, never prompt content"},
			}), "model", "outcome")},
		{"name": "tendencies", "description": "Learned patterns from the history, rule suggestions (never saved automatically) and recent delegations.", "inputSchema": obj(withScope(map[string]any{}))},
	}
}

// BackgroundRefresh fetches a newer route table when the last check is a day old, without blocking.
func BackgroundRefresh(e *local.Engine) func() {
	return e.RefreshInBackground
}

// CompactRecommendation is recommend's default answer: the pick and one line per option.
type CompactRecommendation struct {
	OfferID  string               `json:"offer_id,omitempty"`
	UseCase  string               `json:"use_case"`
	Stakes   int                  `json:"stakes"`
	Announce string               `json:"announce"`
	Options  []string             `json:"options"`
	Second   *local.SecondOpinion `json:"second_opinion,omitempty"`
	Notes    []string             `json:"notes,omitempty"`
}

// Compact shortens a recommendation to what Claude acts on.
func Compact(r *local.Recommendation) CompactRecommendation {
	c := CompactRecommendation{OfferID: r.OfferID, UseCase: r.UseCase.ID, Stakes: r.Stakes, Announce: r.Announce, Second: r.SecondOpinion}
	for i, o := range r.Options {
		if i == 3 {
			break
		}
		line := fmt.Sprintf("%s %s · total $%.4f", o.Route, o.Model, o.Total)
		if o.Breakdown.Cash > 0 {
			line += fmt.Sprintf(" · about $%.4f cash", o.EstimateUSD)
		}
		line += fmt.Sprintf(" · p_fail %.2f", o.PFail)
		if o.NeedsApproval {
			line += " · asks: " + strings.Join(o.ApprovalReasons, "; ")
		}
		c.Options = append(c.Options, line)
	}
	c.Notes = append(c.Notes, r.Notes...)
	if r.StakesNote != "" {
		c.Notes = append(c.Notes, r.StakesNote)
	}
	return c
}
