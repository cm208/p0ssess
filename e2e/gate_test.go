// Package e2e is the release gate: the owner's trials as real p0ssess runs against OpenRouter, on this
// machine, in a scratch P0SSESS_HOME so the user's own history and limits are untouched. It spends real
// money (well under a dollar), so it runs only when asked:
//
//	P0SSESS_E2E=1 P0SSESS_E2E_BIN=/path/to/p0ssess go test -v -count=1 -timeout 30m ./e2e/
//
// scripts/e2e-gate.sh builds both binaries and runs it. The key comes from $P0SSESS_OPENROUTER_KEY_FILE
// (or ~/.config/p0ssess/secrets/openrouter_key); the test never reads it, only hands its path on.
// P0SSESS_E2E_MAX_USD (default 1.00) is the scratch install's monthly cap, so p0ssess itself stops paid
// runs past it. The agent scenarios need rootless podman and the sandbox image.
package e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The owner's research prompt of 2026-11-16.
const researchPrompt = "give me a summary and news insight and brief market trend insight using consumer data and news " +
	"headlines from the past 6 months about consumer trends among AI users who don't code"

var receiptRe = regexp.MustCompile(`^\([^()\n]+, (about )?(\$[0-9.]+|under \$0\.0001)(, saved ~\$[0-9.]+)?\)$`)

type gate struct {
	bin, home, key, nongit, repo string
	podman                       bool
}

type result struct {
	Answer  string   `json:"answer"`
	Status  string   `json:"status"`
	Model   string   `json:"model"`
	Route   string   `json:"route"`
	CostUSD float64  `json:"cost_usd"`
	Receipt string   `json:"receipt"`
	Retries []string `json:"retries"`
	Error   string   `json:"error"`
	Applied []string `json:"applied"`
	Revert  string   `json:"revert"`
	Tests   string   `json:"tests"`
	RunID   string   `json:"run_id"`
	Session string   `json:"session_id"`
}

func setup(t *testing.T) *gate {
	if os.Getenv("P0SSESS_E2E") != "1" {
		t.Skip("set P0SSESS_E2E=1 to run the release gate (real OpenRouter runs)")
	}
	g := &gate{bin: os.Getenv("P0SSESS_E2E_BIN"), key: os.Getenv("P0SSESS_OPENROUTER_KEY_FILE")}
	if g.bin == "" {
		t.Fatal("set P0SSESS_E2E_BIN to the p0ssess binary under test")
	}
	if g.key == "" {
		home, _ := os.UserHomeDir()
		g.key = filepath.Join(home, ".config", "p0ssess", "secrets", "openrouter_key")
	}
	if st, err := os.Stat(g.key); err != nil || st.Size() == 0 {
		t.Fatalf("no OpenRouter key file at %s", g.key)
	}
	root, err := os.MkdirTemp("", "p0ssess-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("scratch: %s", root)
	g.home = filepath.Join(root, "home")
	g.nongit = filepath.Join(root, "notes")
	g.repo = filepath.Join(root, "calc")
	os.MkdirAll(g.home, 0o700)
	os.MkdirAll(g.nongit, 0o755)
	writeRepo(t, g.repo)
	_, err = exec.LookPath("podman")
	g.podman = err == nil
	cap := os.Getenv("P0SSESS_E2E_MAX_USD")
	if cap == "" {
		cap = "1.00"
	}
	g.must(t, g.nongit, nil, "engine", "set", "monthly_usd", cap)
	g.must(t, g.nongit, nil, "engine", "set", "daily_usd", cap)
	return g
}

func writeRepo(t *testing.T, dir string) {
	os.MkdirAll(dir, 0o755)
	files := map[string]string{
		"go.mod":       "module calc\n\ngo 1.27\n",
		"calc.go":      "package calc\n\n// Add returns the sum of a and b.\nfunc Add(a, b int) int { return a + b }\n",
		"calc_test.go": "package calc\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatal(\"Add(2, 3) != 5\")\n\t}\n}\n",
	}
	for n, c := range files {
		os.WriteFile(filepath.Join(dir, n), []byte(c), 0o644)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=e2e", "-c", "user.email=e2e@example.com", "commit", "-qm", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
}

func (g *gate) env(extra []string) []string {
	env := []string{}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "P0SSESS_") && !strings.HasPrefix(kv, "CLAUDE_") {
			env = append(env, kv)
		}
	}
	env = append(env, "P0SSESS_HOME="+g.home, "P0SSESS_OPENROUTER_KEY_FILE="+g.key)
	return append(env, extra...)
}

func (g *gate) p0ssess(t *testing.T, dir string, extra []string, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(g.bin, args...)
	cmd.Dir, cmd.Env = dir, g.env(extra)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

func (g *gate) must(t *testing.T, dir string, extra []string, args ...string) string {
	t.Helper()
	out, errs, err := g.p0ssess(t, dir, extra, args...)
	if err != nil {
		t.Fatalf("p0ssess %v: %v\n%s%s", args, err, out, errs)
	}
	return out
}

// run is `p0ssess run`, decoded; a refusal or plain failure comes back as Error.
func (g *gate) run(t *testing.T, dir string, extra []string, args ...string) result {
	t.Helper()
	out, errs, err := g.p0ssess(t, dir, extra, append([]string{"run"}, args...)...)
	var r result
	if jerr := json.Unmarshal([]byte(out), &r); jerr != nil {
		r.Status = "failed"
		r.Error = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(errs), "p0ssess: "))
		if err == nil {
			r.Error = "no JSON result: " + out
		}
	}
	t.Logf("%s · %s · %s", r.Status, r.Receipt, strings.Join(r.Retries, " | "))
	if r.Error != "" {
		t.Logf("error: %s", r.Error)
	}
	return r
}

// spent is the scratch install's measured spend, from its own ledger.
func (g *gate) spent(t *testing.T) float64 {
	b, err := os.ReadFile(filepath.Join(g.home, "state", "history.jsonl"))
	if err != nil {
		return 0
	}
	total := 0.0
	for _, line := range bytes.Split(b, []byte("\n")) {
		var e struct {
			Type string  `json:"type"`
			Cash float64 `json:"cash_usd"`
		}
		if json.Unmarshal(line, &e) == nil && e.Type == "run" {
			total += e.Cash
		}
	}
	return total
}

func quiet(t *testing.T, r result) {
	t.Helper()
	if r.Status != "done" || strings.TrimSpace(r.Answer) == "" {
		t.Fatalf("not answered: %+v", r)
	}
	if !receiptRe.MatchString(r.Receipt) {
		t.Errorf("receipt is not one line (model, cost[, saving]): %q", r.Receipt)
	}
}

func (g *gate) needPodman(t *testing.T) {
	if !g.podman {
		t.Skip("the agent path needs rootless podman")
	}
}

func TestGate(t *testing.T) {
	g := setup(t)
	defer func() { t.Logf("total spent by the gate: $%.4f", g.spent(t)) }()

	t.Run("research prompt routes with web search", func(t *testing.T) {
		r := g.run(t, g.nongit, nil, "--max-cost", "0.15", researchPrompt)
		quiet(t, r)
		if r.Route != "direct" || !strings.Contains(strings.Join(r.Retries, " "), "web search") {
			t.Errorf("not answered from the web: route %q, %v", r.Route, r.Retries)
		}
	})

	t.Run("model newer than the sandbox catalog", func(t *testing.T) {
		g.needPodman(t)
		model := os.Getenv("P0SSESS_E2E_NEW_MODEL")
		if model == "" {
			model = "thinkingmachines/inkling-2-mini"
		}
		r := g.run(t, g.repo, nil, "--model", model, "--max-cost", "0.05", "What does the function Add in calc.go return? One sentence.")
		quiet(t, r)
		if r.Model != model {
			t.Errorf("ran on %s, want %s", r.Model, model)
		}
	})

	t.Run("edit lands and its Go test runs", func(t *testing.T) {
		g.needPodman(t)
		r := g.run(t, g.repo, nil, "--mode", "edit", "--model", "deepseek/deepseek-v4.1-flash", "--max-cost", "0.15",
			"Add a function Sub(a, b int) int to calc.go that returns a - b, and a test TestSub for it in calc_test.go.")
		quiet(t, r)
		if len(r.Applied) == 0 || !strings.Contains(r.Tests, "passed") {
			t.Errorf("applied %v, tests %q", r.Applied, r.Tests)
		}
		if b, _ := os.ReadFile(filepath.Join(g.repo, "calc.go")); !strings.Contains(string(b), "func Sub(") {
			t.Errorf("calc.go has no Sub:\n%s", b)
		}
	})

	t.Run("a command behind VAR=value does not ask", func(t *testing.T) {
		g.needPodman(t)
		r := g.run(t, g.repo, nil, "--mode", "edit", "--model", "deepseek/deepseek-v4.1-flash", "--max-cost", "0.08",
			`In calc.go, change the comment on Add to "Add returns a plus b." Then run exactly this shell command to test: GOFLAGS=-mod=mod go test ./...`)
		quiet(t, r)
		if !strings.Contains(strings.Join(r.Retries, " "), "run it plainly") && !strings.Contains(r.Tests, "passed") {
			t.Errorf("retries %v, tests %q", r.Retries, r.Tests)
		}
	})

	t.Run("read from a directory that is not a repository", func(t *testing.T) {
		r := g.run(t, g.nongit, nil, "--max-cost", "0.03", "In one sentence, what does git rebase do?")
		quiet(t, r)
		if r.Route != "direct" {
			t.Errorf("route %q", r.Route)
		}
	})

	t.Run("edit from a directory that is not a repository", func(t *testing.T) {
		g.needPodman(t)
		r := g.run(t, g.nongit, nil, "--mode", "edit", "--model", "deepseek/deepseek-v4.1-flash", "--max-cost", "0.10",
			"Write a file named haiku.txt in "+g.nongit+" containing a three-line haiku about rain.")
		quiet(t, r)
		if b, err := os.ReadFile(filepath.Join(g.nongit, "haiku.txt")); err != nil || len(bytes.TrimSpace(b)) == 0 {
			t.Errorf("haiku.txt not written to %s: %v (applied %v)", g.nongit, err, r.Applied)
		}
	})

	t.Run("over-limit pick falls back inside the envelope", func(t *testing.T) {
		g.must(t, g.nongit, nil, "engine", "set", "per_task_usd", "0.03")
		defer g.must(t, g.nongit, nil, "engine", "set", "per_task_usd", "0.25")
		before := g.spent(t)
		r := g.run(t, g.nongit, nil, "What were the top 3 AI news headlines this week? One line each.")
		quiet(t, r)
		if d := g.spent(t) - before; d > 0.03*1.5 {
			t.Errorf("spent $%.4f against a $0.03 limit", d)
		}
	})

	t.Run("a run that fails before the model does not use up an approval", func(t *testing.T) {
		g.must(t, g.nongit, nil, "engine", "set", "per_task_usd", "0.0001")
		defer g.must(t, g.nongit, nil, "engine", "set", "per_task_usd", "0.25")
		m := g.mcp(t, g.nongit)
		defer m.close()
		m.call(t, "approvals_add", map[string]any{"max_count": 1, "max_per_run_usd": 0.05, "max_total_usd": 0.05,
			"expires_in_minutes": 30, "scope": "global", "models": []string{"deepseek/deepseek-v4.1-flash"}, "note": "e2e gate"})
		bad := filepath.Join(filepath.Dir(g.home), "bad-key")
		os.WriteFile(bad, []byte("sk-or-v1-"+strings.Repeat("0", 64)+"\n"), 0o600)
		badEnv := []string{"P0SSESS_OPENROUTER_KEY_FILE=" + bad}
		r := g.run(t, g.nongit, badEnv, "--model", "deepseek/deepseek-v4.1-flash", "In one sentence, what is a mutex?")
		if r.Status == "done" || !strings.Contains(r.Error, "did not accept your API key") {
			t.Errorf("a rejected key: %+v", r)
		}
		if g.podman {
			r = g.run(t, g.repo, badEnv, "--model", "deepseek/deepseek-v4.1-flash", "--max-cost", "0.03", "What does Add in calc.go do? One sentence.")
			if r.Status == "done" || !strings.Contains(r.Error, "did not accept your API key") || r.Receipt != "" {
				t.Errorf("a rejected key on the agent path: %+v", r)
			}
		}
		if n := m.approvalUses(t, "e2e gate"); n != 0 {
			t.Fatalf("failed runs used the approval %d time(s)", n)
		}
		quiet(t, g.run(t, g.nongit, nil, "--model", "deepseek/deepseek-v4.1-flash", "In one sentence, what is a mutex?"))
		if n := m.approvalUses(t, "e2e gate"); n != 1 {
			t.Errorf("the approval was used %d time(s), want 1", n)
		}
	})

	t.Run("can only afford", func(t *testing.T) {
		t.Skip("needs a key with a low limit; covered by internal/relay TestDoSelfHeals with OpenRouter's 402 text")
	})

	t.Run("tour demo", func(t *testing.T) {
		tg := *g
		tg.home = filepath.Join(filepath.Dir(g.home), "tour-home")
		os.MkdirAll(tg.home, 0o700)
		show := tg.must(t, g.nongit, nil, "tour", "show")
		if !strings.Contains(show, "the defaults for a new install: $1 a day") || !strings.Contains(show, "$0.25 a task") {
			t.Errorf("tour show:\n%s", show)
		}
		out := tg.must(t, g.nongit, nil, "tour", "demo")
		if !strings.Contains(out, "answer   ") || !strings.Contains(out, "receipt  (") {
			t.Errorf("demo:\n%s", out)
		}
		if s := tg.spent(t); s > 0.01 {
			t.Errorf("the demo cost $%.4f", s)
		}
		if again := tg.must(t, g.nongit, nil, "tour", "show"); !strings.Contains(again, "the defaults for a new install") {
			t.Errorf("after the demo the install no longer counts as new:\n%s", again)
		}
		t.Logf("demo spent $%.4f", tg.spent(t))
	})

	t.Run("quiet result through the MCP tool", func(t *testing.T) {
		m := g.mcp(t, g.nongit)
		defer m.close()
		text := m.call(t, "run", map[string]any{"task": "In one sentence, what is a goroutine?", "cwd": g.nongit})
		var r result
		if err := json.Unmarshal([]byte(text), &r); err != nil {
			t.Fatalf("run tool: %v\n%s", err, text)
		}
		quiet(t, r)
		notes, _ := os.ReadFile(filepath.Join(g.home, "state", "notices.jsonl"))
		if n := strings.Count(string(notes), `"run":"`+r.RunID+`"`); r.RunID != "" && n != 1 {
			t.Errorf("the run's receipt was recorded %d times", n)
		}
	})
}

// mcpClient speaks just enough MCP over stdio to call tools.
type mcpClient struct {
	cmd *exec.Cmd
	in  *json.Encoder
	out *bufio.Scanner
	id  int
}

func (g *gate) mcp(t *testing.T, dir string) *mcpClient {
	cmd := exec.Command(g.bin, "mcp")
	cmd.Dir, cmd.Env = dir, g.env(nil)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	m := &mcpClient{cmd: cmd, in: json.NewEncoder(stdin), out: sc}
	m.request(t, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "e2e", "version": "1"}})
	m.in.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	return m
}

func (m *mcpClient) request(t *testing.T, method string, params any) json.RawMessage {
	t.Helper()
	m.id++
	m.in.Encode(map[string]any{"jsonrpc": "2.0", "id": m.id, "method": method, "params": params})
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) && m.out.Scan() {
		var msg struct {
			ID     *int            `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(m.out.Bytes(), &msg) != nil || msg.ID == nil || *msg.ID != m.id {
			continue
		}
		if msg.Error != nil {
			t.Fatalf("%s: %s", method, msg.Error)
		}
		return msg.Result
	}
	t.Fatalf("%s: no answer", method)
	return nil
}

func (m *mcpClient) call(t *testing.T, tool string, args any) string {
	t.Helper()
	var r struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	json.Unmarshal(m.request(t, "tools/call", map[string]any{"name": tool, "arguments": args}), &r)
	var b strings.Builder
	for _, c := range r.Content {
		b.WriteString(c.Text)
	}
	if r.IsError {
		t.Fatalf("%s: %s", tool, b.String())
	}
	return b.String()
}

// approvalUses is how many runs the approval saved with note has covered.
func (m *mcpClient) approvalUses(t *testing.T, note string) int {
	t.Helper()
	text := m.call(t, "approvals_list", map[string]any{})
	var any_ any
	if err := json.Unmarshal([]byte(text), &any_); err != nil {
		t.Fatalf("approvals_list: %v\n%s", err, text)
	}
	n := -1
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if x["note"] == note {
				if u, ok := x["used"].(map[string]any); ok {
					c, _ := strconv.Atoi(fmt.Sprint(u["count"]))
					n = c
				}
				return
			}
			for _, e := range x {
				walk(e)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(any_)
	if n < 0 {
		t.Fatalf("no approval noted %q in:\n%s", note, text)
	}
	return n
}

func (m *mcpClient) close() {
	m.cmd.Process.Kill()
	m.cmd.Wait()
}
