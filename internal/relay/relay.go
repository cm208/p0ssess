package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"p0ssess/internal/config"
	"p0ssess/internal/mcp"
)

const instructions = `p0ssess runs a task on another model (any OpenRouter model) inside a sandboxed OpenCode session.
Call run once per task; results put the answer first. Continue a session by passing its session id.
Edit tasks work in a separate git worktree and return a diff to review; nothing is merged.`

var runSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "task": {"type": "string", "description": "The task, exactly as the caller wrote it."},
    "model": {"type": "string", "description": "OpenRouter model id, e.g. deepseek/deepseek-v4.1-flash. Optional when continuing a session."},
    "mode": {"type": "string", "enum": ["read", "edit"], "description": "read (default): no file changes. edit: works in a new git worktree and returns a diff."},
    "cwd": {"type": "string", "description": "Working directory. Defaults to the Claude Code project directory."},
    "attachments": {"type": "array", "items": {"type": "string"}, "description": "Absolute paths of text, PDF or image files to attach."},
    "session": {"type": "string", "description": "A session id from an earlier result, to continue that session."},
    "kind": {"type": "string", "description": "Optional short task kind for the local history, e.g. tests, summary, refactor."},
    "wait_s": {"type": "integer", "description": "Return still_running after this many seconds (default 1500); then call wait."}
  },
  "required": ["task"]
}`)

var waitSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "session": {"type": "string"},
    "wait_s": {"type": "integer", "description": "Maximum seconds to block (default 1500)."}
  },
  "required": ["session"]
}`)

var abortSchema = json.RawMessage(`{
  "type": "object",
  "properties": {"session": {"type": "string"}},
  "required": ["session"]
}`)

var answerSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "session": {"type": "string"},
    "request_id": {"type": "string", "description": "permission.request_id from the needs_permission result."},
    "decision": {"type": "string", "enum": ["once", "reject"]},
    "message": {"type": "string", "description": "Optional feedback for the model, e.g. why it was rejected."},
    "wait_s": {"type": "integer"}
  },
  "required": ["session", "request_id", "decision"]
}`)

var modelsSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "query": {"type": "string", "description": "Substring filter on id, name or modality, e.g. deepseek, video."},
    "live": {"type": "boolean", "description": "Fetch the live OpenRouter list instead of the local registry snapshot."},
    "limit": {"type": "integer"}
  }
}`)

func textResult(v any, err error) mcp.CallResult {
	if err != nil {
		return mcp.Error(err.Error())
	}
	b, _ := json.MarshalIndent(v, "", " ")
	return mcp.Text(string(b))
}

// NewServer wires the engine into an MCP server.
func NewServer(e *Engine, version string) *mcp.Server {
	s := mcp.NewServer("p0ssess", version)
	s.Instructions = instructions
	s.AddTool(mcp.Tool{Name: "run", Description: "Run a task on an OpenRouter model in a sandboxed OpenCode session and " +
		"block until it finishes. Returns the answer first, then session_id, status, cost, tokens and " +
		"transcript_path; edit mode also returns the worktree and diff. status is done, failed, interrupted, " +
		"needs_permission (call answer_permission after the caller decides), still_running (call wait), or " +
		"tampered (the session was acted on by something other than the relay; it was aborted, do not trust it).",
		InputSchema: runSchema},
		func(ctx context.Context, args json.RawMessage, p mcp.Progress) mcp.CallResult {
			var req RunRequest
			if err := json.Unmarshal(args, &req); err != nil {
				return mcp.Error("invalid arguments: " + err.Error())
			}
			return textResult(e.Run(ctx, req, p))
		})
	s.AddTool(mcp.Tool{Name: "wait", Description: "Keep waiting on a session whose run returned still_running.", InputSchema: waitSchema},
		func(ctx context.Context, args json.RawMessage, p mcp.Progress) mcp.CallResult {
			var a struct {
				Session string `json:"session"`
				WaitS   int    `json:"wait_s"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return mcp.Error("invalid arguments: " + err.Error())
			}
			return textResult(e.Wait(ctx, a.Session, a.WaitS, p))
		})
	s.AddTool(mcp.Tool{Name: "abort", Description: "Interrupt a running session.", InputSchema: abortSchema},
		func(ctx context.Context, args json.RawMessage, p mcp.Progress) mcp.CallResult {
			var a struct {
				Session string `json:"session"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return mcp.Error("invalid arguments: " + err.Error())
			}
			return textResult(e.Abort(ctx, a.Session))
		})
	s.AddTool(mcp.Tool{Name: "answer_permission", Description: "Answer a paused edit task's permission request with once or " +
		"reject (never always), then keep waiting like wait.", InputSchema: answerSchema},
		func(ctx context.Context, args json.RawMessage, p mcp.Progress) mcp.CallResult {
			var a struct {
				Session   string `json:"session"`
				RequestID string `json:"request_id"`
				Decision  string `json:"decision"`
				Message   string `json:"message"`
				WaitS     int    `json:"wait_s"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return mcp.Error("invalid arguments: " + err.Error())
			}
			return textResult(e.Answer(ctx, a.Session, a.RequestID, a.Decision, a.Message, a.WaitS, p))
		})
	s.AddTool(mcp.Tool{Name: "models", Description: "List OpenRouter models with context size, price per million tokens " +
		"and modalities, from the local registry snapshot or live.", InputSchema: modelsSchema,
		Annotations: map[string]any{"readOnlyHint": true}},
		func(ctx context.Context, args json.RawMessage, p mcp.Progress) mcp.CallResult {
			var req ModelsRequest
			if err := json.Unmarshal(args, &req); err != nil {
				return mcp.Error("invalid arguments: " + err.Error())
			}
			list, source, err := e.Models(ctx, req)
			return textResult(map[string]any{"source": source, "models": list}, err)
		})
	return s
}

// Serve runs the relay MCP server on stdio. When stdin closes, active runs are interrupted.
func Serve(ctx context.Context, in io.Reader, out io.Writer, version string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	e := NewEngine(cfg, version, os.Stderr)
	defer e.Shutdown()
	err = NewServer(e, version).Serve(ctx, in, out)
	if err == io.EOF || err == context.Canceled {
		return nil
	}
	if err != nil {
		return fmt.Errorf("mcp: %w", err)
	}
	return nil
}
