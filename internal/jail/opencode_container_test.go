package jail

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeUpstream answers OpenAI-style chat calls inside the container and logs each request body, so the
// test never reaches OpenRouter.
const fakeUpstream = `import http.server, json
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("content-length", 0)))
        open("/tmp/fake.log", "a").write(body.decode() + "\n")
        model = json.loads(body).get("model", "?")
        self.send_response(200)
        self.send_header("content-type", "text/event-stream")
        self.end_headers()
        for d in ({"choices": [{"index": 0, "delta": {"role": "assistant", "content": "FAKE-OK"}}]},
                  {"choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}], "usage": {"prompt_tokens": 1, "completion_tokens": 1, "cost": 0}}):
            d.update({"id": "x", "object": "chat.completion.chunk", "model": model})
            self.wfile.write(("data: " + json.dumps(d) + "\n\n").encode())
        self.wfile.write(b"data: [DONE]\n\n")
    def log_message(self, *a): pass
http.server.ThreadingHTTPServer(("127.0.0.1", 9999), H).serve_forever()
`

// TestContainerUnknownModelRuns starts the pinned OpenCode image with no network and a fake upstream,
// and checks the rule OpenCodeConfig relies on: a model missing from OpenCode's built-in catalog fails
// with "Model unavailable" unless it is declared under providers.openrouter.models, and a declared
// output limit is what OpenCode sends as max_completion_tokens. Set P0SSESS_CONTAINER_TEST=1 to run it
// (it needs podman and the image p0ssess-opencode:<OpenCodeVersion>-<ImageRev>).
func TestContainerUnknownModelRuns(t *testing.T) {
	if os.Getenv("P0SSESS_CONTAINER_TEST") != "1" {
		t.Skip("set P0SSESS_CONTAINER_TEST=1 to run against the OpenCode image")
	}
	image := os.Getenv("P0SSESS_CONTAINER_IMAGE")
	if image == "" {
		image = "localhost/p0ssess-opencode:2.2.5-1"
	}
	const id = "google/gemini-4-carbon"
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "fake.py"), []byte(fakeUpstream), 0o644)

	run := func(name string, models ...Model) (outcome string, sent map[string]any) {
		cfgJSON, err := OpenCodeConfig(models...)
		if err != nil {
			t.Fatal(err)
		}
		var cfg map[string]any
		json.Unmarshal(cfgJSON, &cfg)
		or := cfg["providers"].(map[string]any)["openrouter"].(map[string]any)
		or["settings"].(map[string]any)["baseURL"] = "http://127.0.0.1:9999/api/v1"
		cfg["shell"] = "/bin/bash"
		cfgJSON, _ = json.Marshal(cfg)
		exec.Command("podman", "rm", "-f", name).Run()
		t.Cleanup(func() { exec.Command("podman", "rm", "-f", name).Run() })
		cmd := exec.Command("podman", "run", "-d", "--name", name, "--network", "none",
			"-v", filepath.Join(dir, "fake.py")+":/fake.py:ro,Z",
			"-e", "OPENCODE_PASSWORD=pw", "-e", "OPENCODE_CONFIG_CONTENT", "-e", "OPENROUTER_API_KEY="+ProxyKeyPlaceholder,
			"-e", "HOME=/tmp/h", "-e", "XDG_CONFIG_HOME=/tmp/c", "-e", "XDG_DATA_HOME=/tmp/d", "-e", "XDG_STATE_HOME=/tmp/s",
			"-e", "XDG_CACHE_HOME=/tmp/k", "-e", "OPENCODE_DISABLE_PROJECT_CONFIG=1", "-e", "OPENCODE_DISABLE_CLAUDE_CODE=1",
			"-w", "/tmp", image, "sh", "-c", "python3 /fake.py & exec opencode serve --hostname 127.0.0.1 --port 4096")
		cmd.Env = append(os.Environ(), "OPENCODE_CONFIG_CONTENT="+string(cfgJSON))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("podman run: %v: %s", err, out)
		}
		api := func(method, path, body string) []byte {
			args := []string{"exec", name, "curl", "-s", "-u", "opencode:pw", "-X", method, "-H", "content-type: application/json"}
			if body != "" {
				args = append(args, "-d", body)
			}
			out, _ := exec.Command("podman", append(args, "http://127.0.0.1:4096"+path)...).Output()
			return out
		}
		up := false
		for i := 0; i < 90 && !up; i++ {
			up = len(api("GET", "/api/info", "")) > 0
			if !up {
				time.Sleep(time.Second)
			}
		}
		if !up {
			t.Fatal("the OpenCode server did not come up")
		}
		var s struct {
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		json.Unmarshal(api("POST", "/api/session", fmt.Sprintf(`{"model":{"id":%q,"providerID":"openrouter"},"location":{"directory":"/tmp"}}`, id)), &s)
		if s.Data.ID == "" {
			t.Fatal("no session")
		}
		api("POST", "/api/session/"+s.Data.ID+"/prompt", `{"text":"say hi"}`)
		api("POST", "/api/experimental/session/"+s.Data.ID+"/wait", "")
		msgs := api("GET", "/api/session/"+s.Data.ID+"/message", "")
		log, _ := exec.Command("podman", "exec", name, "cat", "/tmp/fake.log").Output()
		if line, _, _ := bytes.Cut(log, []byte("\n")); len(line) > 0 {
			json.Unmarshal(line, &sent)
		}
		return string(msgs), sent
	}

	msgs, sent := run("p0ssess-ct-undeclared")
	if !strings.Contains(msgs, "Model unavailable: openrouter/"+id) || sent != nil {
		t.Fatalf("undeclared: want Model unavailable and no upstream call, got %s / %v", msgs, sent)
	}
	msgs, sent = run("p0ssess-ct-declared", Model{ID: id, Name: "Gemini 4 Carbon", Context: 1048576, Output: 9000, InPerM: 2, OutPerM: 10})
	if !strings.Contains(msgs, "FAKE-OK") || sent["model"] != id {
		t.Fatalf("declared: want the fake's answer, got %s / %v", msgs, sent)
	}
	if v, _ := sent["max_completion_tokens"].(float64); v != 9000 {
		t.Errorf("max_completion_tokens = %v, want the declared 9000", sent["max_completion_tokens"])
	}
	t.Logf("declared model ran; OpenCode sent %v", map[string]any{"model": sent["model"], "max_completion_tokens": sent["max_completion_tokens"]})
}
