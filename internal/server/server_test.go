package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"p0ssess/internal/pme/engine"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/relay"
	"p0ssess/internal/usage"
)

func TestShortcuts(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "p0ssess-mine.md"), []byte("---\nname: p0ssess-mine\n---\nmy own agent\n"), 0o644)
	if _, _, err := CreateShortcut(dir, Shortcut{Name: "mine", Model: "google/gemini-3.8-flash", Route: "direct"}, "", false); err == nil {
		t.Error("overwrote a file p0ssess did not make")
	}
	for i := 0; i < MaxShortcuts; i++ {
		if _, _, err := CreateShortcut(dir, Shortcut{Name: fmt.Sprintf("s%d", i), Model: "google/gemini-3.8-flash", Route: "direct"}, "", false); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := CreateShortcut(dir, Shortcut{Name: "one-more", Model: "z-ai/glm-5.3-flash", Route: "agent"}, "", false); err == nil {
		t.Error("cap not enforced")
	}
	if _, _, err := CreateShortcut(dir, Shortcut{Name: "s0", Model: "z-ai/glm-5.3-flash", Route: "agent"}, "", false); err != nil {
		t.Errorf("replacing a shortcut at the cap: %v", err)
	}
	list := ListShortcuts(dir)
	if len(list) != MaxShortcuts {
		t.Fatalf("listed %d", len(list))
	}
	b, _ := os.ReadFile(filepath.Join(dir, "p0ssess-s0.md"))
	s := string(b)
	for _, want := range []string{"name: p0ssess-s0\n", "background: true", "omitClaudeMd: true", "tools: " + pluginTool + "run, ", `"z-ai/glm-5.3-flash"`} {
		if !strings.Contains(s, want) {
			t.Errorf("agent file lacks %q:\n%s", want, s)
		}
	}
	if _, _, err := CreateShortcut(dir, Shortcut{Name: "Bad Name", Model: "x/y", Route: "direct"}, "", true); err == nil {
		t.Error("bad name accepted")
	}
}

type pipe struct {
	in  *io.PipeWriter
	out chan map[string]any
}

func start(t *testing.T, opt Options) *pipe {
	t.Helper()
	s, closeFn, err := New(opt)
	if err != nil {
		t.Fatal(err)
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	p := &pipe{in: inW, out: make(chan map[string]any, 16)}
	go s.Serve(context.Background(), inR, outW)
	go func() {
		dec := json.NewDecoder(outR)
		for {
			var m map[string]any
			if dec.Decode(&m) != nil {
				return
			}
			if m["id"] != nil {
				p.out <- m
			}
		}
	}()
	t.Cleanup(func() { inW.Close(); closeFn() })
	return p
}

func (p *pipe) call(t *testing.T, id int, tool string, args any) (string, bool) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
	p.in.Write(append(b, '\n'))
	select {
	case m := <-p.out:
		r, _ := m["result"].(map[string]any)
		if r == nil {
			t.Fatalf("%s: %v", tool, m)
		}
		c := r["content"].([]any)[0].(map[string]any)
		isErr, _ := r["isError"].(bool)
		return c["text"].(string), isErr
	case <-time.After(20 * time.Second):
		t.Fatal("timeout")
	}
	return "", false
}

func TestServerAgentUnavailableAndDirectCap(t *testing.T) {
	home := t.TempDir()
	t.Setenv("P0SSESS_HOME", home)
	t.Setenv("P0SSESS_OPENROUTER_KEY_FILE", filepath.Join(home, "key"))
	os.WriteFile(filepath.Join(home, "key"), []byte("placeholder\n"), 0o600)
	os.WriteFile(filepath.Join(home, "budget.toml"), []byte("monthly_usd = 0.01\n"), 0o600)
	p := start(t, Options{Version: "test", Log: io.Discard, Agent: func() error { return errors.New("it needs Linux with rootless podman (this is darwin)") }})
	text, isErr := p.call(t, 1, "run", map[string]any{"task": "x", "model": "a/b"})
	if !isErr || !strings.Contains(text, "unavailable here") || !strings.Contains(text, "direct tool") {
		t.Errorf("run: %v %s", isErr, text)
	}
	if text, _ := p.call(t, 2, "models", map[string]any{"query": "deepseek", "limit": 2}); !strings.Contains(text, "deepseek/") {
		t.Errorf("models should work without the agent path: %s", text)
	}
	text, isErr = p.call(t, 3, "direct", map[string]any{"model": "google/gemini-3.8-flash", "prompt": "hi", "max_cost_usd": 0.05})
	if !isErr || !strings.Contains(text, "monthly cap") {
		t.Errorf("direct over the cap: %v %s", isErr, text)
	}
	if text, isErr := p.call(t, 4, "budget_status", map[string]any{}); isErr || !strings.Contains(text, "monthly_usd") {
		t.Errorf("budget_status: %s", text)
	}
}

func TestObserverRecordsRuns(t *testing.T) {
	t.Setenv("P0SSESS_HOME", t.TempDir())
	e, err := engine.Load(paths.Default())
	if err != nil {
		t.Fatal(err)
	}
	o := &observer{eng: e, jobs: map[string]string{}}
	info := relay.RunInfo{SessionID: "ses_1", Turn: 1, Model: "deepseek/deepseek-v4.1-flash", Mode: "read"}
	o.RunStarted(info)
	if n := len(usage.Running(e.Dirs.RunningDir(), time.Now())); n != 1 {
		t.Fatalf("running = %d", n)
	}
	o.RunFinished(info, &relay.Result{Status: "done", CostUSD: 0.0019})
	if n := len(usage.Running(e.Dirs.RunningDir(), time.Now())); n != 0 {
		t.Fatalf("running after finish = %d", n)
	}
	st, _ := e.Status(engine.Scope{})
	if st.Spend.Runs != 1 || st.Spend.Today != 0.0019 {
		t.Errorf("spend = %+v", st.Spend)
	}
	if c, ok := e.Ledger().RunCost("ses_1"); !ok || c != 0.0019 {
		t.Errorf("session run cost = %v", c)
	}
}
