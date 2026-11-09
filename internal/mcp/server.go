// Package mcp is a minimal Model Context Protocol server over stdio (newline-delimited JSON-RPC 2.0):
// tools, progress notifications and cancellation. Nothing else is needed by the relay.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

type Tool struct {
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations map[string]any  `json:"annotations,omitempty"`
	Meta        map[string]any  `json:"_meta,omitempty"`
}

type Content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type CallResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

func Text(s string) CallResult { return CallResult{Content: []Content{{Type: "text", Text: s}}} }
func Error(s string) CallResult {
	return CallResult{Content: []Content{{Type: "text", Text: s}}, IsError: true}
}

// Progress sends a progress notification for the current call; it is a no-op without a progress token.
type Progress func(progress float64, message string)

type Handler func(ctx context.Context, args json.RawMessage, progress Progress) CallResult

type Server struct {
	Name, Version, Instructions string
	tools                       []Tool
	handlers                    map[string]Handler

	out    io.Writer
	outMu  sync.Mutex
	mu     sync.Mutex
	active map[string]context.CancelFunc
	wg     sync.WaitGroup
}

func NewServer(name, version string) *Server {
	return &Server{Name: name, Version: version, handlers: map[string]Handler{}, active: map[string]context.CancelFunc{}}
}

func (s *Server) AddTool(t Tool, h Handler) {
	s.tools = append(s.tools, t)
	s.handlers[t.Name] = h
}

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (s *Server) send(m message) {
	m.JSONRPC = "2.0"
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	s.outMu.Lock()
	defer s.outMu.Unlock()
	s.out.Write(append(b, '\n'))
}

func (s *Server) reply(id json.RawMessage, result any) { s.send(message{ID: id, Result: result}) }

func (s *Server) fail(id json.RawMessage, code int, msg string) {
	s.send(message{ID: id, Error: &rpcError{code, msg}})
}

var supportedVersions = map[string]bool{"2024-11-05": true, "2025-03-26": true, "2025-06-18": true, "2025-11-25": true}

// Serve reads requests until in closes or ctx ends. In-flight calls are then cancelled and awaited, so
// handlers can clean up (the relay interrupts OpenCode runs).
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	s.out = out
	ctx, cancelAll := context.WithCancel(ctx)
	defer func() {
		cancelAll()
		s.wg.Wait()
	}()
	lines := make(chan []byte)
	errc := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(in)
		sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
		for sc.Scan() {
			b := append([]byte(nil), sc.Bytes()...)
			select {
			case lines <- b:
			case <-ctx.Done():
				return
			}
		}
		errc <- sc.Err()
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errc:
			return err
		case line := <-lines:
			if len(line) == 0 {
				continue
			}
			var m message
			if err := json.Unmarshal(line, &m); err != nil {
				s.fail(json.RawMessage("null"), -32700, "parse error")
				continue
			}
			s.dispatch(ctx, m)
		}
	}
}

func (s *Server) dispatch(ctx context.Context, m message) {
	isRequest := len(m.ID) > 0 && string(m.ID) != "null"
	switch m.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(m.Params, &p)
		v := p.ProtocolVersion
		if !supportedVersions[v] {
			v = "2025-06-18"
		}
		res := map[string]any{
			"protocolVersion": v,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": s.Name, "version": s.Version},
		}
		if s.Instructions != "" {
			res["instructions"] = s.Instructions
		}
		s.reply(m.ID, res)
	case "ping":
		s.reply(m.ID, map[string]any{})
	case "tools/list":
		s.reply(m.ID, map[string]any{"tools": s.tools})
	case "tools/call":
		s.call(ctx, m)
	case "notifications/cancelled":
		var p struct {
			RequestID json.RawMessage `json:"requestId"`
		}
		_ = json.Unmarshal(m.Params, &p)
		s.mu.Lock()
		if c, ok := s.active[string(p.RequestID)]; ok {
			c()
		}
		s.mu.Unlock()
	case "notifications/initialized", "notifications/roots/list_changed":
	default:
		if isRequest {
			s.fail(m.ID, -32601, "method not found: "+m.Method)
		}
	}
}

func (s *Server) call(ctx context.Context, m message) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Meta      struct {
			ProgressToken json.RawMessage `json:"progressToken"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(m.Params, &p); err != nil {
		s.fail(m.ID, -32602, "invalid params")
		return
	}
	h, ok := s.handlers[p.Name]
	if !ok {
		s.fail(m.ID, -32602, fmt.Sprintf("unknown tool %q", p.Name))
		return
	}
	cctx, cancel := context.WithCancel(ctx)
	key := string(m.ID)
	s.mu.Lock()
	s.active[key] = cancel
	s.mu.Unlock()
	token := p.Meta.ProgressToken
	var progress Progress = func(float64, string) {}
	if len(token) > 0 && string(token) != "null" {
		progress = func(v float64, msg string) {
			s.send(message{Method: "notifications/progress", Params: mustJSON(map[string]any{
				"progressToken": token, "progress": v, "message": msg,
			})})
		}
	}
	args := p.Arguments
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			delete(s.active, key)
			s.mu.Unlock()
			cancel()
		}()
		res := h(cctx, args, progress)
		if res.Content == nil {
			res.Content = []Content{}
		}
		s.reply(m.ID, res)
	}()
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// Wrap replaces the handler of a registered tool with wrap(old). It reports false when the tool is missing.
func (s *Server) Wrap(name string, wrap func(Handler) Handler) bool {
	h, ok := s.handlers[name]
	if !ok {
		return false
	}
	s.handlers[name] = wrap(h)
	return true
}
