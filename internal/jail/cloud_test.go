package jail

import (
	"os"
	"path/filepath"
	"testing"

	"p0ssess/internal/config"
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
