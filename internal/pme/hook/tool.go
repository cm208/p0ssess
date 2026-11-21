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
	"When the result arrives, reply with only its answer (shaped to what the user asked, never longer) and its receipt line, " +
	"for example \"(kimi-k3.1-pro, $0.10, saved ~$0.80)\", and print any file paths it lists without opening them. No intro, no caveats, no offers. " +
	"If it says the changes were applied, check them once with git diff, in proportion to the stakes; if it has an apply line instead, run it and check once, as the routing skill says. " +
	"If it returns an error, send the agent the request again with the fix the error names, or tell the user the gap in one line; " +
	"never do the work yourself unless the user says so."

func agentGoneFile(d paths.Dirs, session string) string {
	return filepath.Join(filepath.Dir(d.SessionRulesFile(session)), "agent-gone")
}

func deniedFile(d paths.Dirs, session, tool string) string {
	return filepath.Join(filepath.Dir(d.SessionRulesFile(session)), "denied-"+tool)
}

func touch(f string) {
	if os.MkdirAll(filepath.Dir(f), 0o700) == nil {
		os.WriteFile(f, nil, 0o600)
	}
}

// finishers are the tools that end or answer a paused run; a run that waits on them cannot be left stuck.
var finishers = map[string]bool{"answer_permission": true, "abort": true}

func mainCallsFile(d paths.Dirs, session string) string {
	return filepath.Join(filepath.Dir(d.SessionRulesFile(session)), "main-calls")
}

// markMainCalls records whether the user's latest prompt asked for p0ssess in the main session.
func markMainCalls(d paths.Dirs, session, prompt string) {
	for t := range finishers {
		os.Remove(deniedFile(d, session, t))
	}
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
	if in.Event == "PostToolUseFailure" {
		// Starting the p0ssess agent failed (its type is not registered in this session): the routed tools
		// are open to the main conversation from here on.
		if (in.ToolName == "Agent" || in.ToolName == "Task") && in.AgentID == "" && strings.HasPrefix(in.ToolInput.SubagentType, "p0ssess") && in.SessionID != "" {
			touch(agentGoneFile(d, in.SessionID))
		}
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
		if _, err := os.Stat(agentGoneFile(d, in.SessionID)); err == nil {
			return ""
		}
		if finishers[name] {
			// The first attempt is sent to the agent. A second one means the agent could not take it (its
			// type is missing after a restart or resume), and a paused run must not be left stuck.
			f := deniedFile(d, in.SessionID, name)
			if _, err := os.Stat(f); err == nil {
				return ""
			}
			touch(f)
		}
	}
	reason := "p0ssess runs routed work in its own agent so this conversation stays small. " +
		`Hand this request to it instead: Agent tool, subagent_type "p0ssess:p0ssess", one sentence with the request ` +
		"and absolute file paths. Say nothing to the user about this."
	if finishers[name] {
		reason += " If that agent type is not available in this session, call this same tool again and it will go through, " +
			"or run the same call from a shell: p0ssess answer --session SESSION --request REQUEST --decision once|reject, " +
			"or p0ssess abort --session SESSION."
	}
	out, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName":            "PreToolUse",
		"permissionDecision":       "deny",
		"permissionDecisionReason": reason,
	}})
	return string(out)
}
