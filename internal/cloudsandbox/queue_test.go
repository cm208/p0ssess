package cloudsandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"p0ssess/internal/pme/paths"
)

// queueHost is a host whose queue answers from a script: one answer per call.
type queueHost struct {
	mu      sync.Mutex
	answers []string // JSON for POST /v1/queue, then each GET
	status  []int
	calls   []string
	deleted bool
}

func (h *queueHost) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, r.Method+" "+r.URL.Path)
	if r.Method == "DELETE" {
		h.deleted = true
		w.WriteHeader(204)
		return
	}
	if len(h.answers) == 0 {
		w.WriteHeader(500)
		return
	}
	a, st := h.answers[0], 200
	h.answers = h.answers[1:]
	if len(h.status) > 0 {
		st, h.status = h.status[0], h.status[1:]
	}
	w.WriteHeader(st)
	fmt.Fprint(w, a)
}

func tlsHost(t *testing.T, h http.Handler) (*httptest.Server, *http.Client) {
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	return srv, srv.Client()
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }
func (c *fakeClock) sleep(_ context.Context, d time.Duration) error {
	c.t = c.t.Add(d)
	return nil
}

func opts(c *fakeClock, max time.Duration, got *[]QueueInfo) WaitOptions {
	return WaitOptions{MaxWait: max, Now: c.now, Sleep: c.sleep,
		Ticket:   func(context.Context) (string, error) { return "p0t1.a.b", nil },
		OnUpdate: func(q QueueInfo) { *got = append(*got, q) }}
}

func TestWaitForSlotReportsPlaceThenReady(t *testing.T) {
	h := &queueHost{answers: []string{
		`{"queue":"q_1","secret":"p0q_s","state":"queued","position":3,"eta_s":600,"poll_s":5}`,
		`{"queue":"q_1","state":"queued","position":2,"eta_s":240,"poll_s":5}`,
		`{"queue":"q_1","state":"queued","position":1}`,
		`{"queue":"q_1","state":"ready","position":1,"hold_s":90}`,
	}}
	srv, hc := tlsHost(t, h)
	c := &fakeClock{t: time.Unix(1000, 0)}
	var got []QueueInfo
	if err := WaitForSlot(context.Background(), hc, srv.URL, opts(c, 10*time.Minute, &got)); err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, q := range got {
		lines = append(lines, q.QueuedText())
	}
	want := "queued 3rd, ~10 min|queued 2nd, ~4 min|queued 1st|starting"
	if strings.Join(lines, "|") != want {
		t.Fatalf("%v", lines)
	}
	if c.t.Sub(time.Unix(1000, 0)) != 15*time.Second {
		t.Errorf("polled every 5s: waited %v", c.t.Sub(time.Unix(1000, 0)))
	}
	if h.deleted {
		t.Error("a request that got its slot is not withdrawn")
	}
}

func TestWaitForSlotGivesUpAndWithdraws(t *testing.T) {
	h := &queueHost{}
	for i := 0; i < 20; i++ {
		h.answers = append(h.answers, `{"queue":"q_1","secret":"p0q_s","state":"queued","position":4,"poll_s":5}`)
	}
	srv, hc := tlsHost(t, h)
	c := &fakeClock{t: time.Unix(0, 0)}
	var got []QueueInfo
	err := WaitForSlot(context.Background(), hc, srv.URL, opts(c, time.Minute, &got))
	var wl *WaitLimitError
	if !errors.As(err, &wl) {
		t.Fatalf("%v", err)
	}
	if !h.deleted {
		t.Error("the queue entry was not withdrawn")
	}
	if want := "The cloud sandbox stayed busy for 1 min, so this job was not started."; !strings.HasPrefix(err.Error(), want) {
		t.Errorf("%q", err.Error())
	}
	if c.t.Sub(time.Unix(0, 0)) > time.Minute {
		t.Errorf("waited past the limit: %v", c.t.Sub(time.Unix(0, 0)))
	}
}

func TestWaitForSlotRejoinsWhenDropped(t *testing.T) {
	h := &queueHost{answers: []string{
		`{"queue":"q_1","secret":"p0q_s","state":"queued","position":1,"poll_s":5}`,
		`{"error":"not_found"}`,
		`{"queue":"q_2","secret":"p0q_t","state":"ready","position":1,"hold_s":90}`,
	}, status: []int{202, 404, 202}}
	srv, hc := tlsHost(t, h)
	var got []QueueInfo
	if err := WaitForSlot(context.Background(), hc, srv.URL, opts(&fakeClock{t: time.Unix(0, 0)}, 5*time.Minute, &got)); err != nil {
		t.Fatal(err)
	}
}

func TestWaitForSlotHostErrors(t *testing.T) {
	for code, status := range map[string]int{"allowance_used": 429, "queue_full": 503, "not_entitled": 403} {
		h := &queueHost{answers: []string{fmt.Sprintf(`{"error":%q,"message":"m"}`, code)}, status: []int{status}}
		srv, hc := tlsHost(t, h)
		var got []QueueInfo
		err := WaitForSlot(context.Background(), hc, srv.URL, opts(&fakeClock{}, time.Minute, &got))
		var ae *APIError
		if !errors.As(err, &ae) || ae.Code != code {
			t.Errorf("%s: %v", code, err)
		}
		if len(got) != 0 {
			t.Errorf("%s: no update for an error", code)
		}
	}
}

func TestQueuedText(t *testing.T) {
	for _, c := range []struct {
		q    QueueInfo
		want string
	}{
		{QueueInfo{Position: 2, ETA: 4 * time.Minute, HasETA: true}, "queued 2nd, ~4 min"},
		{QueueInfo{Position: 1, ETA: 20 * time.Second, HasETA: true}, "queued 1st, ~1 min"},
		{QueueInfo{Position: 3, ETA: 3*time.Minute + 10*time.Second, HasETA: true}, "queued 3rd, ~4 min"},
		{QueueInfo{Position: 11}, "queued 11th"},
		{QueueInfo{Position: 22}, "queued 22nd"},
		{QueueInfo{Position: 1, Ready: true}, "starting"},
	} {
		if got := c.q.QueuedText(); got != c.want {
			t.Errorf("%+v: %q, want %q", c.q, got, c.want)
		}
	}
}

func TestQueueStateFile(t *testing.T) {
	d := paths.Dirs{State: t.TempDir()}
	now := time.Now()
	if _, _, ok := Queued(d, now); ok {
		t.Fatal("no file, no queue")
	}
	SaveQueue(d, QueueInfo{Position: 2, ETA: 4 * time.Minute, HasETA: true}, now.Add(-time.Minute), now)
	q, since, ok := Queued(d, now.Add(10*time.Second))
	if !ok || q.QueuedText() != "queued 2nd, ~4 min" || since.IsZero() {
		t.Fatalf("%+v %v %v", q, since, ok)
	}
	if _, _, ok := Queued(d, now.Add(2*time.Minute)); ok {
		t.Error("a stale update is not shown")
	}
	SaveQueue(d, QueueInfo{Position: 5}, now, now)
	if q, _, ok := Queued(d, now); !ok || q.HasETA || q.QueuedText() != "queued 5th" {
		t.Errorf("no estimate: %+v", q)
	}
	// A file left by a process that died is not shown.
	b, _ := os.ReadFile(QueueFile(d))
	var st QueueState
	json.Unmarshal(b, &st)
	st.PID = 2147483646
	b, _ = json.Marshal(st)
	os.WriteFile(QueueFile(d), b, 0o600)
	if _, _, ok := Queued(d, now); ok {
		t.Error("dead process")
	}
	ClearQueue(d)
	if _, err := os.Stat(QueueFile(d)); err == nil {
		t.Error("not cleared")
	}
}

func TestUsageAndHours(t *testing.T) {
	u := Usage{UsedS: 3 * 3600, LeftS: 17*3600 + 1800, AllowedS: 20 * 3600}
	if u.UsageLine() != "3h used, 17.5h left this month" {
		t.Errorf("%q", u.UsageLine())
	}
	h := &queueHost{answers: []string{`{"month":"2026-11","used_s":3600,"allowance_s":72000,"left_s":68400}`}}
	srv, hc := tlsHost(t, h)
	got, err := FetchUsage(context.Background(), hc, srv.URL, "p0t1.a.b")
	if err != nil || got.LeftS != 68400 || got.UsedS != 3600 {
		t.Fatalf("%+v %v", got, err)
	}
}
