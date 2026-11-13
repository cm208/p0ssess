package cloudsandbox

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeHost is a minimal sandbox host: it accepts one session, records the upload, and lets a test push
// model requests through the tunnel and read the answers.
type fakeHost struct {
	t       *testing.T
	mu      sync.Mutex
	files   []string
	proto   string
	queue   chan fakeCall
	pending map[string]fakeCall
	gone    bool
	deleted bool
	diff    string
}

type fakeCall struct {
	id, body string
	answer   chan string
}

func newFakeHost(t *testing.T) (*fakeHost, *httptest.Server, string) {
	h := &fakeHost{t: t, queue: make(chan fakeCall, 4), pending: map[string]fakeCall{}}
	srv := httptest.NewUnstartedServer(h)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600)
	return h, srv, ca
}

const fakeSecret = "p0s_secret"

func (h *fakeHost) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	h.proto = r.Proto
	gone := h.gone
	h.mu.Unlock()
	if r.URL.Path == "/v1/sessions" && r.Method == "POST" {
		if r.Header.Get("Authorization") != "Bearer p0t1.a.b" {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":"invalid_ticket","message":"bad ticket"}`)
			return
		}
		w.WriteHeader(201)
		io.WriteString(w, `{"session":"sbx_1","secret":"`+fakeSecret+`","expires_at":"2030-01-01T00:00:00Z","limits":{"cpus":2}}`)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+fakeSecret {
		w.WriteHeader(404)
		return
	}
	rest, _ := strings.CutPrefix(r.URL.Path, "/v1/sessions/sbx_1")
	if gone && rest != "" {
		w.WriteHeader(410)
		io.WriteString(w, `{"error":"gone","message":"expired"}`)
		return
	}
	switch {
	case rest == "/files" && r.Method == "PUT":
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			w.WriteHeader(400)
			return
		}
		tr := tar.NewReader(gz)
		for {
			hd, err := tr.Next()
			if err != nil {
				break
			}
			h.mu.Lock()
			h.files = append(h.files, hd.Name)
			h.mu.Unlock()
		}
		io.WriteString(w, `{"files":1,"bytes":3,"base":"abc","opencode":"2.2.5","startup_s":1.5}`)
	case rest == "/model/next":
		select {
		case c := <-h.queue:
			h.mu.Lock()
			h.pending[c.id] = c
			h.mu.Unlock()
			w.Header().Set("Content-Type", "application/x-p0ssess-model-request")
			env, _ := json.Marshal(map[string]any{"id": c.id, "method": "POST", "path": "/api/v1/chat/completions",
				"headers": map[string]string{"Content-Type": "application/json"}, "body_bytes": len(c.body)})
			w.Write(append(env, '\n'))
			io.WriteString(w, c.body)
		case <-time.After(200 * time.Millisecond):
			w.WriteHeader(204)
		case <-r.Context().Done():
		}
	case strings.HasPrefix(rest, "/model/"):
		id := strings.TrimPrefix(rest, "/model/")
		h.mu.Lock()
		c, ok := h.pending[id]
		delete(h.pending, id)
		h.mu.Unlock()
		if !ok {
			w.WriteHeader(404)
			return
		}
		b, _ := io.ReadAll(r.Body)
		c.answer <- string(b)
		w.WriteHeader(204)
	case strings.HasPrefix(rest, "/opencode/"):
		fmt.Fprintf(w, "opencode %s %s auth=%t", r.Method, strings.TrimPrefix(rest, "/opencode"), r.Header.Get("Authorization") != "")
	case rest == "/diff":
		io.WriteString(w, h.diff)
	case rest == "" && r.Method == "GET":
		io.WriteString(w, `{"state":"ready"}`)
	case rest == "" && r.Method == "DELETE":
		h.mu.Lock()
		h.deleted = true
		h.mu.Unlock()
		w.WriteHeader(204)
	default:
		w.WriteHeader(404)
	}
}

func TestSessionRoundTrip(t *testing.T) {
	host, srv, ca := newFakeHost(t)
	hc, err := Transport(ca)
	if err != nil {
		t.Fatal(err)
	}
	m := NewMeter()
	m.StartRun(1, "x/y")
	var upstreamCalls int
	model := m.Guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"gen-1","usage":{"cost":0.01}}`)
	}))
	ctx := context.Background()
	if _, err := Create(ctx, hc, Spec{URL: srv.URL, Ticket: "p0t1.x.y", Workdir: "/work/r", Model: model}); err == nil {
		t.Fatal("a bad ticket must fail")
	}
	s, err := Create(ctx, hc, Spec{URL: srv.URL, Ticket: "p0t1.a.b", Workdir: "/work/r", Model: model})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)
	repo := gitRepo(t, map[string]string{"a.go": "abc", ".env": "SECRET"})
	man, res, err := s.Upload(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if res.OpenCode != "2.2.5" || len(man.Secrets) != 1 || strings.Join(host.files, ",") != "a.go" {
		t.Fatalf("upload: %+v %+v %v", res, man, host.files)
	}
	if host.proto != "HTTP/2.0" {
		t.Fatalf("protocol %s, want HTTP/2.0", host.proto)
	}

	// A model call from the sandbox goes through the guard and comes back on the tunnel.
	ans := make(chan string, 1)
	host.queue <- fakeCall{id: "r_1", body: `{"model":"x/y","messages":[]}`, answer: ans}
	got := <-ans
	first, body, _ := strings.Cut(got, "\n")
	if !strings.Contains(first, `"status":200`) || !strings.Contains(body, "gen-1") || upstreamCalls != 1 {
		t.Fatalf("answer %q (upstream calls %d)", got, upstreamCalls)
	}
	host.queue <- fakeCall{id: "r_2", body: `{"model":"other/expensive"}`, answer: ans}
	if got := <-ans; !strings.Contains(got, `"status":403`) || upstreamCalls != 1 {
		t.Fatalf("wrong model: %q", got)
	}

	// The relay's OpenCode client reaches the host's API through the loopback forwarder.
	req, _ := http.NewRequest("GET", s.APIURL()+"/global/health?x=1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 401 {
		t.Fatalf("no password: %v %v", err, resp)
	}
	req.SetBasicAuth("opencode", s.Password())
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "opencode GET /global/health auth=true" {
		t.Fatalf("api: %q", b)
	}
	if st, err := s.Status(ctx); st != "ready" || err != nil {
		t.Fatalf("status %q %v", st, err)
	}
	host.diff = "diff --git a/a.go b/a.go\n"
	if d, err := s.Diff(ctx); err != nil || string(d) != host.diff {
		t.Fatalf("diff %q %v", d, err)
	}
	if err := s.Close(ctx); err != nil || !host.deleted {
		t.Fatalf("close: %v deleted=%v", err, host.deleted)
	}
}

func TestSessionEndsWhenHostSaysGone(t *testing.T) {
	host, srv, ca := newFakeHost(t)
	hc, _ := Transport(ca)
	s, err := Create(context.Background(), hc, Spec{URL: srv.URL, Ticket: "p0t1.a.b", Workdir: "/work/r", Model: http.NotFoundHandler()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	s.start()
	host.mu.Lock()
	host.gone = true
	host.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ended, why := s.Ended(); ended {
			if !strings.Contains(why, "expired") {
				t.Fatalf("reason %q", why)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the session did not end")
}

func TestCreateRefusesPlainHTTPAndBadWorkdir(t *testing.T) {
	hc, _ := Transport("")
	if _, err := Create(context.Background(), hc, Spec{URL: "http://127.0.0.1:1", Workdir: "/work/r"}); err == nil {
		t.Error("plain http accepted")
	}
	if _, err := Create(context.Background(), hc, Spec{URL: "https://127.0.0.1:1", Workdir: "/home/me/r"}); err == nil {
		t.Error("a real path as workdir accepted")
	}
	for in, want := range map[string]string{"my repo": "/work/my_repo", "..": "/work/repo", "": "/work/repo", "p0ssess": "/work/p0ssess", "café": "/work/caf_"} {
		if got := RemoteWorkdir(in); got != want {
			t.Errorf("RemoteWorkdir(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReadModelRequestRejectsMalformed(t *testing.T) {
	for _, env := range []string{
		`{"id":"r_1","method":"POST","path":"/etc/passwd","body_bytes":0}`,
		`{"id":"../x","method":"POST","path":"/api/v1/chat/completions","body_bytes":0}`,
		`{"id":"r_1","method":"POST","path":"/api/v1/chat/completions","body_bytes":999999999}`,
		`{"id":"r_1","method":"GET","path":"/api/v1/models/../key","body_bytes":0}`,
		`{"id":"r_1","method":"GET","path":"/api/v1/models/x?y","body_bytes":0}`,
		`not json`,
	} {
		if _, err := readModelRequest(context.Background(), bufio.NewReader(strings.NewReader(env+"\n"))); err == nil {
			t.Errorf("accepted %s", env)
		}
	}
}

func TestCheckPatch(t *testing.T) {
	good := "diff --git a/x.go b/x.go\nindex 1..2 100644\n--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-a\n+b\n" +
		"diff --git a/new file.txt b/new file.txt\nnew file mode 100644\n--- /dev/null\n+++ b/new file.txt\n@@ -0,0 +1 @@\n+--- a/../escape\n"
	if err := CheckPatch([]byte(good)); err != nil {
		t.Fatalf("good patch refused: %v", err)
	}
	for _, bad := range []string{
		"diff --git a/.git/config b/.git/config\n",
		"diff --git a/sub/.GIT/hooks/x b/sub/.GIT/hooks/x\n",
		"diff --git a/../x b/../x\n",
		"diff --git a/x b/x\nrename from x\nrename to ../../y\n",
		"diff --git a/x b/x\n--- a/x\n+++ /abs\n",
		"diff --git \"a/.git/x\" \"b/.git/x\"\n",
		"diff --git a/x b/x\n--- a/x\n+++ \"b/.git/config\"\n",
		"diff --git a/.git. b/.git.\n",
		// A traditional header straight after a finished hunk is a header, not hunk content.
		"diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n--- a/.git/config\n+++ b/.git/config\n@@ -1 +1 @@\n-x\n+y\n",
	} {
		if err := CheckPatch([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestApplyToWorktree(t *testing.T) {
	repo := gitRepo(t, map[string]string{"a.txt": "one\n", "b.txt": "keep\n"})
	head := strings.TrimSpace(mustGit(t, repo, "rev-parse", "HEAD"))
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("two\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "c.txt"), []byte("new\n"), 0o644)
	mustGit(t, repo, "add", "-A")
	patch := mustGit(t, repo, "diff", "--cached", "--binary", head)
	mustGit(t, repo, "reset", "-q", "--hard", head)
	os.WriteFile(filepath.Join(repo, "b.txt"), []byte("stale edit\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "stray"), []byte("x"), 0o644)
	if err := ApplyToWorktree(repo, nil, head, []byte(patch)); err != nil {
		t.Fatal(err)
	}
	for f, want := range map[string]string{"a.txt": "two\n", "b.txt": "keep\n", "c.txt": "new\n"} {
		if b, _ := os.ReadFile(filepath.Join(repo, f)); string(b) != want {
			t.Errorf("%s = %q", f, b)
		}
	}
	if _, err := os.Stat(filepath.Join(repo, "stray")); err == nil {
		t.Error("the worktree was not reset before applying")
	}
	if err := ApplyToWorktree(repo, nil, head, []byte("diff --git a/.git/x b/.git/x\n")); err == nil {
		t.Error("a .git patch was applied")
	}
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(gitEnv(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return out.String()
}
