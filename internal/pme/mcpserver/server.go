// Package mcpserver defines the engine's MCP tools (recommend, budget_status,
// settings_set, rules_list, rules_add, rules_remove, approvals_add,
// approvals_list, history_record_outcome and tendencies). The plugin's MCP
// server in internal/server serves them.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"p0ssess/internal/pme/catalog"
	"p0ssess/internal/pme/engine"
	"p0ssess/internal/pme/registry"
	"p0ssess/internal/rules"
)

// Instructions tell Claude how to use the engine tools.
const Instructions = `p0ssess routes work to other models through OpenRouter; Claude stays in charge and reviews what comes back.
- Call recommend before work Claude cannot do (audio, video, image/audio/video generation, embeddings, over 1M tokens), bulk one-shot text jobs, or long agent work. Pass use_case when you know the catalog id, else a profile. Rate stakes 1-4 yourself.
- Follow the pick unless you have a stated reason. Announce routing in one line (the announce field). Ask the user first when the option says needs_approval.
- The direct and run tools enforce every limit themselves. When one answers "needs approval", ask the user with the cost; if they agree, save a one-run session approval with approvals_add as the message says (its permission prompt is the user's confirmation) and call again. Never add an approval the user has not agreed to.
- Spend is measured: the relay and the direct tool record each run's real cost. After delegated work, call history_record_outcome with the offer_id and the run_id from the result (a relay session id or a direct run id): accepted, redo (you had to redo it), overridden or declined. Never include prompt content.
- To turn a plain-language rule into a structured one, call rules_add or approvals_add with dry_run true, show the user the one-line text, and save only after they confirm. Never create rules on your own.`

// Server answers the engine tools; the plugin's one MCP server (internal/server) carries them.
type Server struct {
	E *engine.Engine
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

func (a scopeArgs) scope() engine.Scope { return engine.Scope{Cwd: a.Cwd, Session: a.Session} }

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
			UseCase string           `json:"use_case"`
			Profile *catalog.Profile `json:"profile"`
			Stakes  int              `json:"stakes"`
			Lead    string           `json:"lead_model"`
			Top     int              `json:"top"`
		}
		if err := decode(raw, &a); err != nil {
			return nil, err
		}
		return e.Recommend(engine.Request{UseCase: a.UseCase, Profile: a.Profile, Stakes: a.Stakes, Lead: a.Lead, Top: a.Top, Record: true, Scope: a.scope()})
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
			engine.Outcome
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
		"kind":   map[string]any{"type": "string", "enum": []string{"forbid", "allow", "only", "mix", "approve", "budget"}},
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
		{"name": "recommend", "description": "Rank every route for a task (Claude included) by expected cost per accepted result, with a cost breakdown, a one-line reason and whether it needs the user's approval. Records the offer.",
			"inputSchema": obj(withScope(map[string]any{
				"use_case":   map[string]any{"type": "string", "description": "Catalog id, e.g. video-understand"},
				"profile":    profile,
				"stakes":     map[string]any{"type": "integer", "minimum": 1, "maximum": 4, "description": "1 trivial, 2 routine, 3 important, 4 critical"},
				"lead_model": map[string]any{"type": "string", "description": "The session's model id if known"},
				"top":        integ,
			}))},
		{"name": "budget_status", "description": "Spend against the monthly cap and daily limit, Claude limit pressure and λ, running jobs, and where the config files are.",
			"inputSchema": obj(withScope(map[string]any{}))},
		{"name": "settings_set", "description": "Change one budget or engine setting permanently. Keys: " + fmt.Sprint(engine.SettingKeys()) + ". Lists are comma-separated.",
			"inputSchema": obj(map[string]any{"key": str, "value": str}, "key", "value")},
		{"name": "rules_list", "description": "Every rule with its scope, expiry and one-line text.", "inputSchema": obj(withScope(map[string]any{}))},
		{"name": "rules_add", "description": "Add a structured rule (forbid, allow, only, mix, approve, budget). Use dry_run first and show the text to the user.", "inputSchema": obj(ruleProps(), "kind")},
		{"name": "rules_remove", "description": "Remove a rule by id.", "inputSchema": obj(withScope(map[string]any{"id": str}), "id")},
		{"name": "approvals_add", "description": "Add a standing approval: auto-approve a kind of task up to a per-run price, with optional count or total limits.", "inputSchema": obj(ruleProps(), "max_per_run_usd")},
		{"name": "approvals_list", "description": "Standing approvals and how much of each is used.", "inputSchema": obj(withScope(map[string]any{}))},
		{"name": "history_record_outcome", "description": "Record what happened to an offer: accepted, redo, overridden or declined. Pass run_id so the measured cost is used. No prompt content.",
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

// BackgroundRefresh refreshes the live model list if the cache is stale.
func BackgroundRefresh(e *engine.Engine) func() {
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if fetched, err := registry.Refresh(ctx, &http.Client{Timeout: 25 * time.Second}, e.Dirs.ModelsCache(), registry.LiveMaxAge, ""); err == nil && fetched {
			e.Reg.MergeLive(e.Dirs.ModelsCache())
		}
	}
}
