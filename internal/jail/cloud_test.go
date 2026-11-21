package jail

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"p0ssess/internal/cloudsandbox"
	"p0ssess/internal/config"
	"p0ssess/internal/funnel"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/proxy"
)

func TestDecideTable(t *testing.T) {
	noPodman := errors.New("podman is not installed")
	for _, c := range []struct {
		name string
		p    Placement
		want Where
	}{
		{"mac pro auto", Placement{Setting: "auto", LocalErr: noPodman, Pro: true, GOOS: "darwin"}, Cloud},
		{"mac pro default", Placement{LocalErr: noPodman, Pro: true, GOOS: "darwin"}, Cloud},
		{"windows team auto", Placement{Setting: "auto", LocalErr: noPodman, Pro: true, GOOS: "windows"}, Cloud},
		{"linux without podman pro", Placement{LocalErr: noPodman, Pro: true, GOOS: "linux"}, Cloud},
		{"linux podman pro stays local", Placement{Setting: "auto", Pro: true, GOOS: "linux"}, Local},
		{"linux podman free", Placement{Pro: false, GOOS: "linux"}, Local},
		{"mac free", Placement{LocalErr: noPodman, GOOS: "darwin"}, NeedPro},
		{"windows free auto", Placement{Setting: "auto", LocalErr: noPodman, GOOS: "windows"}, NeedPro},
		{"linux podman pro asks for cloud", Placement{Setting: "cloud", Pro: true, GOOS: "linux"}, Cloud},
		{"free asks for cloud", Placement{Setting: "cloud", GOOS: "linux"}, NeedPro},
		{"environment forces cloud", Placement{Setting: "cloud", Env: true, GOOS: "linux"}, Cloud},
		{"pro asks for local with podman", Placement{Setting: "local", Pro: true, GOOS: "linux"}, Local},
		{"pro asks for local on a mac", Placement{Setting: "local", LocalErr: noPodman, Pro: true, GOOS: "darwin"}, Unavailable},
	} {
		w, err := decide(c.p)
		if w != c.want {
			t.Errorf("%s: %v (%v), want %v", c.name, w, err, c.want)
		}
		if (w == NeedPro) != (func() bool { var n *NeedsPro; return errors.As(err, &n) })() {
			t.Errorf("%s: error %v", c.name, err)
		}
	}
}

func TestNeedsProLine(t *testing.T) {
	mac := (&NeedsPro{GOOS: "darwin"}).Error()
	want := "This job needs a sandbox, and it can run in the p0ssess cloud sandbox with Pro (/p0ssess:pro). " +
		"For now, read-only questions still run on the direct path, or use Linux with rootless podman."
	if mac != want {
		t.Errorf("%q", mac)
	}
	if l := (&NeedsPro{GOOS: "linux"}).Error(); !strings.HasSuffix(l, "or install rootless podman.") || strings.Contains(l, "\n") {
		t.Errorf("%q", l)
	}
}

// TestDecideReadsAccountAndSettings: Pro comes from the linked account's cached plan, the setting from
// the environment, then budget.toml.
func TestDecideReadsAccountAndSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("P0SSESS_HOME", home)
	t.Setenv("P0SSESS_SANDBOX", "")
	if sandboxSetting(config.Config{Sandbox: "local"}) != "local" {
		t.Error("config.json setting")
	}
	d := paths.Default()
	os.MkdirAll(d.Config, 0o700)
	os.WriteFile(d.BudgetFile(), []byte("[sandbox]\nwhere = \"cloud\"\n"), 0o600)
	if sandboxSetting(config.Config{Sandbox: "local"}) != "cloud" {
		t.Error("budget.toml beats config.json")
	}
	t.Setenv("P0SSESS_SANDBOX", "local")
	if sandboxSetting(config.Config{}) != "local" {
		t.Error("the environment beats budget.toml")
	}
	t.Setenv("P0SSESS_SANDBOX", "")
	if w, err := Decide(config.Config{}); w != NeedPro || err == nil {
		t.Errorf("an unlinked install asking for cloud: %v %v", w, err)
	}
	tok := DeviceTokenFile()
	os.MkdirAll(filepath.Dir(tok), 0o700)
	os.WriteFile(tok, []byte("p0d_abc\n"), 0o600)
	os.MkdirAll(d.State, 0o700)
	os.WriteFile(filepath.Join(d.State, "account.json"), []byte(`{"plan":"pro","status":"active"}`), 0o600)
	if w, err := Decide(config.Config{}); w != Cloud || err != nil {
		t.Errorf("a Pro install asking for cloud: %v %v", w, err)
	}
	if !UseCloud(config.Config{}) {
		t.Error("UseCloud follows Decide")
	}
}

func TestRemotePath(t *testing.T) {
	j := &Jail{Cloud: true, LocalRoot: "/home/me/proj/.p0ssess/wt/w1", RemoteDir: "/work/proj"}
	for in, want := range map[string]string{
		"/home/me/proj/.p0ssess/wt/w1":       "/work/proj",
		"/home/me/proj/.p0ssess/wt/w1/src/x": "/work/proj/src/x",
		"/home/me/other":                     "/work/proj",
	} {
		if got := j.RemotePath(in); got != want {
			t.Errorf("RemotePath(%s) = %s, want %s", in, got, want)
		}
	}
	if (&Jail{}).RemotePath("/a/b") != "/a/b" {
		t.Error("a local jail keeps its paths")
	}
}

// TestCloudRunCost: a cloud session's run cost comes from the laptop's own cost meter, for the current run
// only.
func TestCloudRunCost(t *testing.T) {
	t.Setenv("P0SSESS_HOME", t.TempDir())
	m := cloudsandbox.NewMeter()
	cloudMu.Lock()
	cloudJail["p0-cloudtest"] = &cloudLive{meter: m}
	cloudMu.Unlock()
	defer func() { cloudMu.Lock(); delete(cloudJail, "p0-cloudtest"); cloudMu.Unlock() }()
	j := &Jail{Name: "p0-cloudtest", Cloud: true}
	if err := j.StartRun("r1", 0.5, "x/y"); err != nil {
		t.Fatal(err)
	}
	h := m.Guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"gen-1","usage":{"cost":0.0123}}`))
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/v1/chat/completions", strings.NewReader(`{"model":"x/y"}`)))
	rc, ok := RunCost("p0-cloudtest", "r1")
	if !ok || rc.Calls != 1 || rc.KnownUSD != 0.0123 || rc.Unknown != 0 || rc.ChargedUSD != 0.0123 {
		t.Fatalf("%+v %v", rc, ok)
	}
	if _, ok := RunCost("p0-cloudtest", "r0"); ok {
		t.Fatal("a run that is not the meter's current one has no reading")
	}
}

// TestLocalStartRunWritesTheRunFile: the local key proxy learns the run and its cap from the run file.
func TestLocalStartRunWritesTheRunFile(t *testing.T) {
	t.Setenv("P0SSESS_HOME", t.TempDir())
	j := &Jail{Name: "p0-localtest"}
	os.MkdirAll(ctlDir(j.Name), 0o700)
	if err := j.StartRun("r9", 0.2, "x/y"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(ctlDir(j.Name), runFileName))
	var s proxy.RunSpec
	if json.Unmarshal(b, &s) != nil || s.Run != "r9" || s.CapUSD != 0.2 || s.Model != "x/y" {
		t.Fatalf("run file %s", b)
	}
	if _, ok := RunCost(j.Name, "r9"); ok {
		t.Fatal("no meter file yet means no reading")
	}
}

// A full host queues the job: the client joins, shows its place, and creates the session once the slot is
// held. A host with no hours left ends it with the plain line.
func TestCreateSessionWaitsInQueue(t *testing.T) {
	t.Setenv("P0SSESS_HOME", t.TempDir())
	var mu sync.Mutex
	creates, polls := 0, 0
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/v1/sessions":
			creates++
			if creates == 1 {
				w.WriteHeader(503)
				io.WriteString(w, `{"error":"at_capacity","message":"The sandbox host is at capacity."}`)
				return
			}
			w.WriteHeader(201)
			io.WriteString(w, `{"session":"sbx_1","secret":"p0s_x","expires_at":"2030-01-01T00:00:00Z","limits":{}}`)
		case r.URL.Path == "/v1/queue" && r.Method == "POST":
			w.WriteHeader(202)
			io.WriteString(w, `{"queue":"q_1","secret":"p0q_s","state":"queued","position":2,"eta_s":240,"poll_s":1}`)
		case r.URL.Path == "/v1/queue/q_1":
			polls++
			io.WriteString(w, `{"queue":"q_1","state":"ready","position":1,"hold_s":90}`)
		default:
			w.WriteHeader(404)
		}
	})
	srv := httptest.NewTLSServer(h)
	defer srv.Close()
	fetch := func(context.Context) (*cloudsandbox.Ticket, error) {
		return &cloudsandbox.Ticket{Ticket: "p0t1.a.b", SandboxURL: srv.URL}, nil
	}
	var log strings.Builder
	s, err := createSession(context.Background(), srv.Client(), fetch, cloudsandbox.Spec{Workdir: "/work/p"}, config.Config{}, &log)
	if err != nil || s == nil {
		t.Fatalf("%v", err)
	}
	if creates != 2 || polls != 1 || !strings.Contains(log.String(), "queued 2nd, ~4 min") {
		t.Errorf("creates %d polls %d log %q", creates, polls, log.String())
	}
	if _, _, ok := cloudsandbox.Queued(paths.Default(), time.Now()); ok {
		t.Error("the queue file stays after the wait")
	}
}

func TestCreateSessionAllowanceUsedUp(t *testing.T) {
	t.Setenv("P0SSESS_HOME", t.TempDir())
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		io.WriteString(w, `{"error":"allowance_used","message":"This month's sandbox hours are used up (20h of 20h)."}`)
	}))
	defer srv.Close()
	fetch := func(context.Context) (*cloudsandbox.Ticket, error) {
		return &cloudsandbox.Ticket{Ticket: "p0t1.a.b", SandboxURL: srv.URL}, nil
	}
	_, err := createSession(context.Background(), srv.Client(), fetch, cloudsandbox.Spec{Workdir: "/work/p"}, config.Config{}, io.Discard)
	want := "This month's sandbox hours are used up (20h of 20h). They come back on the 1st; read-only questions still run on the direct path."
	if err == nil || err.Error() != want {
		t.Errorf("%v", err)
	}
}

// The Free offer counts as seen only for an install that opted in to usage counts, once per process.
func TestFreeOfferCountsOnlyWhenOptedIn(t *testing.T) {
	t.Setenv("P0SSESS_HOME", t.TempDir())
	t.Setenv("P0SSESS_SANDBOX", "")
	t.Setenv("P0SSESS_FUNNEL", "")
	d := paths.Default()
	count := func() int {
		return funnel.Show(d, time.Now()).ThisWeek.Counts[funnel.UpgradeViewed]
	}
	proOnce = sync.Once{}
	noteUpgradeViewed()
	if count() != 0 {
		t.Fatal("counted without opt-in")
	}
	os.MkdirAll(d.Config, 0o700)
	os.WriteFile(d.BudgetFile(), []byte("[funnel]\nsend = true\n"), 0o600)
	if err := funnel.Start(d, funnel.Reached{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	proOnce = sync.Once{}
	noteUpgradeViewed()
	noteUpgradeViewed()
	if count() != 1 {
		t.Errorf("counted %d, want 1", count())
	}
}
