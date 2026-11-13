package relay

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"p0ssess/internal/config"
	"p0ssess/internal/jail"
)

// The cloud integration test runs a real sandbox host (rootless podman and runsc on the development
// machine) and drives a whole edit run through it. OpenRouter is a local fake, so nothing is spent.
//
//	P0SSESS_CLOUD_IT=1
//	P0SSESS_SANDBOX_HOST=<p0ssess-sandbox binary>  P0SSESS_SANDBOX_INIT=<p0ssess-sandbox-init binary>
//	P0SSESS_SANDBOX_HOST_ARGS="<extra host flags, e.g. runtime and image>"
//	CONTAINERS_CONF_OVERRIDE=<the runsc containers.conf>

// fakeOpenRouter scripts a model: it first calls the shell tool with cmd, then answers with what the tool
// printed. Every answer reports cost usd.
type fakeOpenRouter struct {
	cmd   string
	cost  float64
	calls atomic.Int32
	mu    sync.Mutex
	saw   []string
}

func (f *fakeOpenRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		io.WriteString(w, `{"data":[]}`)
		return
	}
	f.calls.Add(1)
	if r.Header.Get("Authorization") != "Bearer sk-or-fake-key" {
		http.Error(w, `{"error":{"message":"bad key"}}`, 401)
		return
	}
	var req struct {
		Model    string            `json:"model"`
		Messages []json.RawMessage `json:"messages"`
		Tools    []struct {
			Function struct {
				Name       string          `json:"name"`
				Parameters json.RawMessage `json:"parameters"`
			} `json:"function"`
		} `json:"tools"`
		Usage    map[string]any `json:"usage"`
		Provider map[string]any `json:"provider"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	f.mu.Lock()
	f.saw = append(f.saw, fmt.Sprintf("model=%s usage=%v dc=%v", req.Model, req.Usage["include"], req.Provider["data_collection"]))
	f.mu.Unlock()
	var toolOut string
	for _, m := range req.Messages {
		var msg struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		}
		json.Unmarshal(m, &msg)
		if msg.Role == "tool" {
			b, _ := json.Marshal(msg.Content)
			toolOut = string(b)
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	chunk := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", b)
		w.(http.Flusher).Flush()
	}
	usage := map[string]any{"prompt_tokens": 100, "completion_tokens": 20, "total_tokens": 120, "cost": f.cost}
	id := fmt.Sprintf("gen-%d", f.calls.Load())
	shell := ""
	for _, t := range req.Tools {
		if t.Function.Name == "bash" || shell == "" && strings.Contains(t.Function.Name, "shell") {
			shell = t.Function.Name
		}
	}
	if toolOut == "" && shell != "" && !strings.Contains(r.URL.Path, "title") {
		args, _ := json.Marshal(map[string]any{"command": f.cmd, "description": "Run the check"})
		chunk(map[string]any{"id": id, "object": "chat.completion.chunk", "model": req.Model, "choices": []any{map[string]any{
			"index": 0, "delta": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
				"index": 0, "id": "call_1", "type": "function", "function": map[string]any{"name": shell, "arguments": string(args)}}}},
			"finish_reason": nil}}})
		chunk(map[string]any{"id": id, "object": "chat.completion.chunk", "model": req.Model, "choices": []any{map[string]any{
			"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}}, "usage": usage})
	} else {
		chunk(map[string]any{"id": id, "object": "chat.completion.chunk", "model": req.Model, "choices": []any{map[string]any{
			"index": 0, "delta": map[string]any{"role": "assistant", "content": "DONE " + toolOut}, "finish_reason": nil}}})
		chunk(map[string]any{"id": id, "object": "chat.completion.chunk", "model": req.Model, "choices": []any{map[string]any{
			"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}, "usage": usage})
	}
	io.WriteString(w, "data: [DONE]\n\n")
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// fakeAccounts hands out sandbox tickets signed with priv, as auth.md describes.
func fakeAccounts(t *testing.T, priv ed25519.PrivateKey) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/account/ticket" || r.Header.Get("Authorization") != "Bearer p0d_testtoken" {
			w.WriteHeader(403)
			io.WriteString(w, `{"error":"not_entitled"}`)
			return
		}
		now := time.Now()
		jti := make([]byte, 12)
		rand.Read(jti)
		payload, _ := json.Marshal(map[string]any{"v": 1, "iss": "p0ssess-cloud", "aud": "sandbox", "kid": "k1",
			"sub": "acc_test", "dev": "dev_test", "plan": "pro", "org": "", "sbx_h": 20,
			"iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix(), "jti": b64(jti)})
		signed := "p0t1." + b64(payload)
		tk := signed + "." + b64(ed25519.Sign(priv, []byte(signed)))
		json.NewEncoder(w).Encode(map[string]any{"ticket": tk, "expires_at": now.Add(10 * time.Minute)})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func selfSigned(t *testing.T, dir string) (cert, key string) {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "sandbox-test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(k)
	cert, key = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	os.WriteFile(key, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600)
	return cert, key
}

func freePort(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func cloudSetup(t *testing.T, model *fakeOpenRouter) (string, *Engine) {
	t.Helper()
	if os.Getenv("P0SSESS_CLOUD_IT") != "1" {
		t.Skip("set P0SSESS_CLOUD_IT=1 to run the cloud sandbox integration test")
	}
	hostBin, initBin := os.Getenv("P0SSESS_SANDBOX_HOST"), os.Getenv("P0SSESS_SANDBOX_INIT")
	if hostBin == "" || initBin == "" {
		t.Fatal("P0SSESS_SANDBOX_HOST and P0SSESS_SANDBOX_INIT are required")
	}
	tmp := t.TempDir()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keys := filepath.Join(tmp, "ticket-keys.json")
	kj, _ := json.Marshal(map[string]any{"keys": []any{map[string]any{"kid": "k1", "alg": "Ed25519",
		"public_key": base64.StdEncoding.EncodeToString(pub)}}})
	os.WriteFile(keys, kj, 0o600)
	cert, key := selfSigned(t, tmp)
	addr := freePort(t)
	state := filepath.Join(tmp, "host-state")
	args := []string{"-listen", addr, "-tls-cert", cert, "-tls-key", key, "-state", state,
		"-init", initBin, "-ticket-keys", keys}
	args = append(args, strings.Fields(os.Getenv("P0SSESS_SANDBOX_HOST_ARGS"))...)
	host := exec.Command(hostBin, args...)
	host.Stdout, host.Stderr = os.Stderr, os.Stderr
	if err := host.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		host.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { host.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			host.Process.Kill()
		}
	})
	for i := 0; ; i++ {
		if c, err := net.Dial("tcp", addr); err == nil {
			c.Close()
			break
		}
		if i > 100 {
			t.Fatal("the sandbox host did not start")
		}
		time.Sleep(100 * time.Millisecond)
	}

	or := httptest.NewServer(model)
	t.Cleanup(or.Close)
	old := jail.CloudUpstream
	jail.CloudUpstream = or.URL
	t.Cleanup(func() { jail.CloudUpstream = old })

	home := filepath.Join(tmp, "home")
	t.Setenv("P0SSESS_HOME", home)
	t.Setenv("P0SSESS_SANDBOX", "cloud")
	t.Setenv("P0SSESS_CLOUD_URL", fakeAccounts(t, priv).URL)
	t.Setenv("P0SSESS_SANDBOX_URL", "https://"+addr)
	t.Setenv("P0SSESS_SANDBOX_CA", cert)
	keyFile := filepath.Join(tmp, "openrouter_key")
	os.WriteFile(keyFile, []byte("sk-or-fake-key\n"), 0o600)
	t.Setenv("P0SSESS_OPENROUTER_KEY_FILE", keyFile)
	os.MkdirAll(filepath.Dir(jail.DeviceTokenFile()), 0o700)
	os.WriteFile(jail.DeviceTokenFile(), []byte("p0d_testtoken\n"), 0o600)

	repo := filepath.Join(tmp, "repo")
	os.MkdirAll(repo, 0o755)
	os.WriteFile(filepath.Join(repo, "calc.py"), []byte("def add(a, b):\n    return a + b\n"), 0o644)
	os.WriteFile(filepath.Join(repo, ".env"), []byte("SECRET=never-uploaded\n"), 0o644)
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"},
		{"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-qm", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxRunCostUSD = 0.05
	cfg.SessionIdleSec = 600
	re := NewEngine(cfg, "test", os.Stderr)
	re.Unguarded = true
	t.Cleanup(re.Shutdown)
	return repo, re
}

func TestCloudIntegrationEditRun(t *testing.T) {
	model := &fakeOpenRouter{cost: 0.001, cmd: `echo hello > made.txt; ls -a; ` +
		`echo env-opencode=$(env | grep -c OPENCODE); ` +
		`test -e /run/p0ssess/agent && echo agent-dir-visible || echo agent-dir-hidden; ` +
		`python3 -c "import urllib.request as u; u.urlopen('https://example.com', timeout=5)" 2>/dev/null && echo internet-open || echo internet-closed`}
	repo, e := cloudSetup(t, model)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	res, err := e.Run(ctx, RunRequest{Task: "Create made.txt", Model: "fake/model", Mode: "unsafe-allow-all", Cwd: repo, MaxCostUSD: 0.05}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("status=%s cost=%v jail_start=%.2fs note=%q", res.Status, res.CostUSD, res.JailStartS, res.Note)
	t.Logf("answer: %.600s", res.Answer)
	t.Logf("model saw: %v", model.saw)
	if res.Status != "done" {
		t.Fatalf("status %s: %s", res.Status, res.Answer)
	}
	for _, want := range []string{"env-opencode=0", "agent-dir-hidden", "internet-closed", "calc.py"} {
		if !strings.Contains(res.Answer, want) {
			t.Errorf("answer lacks %q", want)
		}
	}
	if strings.Contains(res.Answer, ".env") {
		t.Error(".env reached the sandbox")
	}
	if !strings.Contains(res.Diff, "made.txt") || !strings.Contains(res.Diff, "+hello") {
		t.Errorf("diff: %s", res.Diff)
	}
	if b, err := os.ReadFile(filepath.Join(repo, "made.txt")); err == nil {
		t.Errorf("the main checkout was changed: %q", b)
	}
	if !strings.Contains(res.Note, "cloud sandbox") || !strings.Contains(res.Note, ".env") {
		t.Errorf("note: %q", res.Note)
	}
	for _, s := range model.saw {
		if !strings.Contains(s, "usage=true") || !strings.Contains(s, "dc=deny") {
			t.Errorf("a model call went out without usage accounting or the data-collection rule: %s", s)
		}
	}
}

func TestCloudIntegrationCostCap(t *testing.T) {
	model := &fakeOpenRouter{cost: 0.02, cmd: "echo one"}
	repo, e := cloudSetup(t, model)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	res, err := e.Run(ctx, RunRequest{Task: "x", Model: "fake/model", Mode: "unsafe-allow-all", Cwd: repo, MaxCostUSD: 0.01}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("status=%s answer=%.300s", res.Status, res.Answer)
	if n := model.calls.Load(); n != 1 {
		t.Fatalf("the cap let %d model calls through, want 1 (the call that crossed it)", n)
	}
	if res.Status == "done" && strings.Contains(res.Answer, "DONE") {
		t.Fatal("the run finished past its cost cap")
	}
}
