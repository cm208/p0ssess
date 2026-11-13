package account

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"p0ssess/internal/pme/paths"
)

type fake struct {
	mu         sync.Mutex
	srv        *httptest.Server
	tokenSeq   []string // error codes returned by /v1/device/token before success ("" = success)
	polls      int
	plan       string
	status     string
	endsAt     string
	revoked    bool
	accountHit int
	gotName    string
	gotAuth    string
}

func newFake(t *testing.T) *fake {
	f := &fake{plan: "pro", status: "active"}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/device/code", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		f.gotName = in["client_name"]
		f.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"device_code": "dev123", "user_code": "BKDF-MXTR",
			"verification_uri": f.srv.URL + "/link", "verification_uri_complete": f.srv.URL + "/pro?code=BKDF-MXTR",
			"expires_in": 900, "interval": 5})
	})
	mux.HandleFunc("/v1/device/token", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.polls++
		if len(f.tokenSeq) > 0 {
			code := f.tokenSeq[0]
			f.tokenSeq = f.tokenSeq[1:]
			if code != "" {
				w.WriteHeader(400)
				json.NewEncoder(w).Encode(map[string]string{"error": code})
				return
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "p0d_abc", "token_type": "Bearer"})
	})
	mux.HandleFunc("/v1/account", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.accountHit++
		f.gotAuth = r.Header.Get("Authorization")
		if f.gotAuth != "Bearer p0d_abc" || f.revoked {
			w.WriteHeader(401)
			w.Write([]byte(`{"error":"invalid_token"}`))
			return
		}
		ent := map[string]any{"plan": f.plan, "status": f.status, "interval": "year", "renews_at": "2027-11-12T00:00:00Z"}
		if f.endsAt != "" {
			ent["ends_at"] = f.endsAt
		}
		json.NewEncoder(w).Encode(map[string]any{"account": map[string]any{"id": "acc_1"}, "entitlements": ent,
			"links": map[string]string{"renew": f.srv.URL + "/pro", "manage": f.srv.URL + "/account"}})
	})
	mux.HandleFunc("/v1/account/device", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.revoked = true
		f.mu.Unlock()
		w.WriteHeader(204)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func setup(t *testing.T) (paths.Dirs, *fake, *Client) {
	t.Setenv("P0SSESS_HOME", t.TempDir())
	f := newFake(t)
	t.Setenv("P0SSESS_CLOUD_URL", f.srv.URL)
	return paths.Default(), f, NewClient("1.2.3")
}

func TestBaseURL(t *testing.T) {
	t.Setenv("P0SSESS_CLOUD_URL", "")
	if BaseURL() != DefaultBaseURL {
		t.Fatal(BaseURL())
	}
	t.Setenv("P0SSESS_CLOUD_URL", "http://127.0.0.1:8790/")
	if BaseURL() != "http://127.0.0.1:8790" {
		t.Fatal(BaseURL())
	}
}

func TestTokenFileModeAndOtherWriter(t *testing.T) {
	d, _, _ := setup(t)
	if Token(d) != "" {
		t.Fatal("token before link")
	}
	if err := SaveToken(d, "p0d_abc"); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(tokenFile(d))
		if st.Mode().Perm() != 0o600 {
			t.Errorf("mode %v", st.Mode().Perm())
		}
	}
	// Another process rewrites the file; the next call sees it.
	os.WriteFile(tokenFile(d), []byte("p0d_other\n"), 0o600)
	if Token(d) != "p0d_other" {
		t.Errorf("token %q", Token(d))
	}
	os.Remove(tokenFile(d))
	if Token(d) != "" {
		t.Error("removed token still read")
	}
}

func TestDeviceFlow(t *testing.T) {
	cases := []struct {
		name    string
		seq     []string
		wantErr string
		wait    []time.Duration
	}{
		{"pending then ok", []string{"authorization_pending", "authorization_pending", ""}, "", []time.Duration{5, 5, 5}},
		{"slow down adds five seconds", []string{"slow_down", ""}, "", []time.Duration{5, 10}},
		{"denied", []string{"authorization_pending", "access_denied"}, "declined", []time.Duration{5, 5}},
		{"expired", []string{"expired_token"}, "expired", []time.Duration{5}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, f, c := setup(t)
			f.tokenSeq = tc.seq
			var waits []time.Duration
			old := sleep
			sleep = func(ctx context.Context, dur time.Duration) { waits = append(waits, dur/time.Second) }
			t.Cleanup(func() { sleep = old })

			l, err := StartLink(context.Background(), d, c)
			if err != nil || l.UserCode != "BKDF-MXTR" {
				t.Fatal(l, err)
			}
			if f.gotName != "p0ssess 1.2.3 on "+runtime.GOOS+"/"+runtime.GOARCH {
				t.Errorf("client name %q", f.gotName)
			}
			err = Poll(context.Background(), d, c)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				if Token(d) != "p0d_abc" {
					t.Errorf("token %q", Token(d))
				}
				if !Current(d).Pro() {
					t.Error("not pro after link")
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err %v", err)
				}
				if Token(d) != "" {
					t.Error("token written on failure")
				}
			}
			if _, ok := PendingLink(d); ok {
				t.Error("link state left behind")
			}
			if len(waits) != len(tc.wait) {
				t.Fatalf("waits %v", waits)
			}
			for i := range waits {
				if waits[i] != tc.wait[i] {
					t.Errorf("waits %v want %v", waits, tc.wait)
				}
			}
			if _, err := os.Stat(lockFile(d)); err == nil {
				t.Error("lock left behind")
			}
		})
	}
}

func TestSinglePoller(t *testing.T) {
	d, _, c := setup(t)
	if _, err := StartLink(context.Background(), d, c); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(d.State, 0o700)
	os.WriteFile(lockFile(d), []byte(itoa(os.Getpid())), 0o600)
	if !PollerRunning(d) {
		t.Fatal("own pid should count as running")
	}
	if err := Poll(context.Background(), d, c); err != nil || Token(d) != "" {
		t.Errorf("second poller should return quietly: %v", err)
	}
	// A stale lock is taken over.
	os.WriteFile(lockFile(d), []byte("999999999"), 0o600)
	old := sleep
	sleep = func(context.Context, time.Duration) {}
	defer func() { sleep = old }()
	if err := Poll(context.Background(), d, c); err != nil || Token(d) == "" {
		t.Errorf("stale lock: %v", err)
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestInvalidTokenDeletesFile(t *testing.T) {
	d, f, c := setup(t)
	SaveToken(d, "p0d_abc")
	if _, err := Refresh(context.Background(), d, c); err != nil {
		t.Fatal(err)
	}
	if !Current(d).Pro() || f.gotAuth != "Bearer p0d_abc" {
		t.Fatal("expected pro with bearer", f.gotAuth)
	}
	f.revoked = true
	s, err := Refresh(context.Background(), d, c)
	if err != nil || s.Pro() {
		t.Fatal(s, err)
	}
	if Token(d) != "" {
		t.Error("token survived a 401")
	}
	if _, err := os.Stat(stateFile(d)); err == nil {
		t.Error("state survived a 401")
	}
}

func TestNoTokenNoCalls(t *testing.T) {
	d, f, c := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	Watch(ctx, d)
	if _, err := Refresh(ctx, d, c); err != nil {
		t.Fatal(err)
	}
	if f.accountHit != 0 {
		t.Errorf("free install made %d account calls", f.accountHit)
	}
	if Current(d).Pro() || Notice(d) != "" {
		t.Error("free install shows Pro or a notice")
	}
}

func TestWatchRefreshesAtStart(t *testing.T) {
	d, f, _ := setup(t)
	SaveToken(d, "p0d_abc")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { Watch(ctx, d); close(done) }()
	for i := 0; i < 100 && !Current(d).Pro(); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	if !Current(d).Pro() || f.accountHit != 1 {
		t.Errorf("pro=%v hits=%d", Current(d).Pro(), f.accountHit)
	}
}

func TestLapseNoticeOnce(t *testing.T) {
	d, f, c := setup(t)
	SaveToken(d, "p0d_abc")
	ctx := context.Background()
	Refresh(ctx, d, c)
	if Notice(d) != "" {
		t.Fatal("notice while pro")
	}
	f.plan, f.status, f.endsAt = "free", "lapsed", "2026-11-12T00:00:00Z"
	s, _ := Refresh(ctx, d, c)
	if s.Pro() {
		t.Fatal("still pro")
	}
	want := "p0ssess Pro ended on Nov 12; routing is back on the free plan. Renew: " + f.srv.URL + "/pro"
	if got := Notice(d); got != want {
		t.Fatalf("notice %q", got)
	}
	if Notice(d) != "" {
		t.Error("notice shown twice")
	}
	Refresh(ctx, d, c) // still lapsed: no new notice
	if Notice(d) != "" {
		t.Error("notice came back after another check")
	}
	// Renewing and lapsing again earns a new notice.
	f.plan, f.status, f.endsAt = "pro", "active", ""
	Refresh(ctx, d, c)
	f.plan, f.status, f.endsAt = "free", "lapsed", "2027-11-12T00:00:00Z"
	Refresh(ctx, d, c)
	if !strings.Contains(Notice(d), "Nov 12") {
		t.Error("second lapse not announced")
	}
}

func TestNeverSubscribedNoNotice(t *testing.T) {
	d, f, c := setup(t)
	SaveToken(d, "p0d_abc")
	f.plan, f.status = "free", "lapsed"
	Refresh(context.Background(), d, c)
	if Notice(d) != "" {
		t.Error("notice without a prior Pro state")
	}
}

func TestCanceledStopsAtEndDate(t *testing.T) {
	past := State{Plan: "pro", Status: "canceling", EndsAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}
	future := State{Plan: "pro", Status: "canceling", EndsAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
	if past.Pro() || !future.Pro() {
		t.Error("end date not honored")
	}
}

func TestStateFileMode(t *testing.T) {
	d, _, c := setup(t)
	SaveToken(d, "p0d_abc")
	Refresh(context.Background(), d, c)
	if runtime.GOOS == "windows" {
		return
	}
	st, err := os.Stat(stateFile(d))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("state: %v %v", st, err)
	}
	StartLink(context.Background(), d, c)
	if st, _ := os.Stat(linkFile(d)); st == nil || st.Mode().Perm() != 0o600 {
		t.Error("link state not 0600")
	}
}

func TestTicketAndWaitlist(t *testing.T) {
	d, _, c := setup(t)
	mux := http.NewServeMux()
	var got map[string]string
	mux.HandleFunc("/v1/account/ticket", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer free" {
			w.WriteHeader(403)
			w.Write([]byte(`{"error":"not_entitled"}`))
			return
		}
		w.Write([]byte(`{"ticket":"p0t1.x.y","expires_at":"2026-11-12T20:10:00Z","sandbox_url":"https://sbx.example"}`))
	})
	mux.HandleFunc("/v1/waitlist", func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(204)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c.Base = srv.URL
	_ = d
	tk, err := c.Ticket(context.Background(), "p0d_abc", "sandbox")
	if err != nil || tk.Ticket != "p0t1.x.y" || tk.ExpiresAt.IsZero() || tk.SandboxURL != "https://sbx.example" {
		t.Fatal(tk, err)
	}
	if _, err := c.Ticket(context.Background(), "free", "sandbox"); err != ErrNotEntitled {
		t.Errorf("err %v", err)
	}
	if err := c.Waitlist(context.Background(), "a@example.com", "pro"); err != nil || got["source"] != "client" {
		t.Errorf("%v %v", err, got)
	}
}
