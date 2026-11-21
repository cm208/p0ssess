package cloudsandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"p0ssess/internal/pme/paths"
	"p0ssess/internal/proc"
)

// The cloud sandbox runs one session at a time per slot. When every slot is taken the host queues the
// request and answers with a place in line and an estimate (api/sandbox.md, "Waiting for a slot"); this
// file keeps the request alive, shows the place, and gives up after the user's limit.

// QueueInfo is where a waiting request stands.
type QueueInfo struct {
	Position int
	ETA      time.Duration
	HasETA   bool
	Ready    bool
}

// WaitLimitError means the wait passed the user's limit; the request was withdrawn.
type WaitLimitError struct{ Waited time.Duration }

func (e *WaitLimitError) Error() string {
	return fmt.Sprintf("The cloud sandbox stayed busy for %s, so this job was not started. Try again in a while, or say \"wait longer for the cloud sandbox\" to raise the limit.", minutes(e.Waited))
}

// Usage is this month's sandbox hours.
type Usage struct {
	Month     string    `json:"month"`
	UsedS     int64     `json:"used_s"`
	AllowedS  int64     `json:"allowance_s"`
	LeftS     int64     `json:"left_s"`
	CheckedAt time.Time `json:"checked_at"`
}

// WaitOptions sets how WaitForSlot behaves. Ticket is called for every request: a ticket is short-lived.
type WaitOptions struct {
	MaxWait  time.Duration
	Ticket   func(ctx context.Context) (string, error)
	OnUpdate func(QueueInfo)
	// Sleep and Now are replaced in tests.
	Sleep func(ctx context.Context, d time.Duration) error
	Now   func() time.Time
}

func (o WaitOptions) sleep(ctx context.Context, d time.Duration) error {
	if o.Sleep != nil {
		return o.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (o WaitOptions) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

type queueAnswer struct {
	Queue    string `json:"queue"`
	Secret   string `json:"secret"`
	State    string `json:"state"`
	Position int    `json:"position"`
	ETAS     *int   `json:"eta_s"`
	PollS    int    `json:"poll_s"`
}

func (a queueAnswer) info() QueueInfo {
	qi := QueueInfo{Position: a.Position, Ready: a.State == "ready"}
	if a.ETAS != nil {
		qi.ETA, qi.HasETA = time.Duration(*a.ETAS)*time.Second, true
	}
	return qi
}

func hostBase(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("cloud sandbox URL %q must be https", raw)
	}
	return u, nil
}

func (o WaitOptions) call(ctx context.Context, hc *http.Client, method, u, auth string, body []byte) (*queueAnswer, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+auth)
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("cloud sandbox: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil, resp.StatusCode, nil
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return nil, resp.StatusCode, readAPIError(resp)
	}
	var a queueAnswer
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&a); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("cloud sandbox: queue answer: %w", err)
	}
	return &a, resp.StatusCode, nil
}

// WaitForSlot joins the host's queue and returns once a slot is held for this account; the caller then
// creates its session at once. It returns a *WaitLimitError after MaxWait, and the host's own error for
// anything else (no hours left, a plan without the sandbox, a full queue). The entry is withdrawn on any
// way out but success.
func WaitForSlot(ctx context.Context, hc *http.Client, host string, o WaitOptions) error {
	base, err := hostBase(host)
	if err != nil {
		return err
	}
	start := o.now()
	var id, secret string
	withdraw := func() {
		if id == "" {
			return
		}
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		o.call(c, hc, http.MethodDelete, base.String()+"/v1/queue/"+id, secret, nil)
	}
	join := func() (*queueAnswer, error) {
		tk, err := o.Ticket(ctx)
		if err != nil {
			return nil, err
		}
		a, _, err := o.call(ctx, hc, http.MethodPost, base.String()+"/v1/queue", tk, []byte(`{"v":1}`))
		if err != nil {
			return nil, err
		}
		if !strings.HasPrefix(a.Queue, "q_") || !strings.HasPrefix(a.Secret, "p0q_") || strings.ContainsAny(a.Queue, "/?#%") {
			return nil, errors.New("cloud sandbox: queue answer is malformed")
		}
		id, secret = a.Queue, a.Secret
		return a, nil
	}
	a, err := join()
	if err != nil {
		return err
	}
	for {
		if o.OnUpdate != nil {
			o.OnUpdate(a.info())
		}
		if a.State == "ready" {
			return nil
		}
		waited := o.now().Sub(start)
		if waited >= o.MaxWait {
			withdraw()
			return &WaitLimitError{Waited: waited}
		}
		poll := time.Duration(a.PollS) * time.Second
		if poll < time.Second || poll > 15*time.Second {
			poll = 5 * time.Second
		}
		if left := o.MaxWait - waited; poll > left {
			poll = left
		}
		if err := o.sleep(ctx, poll); err != nil {
			withdraw()
			return err
		}
		next, status, err := o.call(ctx, hc, http.MethodGet, base.String()+"/v1/queue/"+id, secret, nil)
		switch {
		case err == nil:
			a = next
		case status == http.StatusNotFound:
			// The host dropped the entry (a restart, or polls that came too late): get back in line.
			if a, err = join(); err != nil {
				return err
			}
		case ctx.Err() != nil:
			withdraw()
			return ctx.Err()
		default:
			// A network blip keeps the entry for now; the host holds it for 45 seconds.
			if o.now().Sub(start) >= o.MaxWait {
				withdraw()
				return &WaitLimitError{Waited: o.now().Sub(start)}
			}
		}
	}
}

// FetchUsage asks the sandbox host for this month's hours.
func FetchUsage(ctx context.Context, hc *http.Client, host, ticket string) (Usage, error) {
	base, err := hostBase(host)
	if err != nil {
		return Usage{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String()+"/v1/usage", nil)
	if err != nil {
		return Usage{}, err
	}
	req.Header.Set("Authorization", "Bearer "+ticket)
	resp, err := hc.Do(req)
	if err != nil {
		return Usage{}, fmt.Errorf("cloud sandbox: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Usage{}, readAPIError(resp)
	}
	var u Usage
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&u); err != nil {
		return Usage{}, err
	}
	return u, nil
}

// Hours renders seconds as hours for people: 20h, 2.5h, 0h.
func Hours(s int64) string {
	h := float64(s) / 3600
	if h == float64(int64(h)) {
		return fmt.Sprintf("%dh", int64(h))
	}
	return fmt.Sprintf("%.1fh", h)
}

// UsageLine is "3h used, 17h left this month".
func (u Usage) UsageLine() string {
	return fmt.Sprintf("%s used, %s left this month", Hours(u.UsedS), Hours(u.LeftS))
}

// Ordinal renders 1 as 1st, 2 as 2nd, 11 as 11th.
func Ordinal(n int) string {
	suffix := "th"
	if n%100 < 11 || n%100 > 13 {
		switch n % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return fmt.Sprintf("%d%s", n, suffix)
}

func minutes(d time.Duration) string {
	m := int((d + 59*time.Second) / time.Minute)
	if m < 1 {
		m = 1
	}
	return fmt.Sprintf("%d min", m)
}

// QueuedText is what the status line and the page say: "queued 2nd, ~4 min". With no estimate yet it
// is just "queued 2nd".
func (q QueueInfo) QueuedText() string {
	if q.Ready {
		return "starting"
	}
	t := "queued " + Ordinal(q.Position)
	if q.HasETA {
		t += ", ~" + minutes(q.ETA)
	}
	return t
}

// QueueState is the file the status line and the page read while a job waits.
type QueueState struct {
	PID      int       `json:"pid"`
	Position int       `json:"position"`
	ETAS     int       `json:"eta_s"` // -1 until the host can estimate
	Since    time.Time `json:"since"`
	Updated  time.Time `json:"updated"`
}

// queueFresh is how old an update may be before the status line stops showing it.
const queueFresh = 40 * time.Second

func QueueFile(d paths.Dirs) string { return filepath.Join(d.State, "sandbox-queue.json") }

// SaveQueue records where a waiting job stands.
func SaveQueue(d paths.Dirs, q QueueInfo, since, now time.Time) {
	st := QueueState{PID: os.Getpid(), Position: q.Position, ETAS: -1, Since: since.UTC(), Updated: now.UTC()}
	if q.HasETA {
		st.ETAS = int(q.ETA / time.Second)
	}
	b, _ := json.Marshal(st)
	_ = paths.WriteFileAtomic(QueueFile(d), b, 0o600)
}

// ClearQueue removes the file when the wait is over.
func ClearQueue(d paths.Dirs) { os.Remove(QueueFile(d)) }

// Queued reads the file; ok only while a live process is waiting and has updated it lately.
func Queued(d paths.Dirs, now time.Time) (QueueInfo, time.Time, bool) {
	b, err := os.ReadFile(QueueFile(d))
	if err != nil {
		return QueueInfo{}, time.Time{}, false
	}
	var st QueueState
	if json.Unmarshal(b, &st) != nil || st.Position < 1 || now.Sub(st.Updated) > queueFresh || !proc.Alive(st.PID) {
		return QueueInfo{}, time.Time{}, false
	}
	q := QueueInfo{Position: st.Position}
	if st.ETAS >= 0 {
		q.ETA, q.HasETA = time.Duration(st.ETAS)*time.Second, true
	}
	return q, st.Since, true
}
