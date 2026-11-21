package local

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"p0ssess/internal/account"
	"p0ssess/internal/funnel"
	"p0ssess/internal/history"
)

func TestFunnelSettingStartsAndStops(t *testing.T) {
	e, _ := setup(t, "")
	history.Append(e.Dirs.HistoryFile(), history.Entry{TS: e.Now().UTC(), Type: history.TypeRun, RunID: "d-1", Route: "direct",
		Model: "google/gemini-3.8-flash", CashUSD: 0.01, Status: "done"})
	account.SaveToken(e.Dirs, "p0d_test")
	if e.FunnelView().Send {
		t.Fatal("usage counts on by default")
	}
	msg, err := e.SetSetting("funnel.send", "true")
	if err != nil || !strings.Contains(msg, "random id that is not your account") {
		t.Fatalf("on: %q %v", msg, err)
	}
	c := e.FunnelView().ThisWeek.Counts
	if c[funnel.Install] != 1 || c[funnel.FirstRoute] != 1 || c[funnel.DeviceLinked] != 1 || c[funnel.TourDone] != 0 {
		t.Fatalf("seeded counts %v", c)
	}
	if msg, err := e.SetSetting("funnel.send", "false"); err != nil || !strings.Contains(msg, "deleted") {
		t.Fatalf("off: %q %v", msg, err)
	}
	if v := e.FunnelView(); v.Send || v.ID != "" {
		t.Fatalf("view after off: %+v", v)
	}
}

func TestSendFunnelCarriesNoToken(t *testing.T) {
	e, _ := setup(t, "[funnel]\nsend = true\n")
	var calls int32
	var hdr http.Header
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/funnel" {
			w.WriteHeader(404)
			return
		}
		atomic.AddInt32(&calls, 1)
		hdr = r.Header.Clone()
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("P0SSESS_CLOUD_URL", srv.URL)
	account.SaveToken(e.Dirs, "p0d_secret")
	funnel.Start(e.Dirs, funnel.Reached{}, e.Now().AddDate(0, 0, -7))
	n, err := e.SendFunnel(context.Background())
	if err != nil || n != 1 || atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("sent %d %v calls %d", n, err, calls)
	}
	if hdr.Get("Authorization") != "" || hdr.Get("P0ssess-Install") != "" || strings.Contains(hdr.Get("User-Agent"), "/") {
		t.Fatalf("request identifies the install: %v", hdr)
	}
	if !strings.Contains(body, `"install":1`) || strings.Contains(body, "p0d_") {
		t.Fatalf("body %s", body)
	}
}
