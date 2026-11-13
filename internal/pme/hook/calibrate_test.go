package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"p0ssess/internal/account"
	"p0ssess/internal/calibrate"
)

func TestCalibrationOfferAndBaselineSilence(t *testing.T) {
	d, proj := dirs(t)
	raw, _ := json.Marshal(Input{SessionID: "s", Cwd: proj, Prompt: "transcribe this recording", Event: "UserPromptSubmit"})
	if out := Run(raw, d, time.Now()); strings.Contains(out, "calibrat") {
		t.Errorf("free plan got the offer: %q", out)
	}
	account.SaveToken(d, "p0d_test")
	os.MkdirAll(d.State, 0o700)
	os.WriteFile(filepath.Join(d.State, "account.json"), []byte(`{"plan":"pro","status":"active"}`), 0o600)

	t.Setenv(calibrate.BaselineEnv, calibrate.ModeAlone)
	if out := Run(raw, d, time.Now()); out != "" {
		t.Errorf("baseline run got hook text: %q", out)
	}
	t.Setenv(calibrate.BaselineEnv, "")
	if out := Run(raw, d, time.Now()); !strings.Contains(out, "/p0ssess:calibrate") {
		t.Errorf("no offer after Pro: %q", out)
	}
	if out := Run(raw, d, time.Now()); strings.Contains(out, "/p0ssess:calibrate") {
		t.Errorf("offer repeated: %q", out)
	}
}
