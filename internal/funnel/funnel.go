// Package funnel keeps the opt-in, anonymous usage counts: how many times each of a few milestones
// happened in each ISO week, under a random install id of their own. Nothing is counted unless the
// user said yes (funnel.send in budget.toml); a finished week goes out once, after the p0ssess page
// has shown it. No prompt, file, path, model, cost, account or time of day is ever part of it.
package funnel

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"p0ssess/internal/budget"
	"p0ssess/internal/pme/paths"
)

// The milestones, in funnel order.
const (
	Install        = "install"
	TourDone       = "tour_done"
	FirstRoute     = "first_route"
	FirstSaving    = "first_saving"
	RescueOffered  = "rescue_offered"
	RescueUsed     = "rescue_used"
	UpgradeViewed  = "upgrade_viewed"
	UpgradeClicked = "upgrade_clicked"
	DeviceLinked   = "device_linked"
)

// Events lists every milestone in funnel order.
var Events = []string{Install, TourDone, FirstRoute, FirstSaving, RescueOffered, RescueUsed, UpgradeViewed, UpgradeClicked, DeviceLinked}

// once are the milestones counted at most once per install id.
var once = map[string]bool{Install: true, TourDone: true, FirstRoute: true, FirstSaving: true, DeviceLinked: true}

// MaxWeeks is how many weeks back a count may go out; older ones are dropped unsent.
const MaxWeeks = 8

// state is the local file. Weeks holds the counts not sent yet; Seen the week each once-milestone was
// counted in.
type state struct {
	ID    string                    `json:"id"`
	Weeks map[string]map[string]int `json:"weeks,omitempty"`
	Seen  map[string]string         `json:"seen,omitempty"`
}

func file(d paths.Dirs) string { return filepath.Join(d.State, "funnel.json") }

// Week is the ISO week of t in UTC, such as "2026-W46".
func Week(t time.Time) string {
	y, w := t.UTC().ISOWeek()
	return fmt.Sprintf("%04d-W%02d", y, w)
}

// Enabled reports whether usage counts are on. P0SSESS_FUNNEL=off forces them off (tests, the gate).
func Enabled(d paths.Dirs) bool {
	if v := strings.ToLower(os.Getenv("P0SSESS_FUNNEL")); v == "off" || v == "0" || v == "false" {
		return false
	}
	b, err := budget.Load(d.BudgetFile())
	return err == nil && b.Funnel.Send
}

func read(d paths.Dirs) state {
	var st state
	if b, err := os.ReadFile(file(d)); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	if st.Weeks == nil {
		st.Weeks = map[string]map[string]int{}
	}
	if st.Seen == nil {
		st.Seen = map[string]string{}
	}
	return st
}

func write(d paths.Dirs, st state) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return paths.WriteFileAtomic(file(d), append(b, '\n'), 0o600)
}

func newID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "p0f_" + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

// update runs fn on the state under the file's lock.
func update(d paths.Dirs, fn func(*state) error) error {
	if err := os.MkdirAll(d.State, 0o700); err != nil {
		return err
	}
	return paths.WithLock(file(d), func() error {
		st := read(d)
		if st.ID == "" {
			id, err := newID()
			if err != nil {
				return err
			}
			st.ID = id
		}
		if err := fn(&st); err != nil {
			return err
		}
		return write(d, st)
	})
}

func add(st *state, event string, now time.Time) {
	if once[event] && st.Seen[event] != "" {
		return
	}
	w := Week(now)
	if st.Weeks[w] == nil {
		st.Weeks[w] = map[string]int{}
	}
	st.Weeks[w][event]++
	if once[event] {
		st.Seen[event] = w
	}
}

func known(event string) bool {
	for _, e := range Events {
		if e == event {
			return true
		}
	}
	return false
}

// Note counts one milestone, when usage counts are on. It never fails loudly: a count is not worth
// an error anywhere.
func Note(d paths.Dirs, event string, now time.Time) {
	if !known(event) || !Enabled(d) {
		return
	}
	_ = update(d, func(st *state) error {
		add(st, event, now)
		return nil
	})
}

// Reached is what this install already did before usage counts were turned on, so the funnel does
// not miss the steps of someone who says yes late.
type Reached struct {
	TourDone, Routed, Saved, Linked bool
}

// Start begins counting after the user said yes: it makes the id and counts the install, plus each
// milestone the install has already reached, once, in the current week.
func Start(d paths.Dirs, r Reached, now time.Time) error {
	return update(d, func(st *state) error {
		add(st, Install, now)
		for ev, ok := range map[string]bool{TourDone: r.TourDone, FirstRoute: r.Routed, FirstSaving: r.Saved, DeviceLinked: r.Linked} {
			if ok {
				add(st, ev, now)
			}
		}
		return nil
	})
}

// Stop forgets everything: the id and every count not sent. A later yes starts over with a new id.
func Stop(d paths.Dirs) error {
	err := paths.WithLock(file(d), func() error { return os.Remove(file(d)) })
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// WeekCounts is one week as it goes on the wire.
type WeekCounts struct {
	Week   string         `json:"week"`
	Counts map[string]int `json:"counts"`
}

// Batch is the request body.
type Batch struct {
	V     int          `json:"v"`
	ID    string       `json:"id"`
	Weeks []WeekCounts `json:"weeks"`
}

// pending is the batch of finished weeks still to send: the current week stays until it ends, and
// weeks older than MaxWeeks are left out.
func pending(st state, now time.Time) Batch {
	cur, earliest := Week(now), Week(now.AddDate(0, 0, -7*MaxWeeks))
	b := Batch{V: 1, ID: st.ID}
	for w, c := range st.Weeks {
		if w < cur && w >= earliest && len(c) > 0 {
			b.Weeks = append(b.Weeks, WeekCounts{Week: w, Counts: c})
		}
	}
	sort.Slice(b.Weeks, func(i, j int) bool { return b.Weeks[i].Week < b.Weeks[j].Week })
	return b
}

// View is what the p0ssess page shows: whether counts are on, the install id, the next batch exactly as
// it will be sent, and the current week's counts that are still building.
type View struct {
	Send     bool       `json:"send"`
	ID       string     `json:"id,omitempty"`
	Pending  string     `json:"pending,omitempty"`
	ThisWeek WeekCounts `json:"this_week"`
}

// Show builds the page's view.
func Show(d paths.Dirs, now time.Time) View {
	v := View{Send: Enabled(d)}
	if !v.Send {
		return v
	}
	st := read(d)
	v.ID = st.ID
	if b := pending(st, now); len(b.Weeks) > 0 {
		v.Pending, _ = Body(b)
	}
	w := Week(now)
	v.ThisWeek = WeekCounts{Week: w, Counts: st.Weeks[w]}
	if v.ThisWeek.Counts == nil {
		v.ThisWeek.Counts = map[string]int{}
	}
	return v
}

// Body encodes a batch exactly as it is sent.
func Body(b Batch) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(b); err != nil {
		return "", err
	}
	return strings.TrimRight(buf.String(), "\n"), nil
}

// Poster sends a batch and returns the HTTP status.
type Poster func(ctx context.Context, body []byte) (int, error)

// Send sends the finished weeks not sent yet, when usage counts are on. Sent weeks, and weeks too old
// to send, are dropped from the file. A batch the server refuses as invalid (400, 413) is dropped too,
// since it would never be accepted; anything else keeps it for the next try.
func Send(ctx context.Context, d paths.Dirs, post Poster, now time.Time) (int, error) {
	if !Enabled(d) {
		return 0, nil
	}
	if _, err := os.Stat(file(d)); err != nil {
		return 0, nil
	}
	sent := 0
	err := update(d, func(st *state) error {
		earliest := Week(now.AddDate(0, 0, -7*MaxWeeks))
		for w := range st.Weeks {
			if w < earliest {
				delete(st.Weeks, w)
			}
		}
		b := pending(*st, now)
		if len(b.Weeks) == 0 {
			return nil
		}
		body, err := Body(b)
		if err != nil {
			return err
		}
		status, perr := post(ctx, []byte(body))
		switch {
		case status >= 200 && status < 300:
			sent = len(b.Weeks)
		case status == 400 || status == 413:
		default:
			if perr == nil {
				perr = fmt.Errorf("funnel: server answered %d", status)
			}
			return perr
		}
		for _, w := range b.Weeks {
			delete(st.Weeks, w.Week)
		}
		return nil
	})
	return sent, err
}
