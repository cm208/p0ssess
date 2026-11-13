package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"p0ssess/internal/account"
	"p0ssess/internal/pme/paths"
)

type accountStubs struct {
	opened  []string
	started int
}

func stub(t *testing.T, checkout string) (*accountStubs, *httptest.Server, paths.Dirs) {
	t.Setenv("P0SSESS_HOME", t.TempDir())
	st := &accountStubs{}
	oldB, oldP := openBrowser, startPoller
	openBrowser = func(u string) error { st.opened = append(st.opened, u); return nil }
	startPoller = func(paths.Dirs) error { st.started++; return nil }
	t.Cleanup(func() { openBrowser, startPoller = oldB, oldP })
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/pro/offer", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"checkout":"` + checkout + `","plans":[
 {"id":"pro_yearly","tier":"pro","interval":"year","recommended":true,"price":{"display":"$X/year"},"placeholder":true},
 {"id":"pro_monthly","tier":"pro","interval":"month","price":{"display":"$Y/month"},"placeholder":true},
 {"id":"team_monthly","tier":"team","interval":"month","per_seat":true,"available":false,"price":{"display":"$Z"}}]}`))
	})
	mux.HandleFunc("/v1/device/code", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"device_code": "dev", "user_code": "BKDF-MXTR",
			"verification_uri_complete": srv.URL + "/pro?code=BKDF-MXTR", "expires_in": 900, "interval": 5})
	})
	var waitlist map[string]string
	mux.HandleFunc("/v1/waitlist", func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&waitlist)
		w.WriteHeader(204)
	})
	mux.HandleFunc("/v1/account", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"entitlements":{"plan":"pro","status":"active","interval":"year","renews_at":"2027-11-12T00:00:00Z"},
 "links":{"renew":"x/pro","manage":"https://example.test/account"}}`))
	})
	mux.HandleFunc("/v1/account/device", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("P0SSESS_CLOUD_URL", srv.URL)
	return st, srv, paths.Default()
}

func run(t *testing.T, args ...string) string {
	var b bytes.Buffer
	if err := runAccount(context.Background(), args, &b); err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return b.String()
}

func TestAccountProOpeningSoon(t *testing.T) {
	st, _, _ := stub(t, "opening_soon")
	out := run(t, "pro")
	for _, want := range []string{"Pro, yearly: $X/year (placeholder)  <- recommended", "Pro, monthly: $Y/month (placeholder)",
		"Team: coming in a later release", "Opening soon. Tell Claude an email to get notified."} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if len(st.opened) != 0 || st.started != 0 {
		t.Errorf("opening soon must not start a link: %+v", st)
	}
	if _, ok := account.PendingLink(paths.Default()); ok {
		t.Error("link state saved")
	}
}

func TestAccountProOpen(t *testing.T) {
	st, srv, d := stub(t, "open")
	out := run(t, "pro")
	if !strings.Contains(out, "Code: BKDF-MXTR") || !strings.Contains(out, "Pro turns on here by itself") {
		t.Errorf("output:\n%s", out)
	}
	if len(st.opened) != 1 || st.opened[0] != srv.URL+"/pro?code=BKDF-MXTR" || st.started != 1 {
		t.Errorf("stubs %+v", st)
	}
	if l, ok := account.PendingLink(d); !ok || l.UserCode != "BKDF-MXTR" {
		t.Error("link state missing")
	}
	if s := run(t, "status"); !strings.Contains(s, "linking: waiting for the browser, code BKDF-MXTR") {
		t.Errorf("status %q", s)
	}
}

func TestAccountProAlreadyPro(t *testing.T) {
	st, _, d := stub(t, "open")
	account.SaveToken(d, "p0d_abc")
	out := run(t, "pro")
	if !strings.Contains(out, "You are on p0ssess Pro") || !strings.Contains(out, "Manage: https://example.test/account") {
		t.Errorf("output:\n%s", out)
	}
	if st.started != 0 || len(st.opened) != 0 {
		t.Error("linked again while Pro")
	}
	if s := run(t, "status"); !strings.Contains(s, "p0ssess Pro (yearly), active; renews Nov 12, 2027") {
		t.Errorf("status %q", s)
	}
}

func TestAccountNotifyAndSignout(t *testing.T) {
	_, _, d := stub(t, "opening_soon")
	if out := run(t, "notify", "--email", "a@example.com"); !strings.Contains(out, "a@example.com") {
		t.Errorf("notify %q", out)
	}
	var b bytes.Buffer
	if err := runAccount(context.Background(), []string{"notify", "--email", "not an email"}, &b); err == nil {
		t.Error("bad email accepted")
	}
	account.SaveToken(d, "p0d_abc")
	if out := run(t, "signout"); !strings.Contains(out, "Signed out") || account.Token(d) != "" {
		t.Errorf("signout %q", out)
	}
	if out := run(t, "status"); !strings.Contains(out, "p0ssess Free") {
		t.Errorf("status %q", out)
	}
}

func TestAccountLinkWhileOpeningSoon(t *testing.T) {
	st, _, d := stub(t, "opening_soon")
	out := run(t, "link")
	if len(st.opened) != 1 || st.started != 1 || !strings.Contains(out, "Code: BKDF-MXTR") {
		t.Errorf("link should start while checkout is closed: %+v\n%s", st, out)
	}
	if _, ok := account.PendingLink(d); !ok {
		t.Error("no link state saved")
	}
	account.SaveToken(d, "p0d_x")
	if out := run(t, "link"); !strings.Contains(out, "already linked") || st.started != 1 {
		t.Errorf("a linked install should not relink: %s", out)
	}
}
