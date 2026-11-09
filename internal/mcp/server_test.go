package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

type client struct {
	in  *io.PipeWriter
	out *bufio.Scanner
}

func (c *client) send(t *testing.T, s string) {
	t.Helper()
	if _, err := c.in.Write([]byte(s + "\n")); err != nil {
		t.Fatal(err)
	}
}

func (c *client) next(t *testing.T) map[string]any {
	t.Helper()
	if !c.out.Scan() {
		t.Fatal("no output")
	}
	var m map[string]any
	if err := json.Unmarshal(c.out.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func start(t *testing.T) (*client, chan error) {
	s := NewServer("test", "0")
	s.AddTool(Tool{Name: "slow", Description: "d", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(ctx context.Context, args json.RawMessage, p Progress) CallResult {
			p(1, "working")
			<-ctx.Done()
			return Text("cancelled")
		})
	s.AddTool(Tool{Name: "echo", Description: "d", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(ctx context.Context, args json.RawMessage, p Progress) CallResult { return Text(string(args)) })
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	errc := make(chan error, 1)
	go func() { errc <- s.Serve(context.Background(), inR, outW); outW.Close() }()
	return &client{in: inW, out: bufio.NewScanner(outR)}, errc
}

func TestProtocol(t *testing.T) {
	c, errc := start(t)
	c.send(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{}}}`)
	m := c.next(t)
	if m["result"].(map[string]any)["protocolVersion"] != "2025-06-18" {
		t.Fatalf("%v", m)
	}
	c.send(t, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	c.send(t, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if tools := c.next(t)["result"].(map[string]any)["tools"].([]any); len(tools) != 2 {
		t.Fatalf("tools: %v", tools)
	}
	c.send(t, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"x":1}}}`)
	if txt := c.next(t)["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"]; txt != `{"x":1}` {
		t.Fatalf("echo: %v", txt)
	}

	// progress, then cancellation of the in-flight call
	c.send(t, `{"jsonrpc":"2.0","id":"a","method":"tools/call","params":{"name":"slow","arguments":{},"_meta":{"progressToken":7}}}`)
	p := c.next(t)
	if p["method"] != "notifications/progress" || p["params"].(map[string]any)["progressToken"].(float64) != 7 {
		t.Fatalf("progress: %v", p)
	}
	c.send(t, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":"a"}}`)
	r := c.next(t)
	if r["id"] != "a" || !strings.Contains(c.out.Text(), "cancelled") {
		t.Fatalf("cancel: %v", r)
	}

	// closing stdin cancels in-flight calls and returns
	c.send(t, `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"slow","arguments":{}}}`)
	time.Sleep(50 * time.Millisecond)
	c.in.Close()
	if r := c.next(t); r["id"].(float64) != 9 {
		t.Fatalf("stdin close: %v", r)
	}
	select {
	case <-errc:
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after stdin closed")
	}
}
