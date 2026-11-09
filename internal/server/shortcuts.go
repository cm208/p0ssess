package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"p0ssess/internal/history"
	"p0ssess/internal/mcp"
	"p0ssess/internal/pme/engine"
	"p0ssess/internal/pme/paths"
)

// Shortcut agents are named relay agents with a fixed model (for example p0ssess-gemini-video). The
// engine proposes them from the history; a file is written only when Claude calls shortcuts_create,
// which Claude Code asks the user to approve unless the user allowed it. At most MaxShortcuts exist.

const (
	MaxShortcuts   = 12
	shortcutPrefix = "p0ssess-"
	shortcutMark   = "<!-- made by p0ssess; remove with shortcuts_remove -->"
	pluginTool     = "mcp__plugin_p0ssess_p0ssess__"
)

func enginePaths() paths.Dirs { return paths.Default() }

func randID() string {
	var b [6]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// AgentsDir is the user's Claude Code agent directory.
func AgentsDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "agents")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "agents")
}

type Shortcut struct {
	Name   string `json:"name"`
	Model  string `json:"model"`
	Route  string `json:"route"`
	Path   string `json:"path,omitempty"`
	Reason string `json:"reason,omitempty"`
}

var shortcutName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,40}$`)

// ListShortcuts returns the shortcut agents p0ssess made in dir.
func ListShortcuts(dir string) []Shortcut {
	ents, _ := os.ReadDir(dir)
	var out []Shortcut
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), shortcutPrefix) || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(p)
		if err != nil || !strings.Contains(string(b), shortcutMark) {
			continue
		}
		s := Shortcut{Name: strings.TrimSuffix(strings.TrimPrefix(e.Name(), shortcutPrefix), ".md"), Path: p}
		for _, line := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(line, "<!-- p0ssess model: "); ok {
				s.Model, s.Route, _ = strings.Cut(strings.TrimSuffix(v, " -->"), " ")
			}
		}
		out = append(out, s)
	}
	return out
}

// SuggestShortcuts proposes shortcuts for routes the user reliably accepts (three or more outcomes,
// mostly accepted) that have no shortcut yet.
func SuggestShortcuts(e *engine.Engine, dir string) []Shortcut {
	have := map[string]bool{}
	for _, s := range ListShortcuts(dir) {
		have[s.Model] = true
	}
	var out []Shortcut
	for _, t := range e.Ledger().Tendencies() {
		if !t.Favored || have[t.Model] || strings.HasPrefix(t.Model, "anthropic/") || !strings.Contains(t.Model, "/") {
			continue
		}
		route := "agent"
		if t.Kind == "media" || t.Kind == "generate" || t.Kind == "text" || t.Kind == "review" {
			route = "direct"
		}
		name := strings.ToLower(history.Short(t.Model))
		if t.Kind != "" {
			name += "-" + t.Kind
		}
		name = regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(name, "-")
		out = append(out, Shortcut{Name: name, Model: t.Model, Route: route, Reason: t.Text})
		have[t.Model] = true
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// CreateShortcut writes the agent file. dryRun returns the content without writing.
func CreateShortcut(dir string, s Shortcut, description string, dryRun bool) (string, string, error) {
	if !shortcutName.MatchString(s.Name) {
		return "", "", errors.New("name: 2-41 lowercase letters, digits and dashes")
	}
	if !strings.Contains(s.Model, "/") || strings.ContainsAny(s.Model, " \t\n") {
		return "", "", errors.New("model must be an OpenRouter id like google/gemini-3.8-flash")
	}
	if s.Route != "agent" && s.Route != "direct" {
		return "", "", errors.New(`route must be "agent" or "direct"`)
	}
	path := filepath.Join(dir, shortcutPrefix+s.Name+".md")
	existing := ListShortcuts(dir)
	replacing := false
	for _, x := range existing {
		if x.Path == path {
			replacing = true
		}
	}
	if !replacing && len(existing) >= MaxShortcuts {
		return "", "", fmt.Errorf("there are already %d p0ssess shortcuts (the cap); remove one first", len(existing))
	}
	if _, err := os.Stat(path); err == nil && !replacing {
		return "", "", fmt.Errorf("%s exists and was not made by p0ssess", path)
	}
	body := shortcutBody(s, description)
	if dryRun {
		return path, body, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	return path, body, paths.WriteFileAtomic(path, []byte(body), 0o644)
}

func shortcutBody(s Shortcut, description string) string {
	short := history.Short(s.Model)
	if description == "" {
		if s.Route == "direct" {
			description = fmt.Sprintf("Runs a one-shot job (files allowed) on %s through OpenRouter and returns its answer verbatim.", short)
		} else {
			description = fmt.Sprintf("Hands a task to %s in a sandboxed OpenCode session and returns its answer verbatim.", short)
		}
	}
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "name: %s%s\n", shortcutPrefix, s.Name)
	desc := description + " Made by p0ssess."
	if s.Route == "direct" {
		desc += " Put the task, the absolute paths of any files, and the cost limit (\"max $0.05\") in the prompt."
	} else {
		desc += " Put only the task in the prompt; add \"edit mode\" when it should change files. Continue with SendMessage."
	}
	fmt.Fprintf(&b, "description: %s\n", yamlQuote(desc))
	b.WriteString("model: haiku\nbackground: true\nomitClaudeMd: true\nmaxTurns: 12\n")
	if s.Route == "direct" {
		fmt.Fprintf(&b, "tools: %sdirect\n", pluginTool)
	} else {
		fmt.Fprintf(&b, "tools: %srun, %swait, %sanswer_permission, %sabort\n", pluginTool, pluginTool, pluginTool, pluginTool)
	}
	b.WriteString("---\n")
	b.WriteString(shortcutMark + "\n")
	fmt.Fprintf(&b, "<!-- p0ssess model: %s %s -->\n", s.Model, s.Route)
	b.WriteString("You are a pipe to another model. You never do the task, answer it, summarize it or comment on it.\n\n")
	if s.Route == "direct" {
		fmt.Fprintf(&b, "Call `%sdirect` once with `model` %q, `prompt` set to the message text copied exactly (drop only the cost limit and instructions about reporting back), `files` set to the absolute paths the message lists, and `max_cost_usd` set to the limit the message gives. If the message gives no limit, call it with `dry_run: true` instead and report the estimate.\n\n", pluginTool, s.Model)
		b.WriteString("Reply with the tool result exactly as it came back: the <answer> block first, then the lines after it, nothing else.\n")
	} else {
		fmt.Fprintf(&b, "Call `%srun` once with `model` %q, `task` set to the message text copied exactly (drop only routing words and instructions about reporting back), `mode` \"edit\" when the message says edit mode or asks for file changes (else \"read\"), and `session` when an earlier result in this conversation has a `session_id`.\n", pluginTool, s.Model)
		fmt.Fprintf(&b, "If `status` is `still_running`, call `%swait` with the session until it is not. If `status` is `needs_permission`, stop and report it. If a later message says to approve or reject, call `%sanswer_permission` with `once` or `reject`; to stop, call `%sabort`.\n\n", pluginTool, pluginTool, pluginTool)
		b.WriteString("Reply with exactly:\n<answer>\n…the result's `answer`, copied character for character…\n</answer>\nsession: <session_id>, status: <status>, model: <model>, cost: $<cost_usd>\n")
		b.WriteString("Then, only when present: `worktree: <worktree>` and `diff: <diff_path>` lines and the result's `diff` in a ```diff block; `permission: <request_id> <action> <resources>`; `error: <error>`.\n")
	}
	return b.String()
}

func yamlQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func addShortcutTools(s *mcp.Server, e *engine.Engine) {
	obj := func(props map[string]any, req ...string) json.RawMessage {
		m := map[string]any{"type": "object", "properties": props}
		if len(req) > 0 {
			m["required"] = req
		}
		b, _ := json.Marshal(m)
		return b
	}
	text := func(v any, err error) mcp.CallResult {
		if err != nil {
			return mcp.Error(err.Error())
		}
		b, _ := json.MarshalIndent(v, "", " ")
		return mcp.Text(string(b))
	}
	s.AddTool(mcp.Tool{Name: "shortcuts_suggest", Description: "Named shortcut agents p0ssess made, and ones it suggests from routes the user " +
		"reliably accepts. Suggestions are never created automatically; ask the user, then call shortcuts_create.",
		InputSchema: obj(map[string]any{}), Annotations: map[string]any{"readOnlyHint": true}},
		func(ctx context.Context, _ json.RawMessage, _ mcp.Progress) mcp.CallResult {
			dir := AgentsDir()
			return text(map[string]any{"directory": dir, "existing": ListShortcuts(dir), "suggested": SuggestShortcuts(e, dir), "cap": MaxShortcuts}, nil)
		})
	s.AddTool(mcp.Tool{Name: "shortcuts_create", Description: "Write a named shortcut agent (a relay with a fixed model) into the user's " +
		"Claude Code agent directory. Only after the user agrees. At most 12. dry_run shows the file without writing it.",
		InputSchema: obj(map[string]any{
			"name":        map[string]any{"type": "string", "description": "short, e.g. gemini-video; the agent is named p0ssess-<name>"},
			"model":       map[string]any{"type": "string", "description": "OpenRouter model id"},
			"route":       map[string]any{"type": "string", "enum": []string{"direct", "agent"}, "description": "direct for one-shot and media jobs, agent for multi-step work on files"},
			"description": map[string]any{"type": "string"},
			"dry_run":     map[string]any{"type": "boolean"},
		}, "name", "model", "route")},
		func(ctx context.Context, raw json.RawMessage, _ mcp.Progress) mcp.CallResult {
			var a struct {
				Shortcut
				Description string `json:"description"`
				DryRun      bool   `json:"dry_run"`
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return mcp.Error(err.Error())
			}
			path, body, err := CreateShortcut(AgentsDir(), a.Shortcut, a.Description, a.DryRun)
			status := "written; it is available in new sessions (or after /agents reloads)"
			if a.DryRun {
				status = "not written (dry run)"
			}
			return text(map[string]any{"path": path, "status": status, "content": body}, err)
		})
	s.AddTool(mcp.Tool{Name: "shortcuts_remove", Description: "Remove a shortcut agent p0ssess made.",
		InputSchema: obj(map[string]any{"name": map[string]any{"type": "string"}}, "name")},
		func(ctx context.Context, raw json.RawMessage, _ mcp.Progress) mcp.CallResult {
			var a struct {
				Name string `json:"name"`
			}
			json.Unmarshal(raw, &a)
			for _, x := range ListShortcuts(AgentsDir()) {
				if x.Name == a.Name {
					return text(map[string]string{"removed": x.Path}, os.Remove(x.Path))
				}
			}
			return mcp.Error("no p0ssess shortcut named " + a.Name)
		})
}
