package jail

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"p0ssess/internal/cloudsandbox"
	"p0ssess/internal/config"
	"p0ssess/internal/proxy"
)

func TestUseCloudSetting(t *testing.T) {
	t.Setenv("P0SSESS_HOME", t.TempDir())
	t.Setenv("P0SSESS_SANDBOX", "cloud")
	if !UseCloud(config.Config{}) {
		t.Error("P0SSESS_SANDBOX=cloud must force the cloud sandbox")
	}
	t.Setenv("P0SSESS_SANDBOX", "local")
	if UseCloud(config.Config{Sandbox: "cloud"}) {
		t.Error("the environment overrides config.json")
	}
	t.Setenv("P0SSESS_SANDBOX", "")
	if !UseCloud(config.Config{Sandbox: "cloud"}) {
		t.Error(`"sandbox": "cloud" must force the cloud sandbox`)
	}
	if CloudReady(config.Config{}) {
		t.Error("an install with no device token is not ready for the cloud")
	}
	tok := DeviceTokenFile()
	os.MkdirAll(filepath.Dir(tok), 0o700)
	os.WriteFile(tok, []byte("p0d_abc\n"), 0o600)
	if !CloudReady(config.Config{}) || CloudReady(config.Config{Sandbox: "local"}) {
		t.Error("a linked install is ready unless the setting says local")
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
