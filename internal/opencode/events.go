package opencode

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
)

// Event is one frame of /api/event or of the durable session log.
type Event struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Created  float64         `json:"created,omitempty"`
	Data     json.RawMessage `json:"data"`
	Durable  *Durable        `json:"durable,omitempty"`
	Location *Location       `json:"location,omitempty"`
	// Seq is set on the log's "log.synced" marker: the newest durable sequence of the aggregate.
	Seq *int64 `json:"seq,omitempty"`
}

// EvLogSynced marks the end of the replayed part of a session log. In OpenCode 2.2.5 the log endpoint
// yields only this marker (its durable-event filter drops every session event), so the relay uses it for
// the current sequence number and takes live events from /api/event.
const EvLogSynced = "log.synced"

type Durable struct {
	AggregateID string `json:"aggregateID"`
	Seq         int64  `json:"seq"`
	Version     int    `json:"version"`
}

const (
	EvServerConnected      = "server.connected"
	EvExecutionStarted     = "session.execution.started"
	EvExecutionSucceeded   = "session.execution.succeeded"
	EvExecutionFailed      = "session.execution.failed"
	EvExecutionInterrupted = "session.execution.interrupted"
	EvStepStarted          = "session.step.started"
	EvStepEnded            = "session.step.ended"
	EvStepFailed           = "session.step.failed"
	EvTextEnded            = "session.text.ended"
	EvToolInputStarted     = "session.tool.input.started"
	EvToolCalled           = "session.tool.called"
	EvToolSuccess          = "session.tool.success"
	EvToolFailed           = "session.tool.failed"
	EvRetryScheduled       = "session.retry.scheduled"
	EvUsageUpdated         = "session.usage.updated"
	EvPermissionAsked      = "permission.asked"
	EvPermissionReplied    = "permission.replied"
	EvFormCreated          = "form.created"
	EvSessionStatus        = "session.status"
)

// SessionRef is the part every session event carries.
type SessionRef struct {
	SessionID string `json:"sessionID"`
}

type ExecutionFailed struct {
	SessionID string           `json:"sessionID"`
	Error     *StructuredError `json:"error,omitempty"`
}

type ExecutionInterrupted struct {
	SessionID string `json:"sessionID"`
	Reason    string `json:"reason"`
}

type StepEnded struct {
	SessionID          string  `json:"sessionID"`
	AssistantMessageID string  `json:"assistantMessageID"`
	Finish             string  `json:"finish"`
	Cost               float64 `json:"cost"`
	Tokens             Tokens  `json:"tokens"`
}

type StepFailed struct {
	SessionID string           `json:"sessionID"`
	Error     *StructuredError `json:"error,omitempty"`
}

type TextEnded struct {
	SessionID          string `json:"sessionID"`
	AssistantMessageID string `json:"assistantMessageID"`
	Ordinal            int    `json:"ordinal"`
	Text               string `json:"text"`
}

type ToolInputStarted struct {
	SessionID string `json:"sessionID"`
	ID        string `json:"id"`
	Name      string `json:"name"`
}

type ToolCalled struct {
	SessionID string         `json:"sessionID"`
	ID        string         `json:"id"`
	Input     map[string]any `json:"input"`
}

type ToolResult struct {
	SessionID string           `json:"sessionID"`
	ID        string           `json:"id"`
	Content   []ToolContent    `json:"content,omitempty"`
	Error     *StructuredError `json:"error,omitempty"`
}

type RetryScheduled struct {
	SessionID string           `json:"sessionID"`
	Attempt   int              `json:"attempt"`
	Error     *StructuredError `json:"error,omitempty"`
}

type UsageUpdated struct {
	SessionID string  `json:"sessionID"`
	Cost      float64 `json:"cost"`
	Tokens    Tokens  `json:"tokens"`
}

// PermissionReplied is the payload of a permission.replied event: a request was answered.
type PermissionReplied struct {
	SessionID string `json:"sessionID"`
	RequestID string `json:"requestID"`
}

// SessionID extracts data.sessionID, or "" when the event has none.
func (e Event) SessionID() string {
	var r SessionRef
	_ = json.Unmarshal(e.Data, &r)
	return r.SessionID
}

// Decode unmarshals the event payload into v.
func (e Event) Decode(v any) error { return json.Unmarshal(e.Data, v) }

// IsTerminal reports whether the event ends an execution.
func (e Event) IsTerminal() bool {
	switch e.Type {
	case EvExecutionSucceeded, EvExecutionFailed, EvExecutionInterrupted:
		return true
	}
	return false
}

// Frame is one raw server-sent event.
type Frame struct {
	ID    string
	Event string
	Data  string
}

// ReadFrames parses an SSE stream and calls fn per frame until EOF, an error, or fn returns false.
// Comment lines (": heartbeat") are reported as a frame with Event "comment" so callers can track liveness.
func ReadFrames(r io.Reader, fn func(Frame) bool) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var f Frame
	var data []string
	flush := func() bool {
		if len(data) == 0 && f.Event == "" && f.ID == "" {
			return true
		}
		f.Data = strings.Join(data, "\n")
		ok := fn(f)
		f, data = Frame{}, nil
		return ok
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if !flush() {
				return nil
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			if !fn(Frame{Event: "comment", Data: strings.TrimSpace(line[1:])}) {
				return nil
			}
			continue
		}
		k, v, _ := strings.Cut(line, ":")
		v = strings.TrimPrefix(v, " ")
		switch k {
		case "data":
			data = append(data, v)
		case "event":
			f.Event = v
		case "id":
			f.ID = v
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	flush()
	return nil
}

// ParseEvent decodes a frame's data as an Event. Log frames carry the durable event itself.
func ParseEvent(f Frame) (Event, bool) {
	if f.Event == "comment" || f.Data == "" {
		return Event{}, false
	}
	var e Event
	if err := json.Unmarshal([]byte(f.Data), &e); err != nil || e.Type == "" {
		return Event{}, false
	}
	return e, true
}
