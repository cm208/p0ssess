package team

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"p0ssess/internal/account"
	"p0ssess/internal/pme/paths"
)

const (
	// MaxBatch is the most runs one spend report carries.
	MaxBatch = 500
	// maxAge is how old a queued run may be and still be sent (the server takes 62 days).
	maxAge = 60 * 24 * time.Hour
)

// Run is one paid run exactly as a spend report carries it.
type Run struct {
	Day     string  `json:"day"`
	UseCase string  `json:"use_case"`
	Model   string  `json:"model"`
	Route   string  `json:"route"`
	CostUSD float64 `json:"cost_usd"`
}

// queued is a run in the local queue. RunID stays on this machine: it lets a measured cost replace
// an estimate before the run is sent.
type queued struct {
	Run
	RunID string `json:"run,omitempty"`
}

var (
	useCaseRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	modelRE   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,39}/[a-z0-9][a-z0-9._:-]{0,79}$`)
)

func validRoute(r string) bool {
	switch r {
	case "direct", "agent", "claude-subagent", "engine", "calibrate":
		return true
	}
	return false
}

func (r Run) valid() bool {
	return validRoute(r.Route) && modelRE.MatchString(r.Model) && (r.UseCase == "" || useCaseRE.MatchString(r.UseCase)) &&
		r.CostUSD >= 0 && r.CostUSD <= 1000 && !math.IsNaN(r.CostUSD)
}

// NewRun is the report for a run that ended at t.
func NewRun(t time.Time, useCase, model, route string, cost float64) Run {
	return Run{Day: t.UTC().Format("2006-01-02"), UseCase: useCase, Model: model, Route: route, CostUSD: cost}
}

func queueFile(d paths.Dirs) string { return filepath.Join(d.State, "team-spend.jsonl") }

func lockQueue(d paths.Dirs) (func(), error) {
	if err := os.MkdirAll(d.State, 0o700); err != nil {
		return nil, err
	}
	return paths.LockFile(queueFile(d) + ".lock")
}

// Enqueue queues a paid run for the org when a policy is in force and asks for spend reports. Runs
// that cost nothing or that a report cannot carry are left out. runID is kept locally only, for an
// estimated cost that Settle may replace.
func Enqueue(d paths.Dirs, keys map[string]ed25519.PublicKey, r Run, runID string) error {
	if !(r.CostUSD > 0) || !r.valid() {
		return nil
	}
	p := Load(d, keys)
	if p == nil || !p.ReportSpend {
		return nil
	}
	b, err := json.Marshal(queued{Run: r, RunID: runID})
	if err != nil {
		return err
	}
	unlock, err := lockQueue(d)
	if err != nil {
		return err
	}
	defer unlock()
	f, err := os.OpenFile(queueFile(d), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func readQueue(d paths.Dirs) []queued {
	b, err := os.ReadFile(queueFile(d))
	if err != nil {
		return nil
	}
	var out []queued
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var q queued
		if json.Unmarshal(sc.Bytes(), &q) == nil {
			out = append(out, q)
		}
	}
	return out
}

func writeQueue(d paths.Dirs, qs []queued) error {
	if len(qs) == 0 {
		err := os.Remove(queueFile(d))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var buf bytes.Buffer
	for _, q := range qs {
		b, err := json.Marshal(q)
		if err != nil {
			return err
		}
		buf.Write(append(b, '\n'))
	}
	return paths.WriteFileAtomic(queueFile(d), buf.Bytes(), 0o600)
}

func clearQueue(d paths.Dirs) {
	unlock, err := lockQueue(d)
	if err != nil {
		os.Remove(queueFile(d))
		return
	}
	defer unlock()
	os.Remove(queueFile(d))
}

// Queued is the runs waiting to be sent.
func Queued(d paths.Dirs) []Run {
	var out []Run
	for _, q := range readQueue(d) {
		out = append(out, q.Run)
	}
	return out
}

// Settle replaces a queued run's estimated cost with the measured one. A run already sent keeps the
// estimate the org was told.
func Settle(d paths.Dirs, runID string, cost float64) error {
	if runID == "" {
		return nil
	}
	unlock, err := lockQueue(d)
	if err != nil {
		return err
	}
	defer unlock()
	qs := readQueue(d)
	changed := false
	for i := range qs {
		if qs[i].RunID == runID {
			qs[i].CostUSD, qs[i].RunID, changed = cost, "", true
		}
	}
	if !changed {
		return nil
	}
	return writeQueue(d, qs)
}

// next drops what can no longer be sent (free runs, bad rows, runs older than the server takes) and
// returns the first batch.
func next(d paths.Dirs, now time.Time) ([]Run, error) {
	unlock, err := lockQueue(d)
	if err != nil {
		return nil, err
	}
	defer unlock()
	qs := readQueue(d)
	oldest := now.UTC().Add(-maxAge).Format("2006-01-02")
	keep := qs[:0:0]
	for _, q := range qs {
		if q.CostUSD > 0 && q.valid() && q.Day >= oldest {
			keep = append(keep, q)
		}
	}
	if len(keep) != len(qs) {
		if err := writeQueue(d, keep); err != nil {
			return nil, err
		}
	}
	var batch []Run
	for _, q := range keep {
		if len(batch) == MaxBatch {
			break
		}
		batch = append(batch, q.Run)
	}
	return batch, nil
}

// drop removes the first n queued runs.
func drop(d paths.Dirs, n int) error {
	unlock, err := lockQueue(d)
	if err != nil {
		return err
	}
	defer unlock()
	qs := readQueue(d)
	if n > len(qs) {
		n = len(qs)
	}
	return writeQueue(d, qs[n:])
}

var nowFn = time.Now

// Flush sends the queued runs, MaxBatch at a time. A 2xx removes what was sent; not_in_team or
// not_entitled deletes the queue and the policy; a batch the server refuses as bad can never pass and
// is dropped; any other failure keeps the queue for the next try. It returns how many runs were sent.
func Flush(ctx context.Context, c *Client) (int, error) {
	d := c.Dirs
	tok := account.Token(d)
	if tok == "" {
		return 0, nil
	}
	if _, err := os.Stat(queueFile(d)); err != nil {
		return 0, nil
	}
	if err := os.MkdirAll(d.State, 0o700); err != nil {
		return 0, err
	}
	unlock, err := paths.LockFile(queueFile(d) + ".send")
	if err != nil {
		return 0, err
	}
	defer unlock()
	sent := 0
	for {
		batch, err := next(d, nowFn())
		if err != nil || len(batch) == 0 {
			return sent, err
		}
		body := struct {
			V    int   `json:"v"`
			Runs []Run `json:"runs"`
		}{1, batch}
		_, err = c.API.Do(ctx, "POST", "/v1/team/spend", tok, body, nil)
		switch {
		case err == nil:
			sent += len(batch)
		case gone(err):
			Forget(d)
			return sent, nil
		case Code(err) == "bad_request":
		default:
			return sent, err
		}
		if err := drop(d, len(batch)); err != nil {
			return sent, err
		}
	}
}
