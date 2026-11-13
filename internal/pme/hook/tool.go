package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"p0ssess/internal/pme/paths"
)

// Routed are the tools that hand work to another model. They belong to the p0ssess agent, which runs on
// a small model, so their raw results, transcripts and diffs never fill the main conversation.
var Routed = map[string]bool{"direct": true, "run": true, "wait": true, "abort": true, "answer_permission": true,
	"recommend": true, "models": true}

const toolPrefix = "mcp__plugin_p0ssess_p0ssess__"

// mainCallsRe is a user explicitly asking for p0ssess in the main session.
var mainCallsRe = regexp.MustCompile(`(?i)\b(in (the|this) main (session|conversation|thread|context)|without (the |a )?(sub-?agent|p0ssess agent)|(do not|don't) use (the |a )?(sub-?agent|p0ssess agent)|(call|use|run) (p0ssess|the direct tool|direct|recommend)( tools?)? (yourself|directly|here))\b`)

// ToolInput is the part of the PreToolUse hook input this hook reads.
type ToolInput struct {
	Event     string `json:"hook_event_name"`
	SessionID string `json:"session_id"`
	ToolName  string `json:"tool_name"`
	AgentID   string `json:"agent_id"`
	AgentType string `json:"agent_type"`
	ToolInput struct {
		SubagentType string `json:"subagent_type"`
	} `json:"tool_input"`
}

// handedOff is what Claude reads right after it starts the p0ssess agent: the turn ends quietly and the
// result is relayed without talk around it.
const handedOff = "p0ssess: the agent is running and its result arrives on its own. End this turn now without writing anything. " +
	"When the result arrives, reply with only its answer (shaped to what the user asked, never longer) and \"(model, cost)\" " +
	"from its via line, and print any file paths it lists without opening them. No intro, no caveats, no offers. " +
	"If it has an apply line, run it and check the change once, in proportion to its stakes, as the routing skill says."

func mainCallsFile(d paths.Dirs, session string) string {
	return filepath.Join(filepath.Dir(d.SessionRulesFile(session)), "main-calls")
}

// markMainCalls records whether the user's latest prompt asked for p0ssess in the main session.
func markMainCalls(d paths.Dirs, session, prompt string) {
	f := mainCallsFile(d, session)
	if !mainCallsRe.MatchString(prompt) {
		os.Remove(f)
		return
	}
	if os.MkdirAll(filepath.Dir(f), 0o700) == nil {
		os.WriteFile(f, nil, 0o600)
	}
}

// Tool is the PreToolUse hook for the routed tools. A call from a subagent passes. A call from the main
// conversation is denied, with a reason that tells Claude to hand the request to the p0ssess agent,
// unless the user's latest prompt asked for p0ssess in the main session. It returns the hook's JSON
// output, or "" to let the call through.
func Tool(raw []byte, d paths.Dirs) string {
	var in ToolInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return ""
	}
	if in.Event == "PostToolUse" {
		if (in.ToolName == "Agent" || in.ToolName == "Task") && in.AgentID == "" && strings.HasPrefix(in.ToolInput.SubagentType, "p0ssess") {
			out, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
				"hookEventName": "PostToolUse", "additionalContext": handedOff}})
			return string(out)
		}
		return ""
	}
	name := strings.TrimPrefix(in.ToolName, toolPrefix)
	// agent_id is set only inside a subagent or teammate; agent_type alone also marks a main thread
	// started with --agent.
	if !Routed[name] || in.AgentID != "" {
		return ""
	}
	if in.SessionID != "" {
		if _, err := os.Stat(mainCallsFile(d, in.SessionID)); err == nil {
			return ""
		}
	}
	out, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName":      "PreToolUse",
		"permissionDecision": "deny",
		"permissionDecisionReason": "p0ssess runs routed work in its own agent so this conversation stays small. " +
			`Hand this request to it instead: Agent tool, subagent_type "p0ssess:p0ssess", one sentence with the request ` +
			"and absolute file paths. Say nothing to the user about this.",
	}})
	return string(out)
}
