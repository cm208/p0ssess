package funnel

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"p0ssess/internal/budget"
	"p0ssess/internal/pme/paths"
)

func dirs(t *testing.T, on bool) paths.Dirs {
	t.Helper()
	t.Setenv("P0SSESS_HOME", t.TempDir())
	t.Setenv("P0SSESS_FUNNEL", "")
	d := paths.Default()
	if on {
		b := budget.Default()
		b.Funnel.Send = true
		os.MkdirAll(d.Config, 0o700)
		if err := budget.Save(d.BudgetFile(), b); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

var monday = time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC) // 2026-W47

func TestOffByDefault(t *testing.T) {
	d := dirs(t, false)
	if Enabled(d) {
		t.Fatal("on by default")
	}
	if budget.Default().Funnel.Send {
		t.Fatal("new installs count by default")
	}
	Note(d, Install, monday)
	Note(d, UpgradeViewed, monday)
	if _, err := os.Stat(file(d)); !os.IsNotExist(err) {
		t.Fatalf("counted while off: %v", err)
	}
	calls := 0
	n, err := Send(context.Background(), d, func(context.Context, []byte) (int, error) { calls++; return 202, nil }, monday.AddDate(0, 0, 14))
	if n != 0 || err != nil || calls != 0 {
		t.Fatalf("sent while off: %d %v %d", n, err, calls)
	}
	if v := Show(d, monday); v.Send || v.ID != "" || v.Pending != "" {
		t.Fatalf("view while off: %+v", v)
	}
}

func TestEnvTurnsItOff(t *testing.T) {
	d := dirs(t, true)
	t.Setenv("P0SSESS_FUNNEL", "off")
	if Enabled(d) {
		t.Fatal("P0SSESS_FUNNEL=off ignored")
	}
}

func TestCountsWeeklyAndSendsFinishedWeeks(t *testing.T) {
	d := dirs(t, true)
	if err := Start(d, Reached{TourDone: true}, monday); err != nil {
		t.Fatal(err)
	}
	Note(d, Install, monday) // once per install
	Note(d, TourDone, monday)
	Note(d, FirstRoute, monday.Add(time.Hour))
	Note(d, FirstRoute, monday.Add(2*time.Hour))
	Note(d, UpgradeViewed, monday)
	Note(d, UpgradeViewed, monday.Add(time.Hour))
	Note(d, "prompt", monday)
	next := monday.AddDate(0, 0, 7)
	Note(d, FirstSaving, next)
	Note(d, UpgradeViewed, next)

	// While a week runs it is not sent; the page shows it building.
	v := Show(d, monday.Add(3*time.Hour))
	if !v.Send || !regexp.MustCompile(`^p0f_[A-Za-z0-9_-]{22}$`).MatchString(v.ID) || v.Pending != "" {
		t.Fatalf("view in week: %+v", v)
	}
	if v.ThisWeek.Counts[FirstRoute] != 1 || v.ThisWeek.Counts[UpgradeViewed] != 2 || v.ThisWeek.Counts[Install] != 1 {
		t.Fatalf("this week: %+v", v.ThisWeek)
	}
	var posted [][]byte
	post := func(_ context.Context, b []byte) (int, error) { posted = append(posted, b); return 202, nil }
	if n, _ := Send(context.Background(), d, post, monday.Add(3*time.Hour)); n != 0 || len(posted) != 0 {
		t.Fatal("sent a week that has not ended")
	}

	// After W47 ends, the page shows exactly the body that goes.
	now := next.Add(time.Hour)
	v = Show(d, now)
	want := `{"v":1,"id":"` + v.ID + `","weeks":[{"week":"2026-W47","counts":{"first_route":1,"install":1,"tour_done":1,"upgrade_viewed":2}}]}`
	if v.Pending != want {
		t.Fatalf("pending\n%s\nwant\n%s", v.Pending, want)
	}
	if n, err := Send(context.Background(), d, post, now); n != 1 || err != nil || len(posted) != 1 || string(posted[0]) != want {
		t.Fatalf("send: %d %v %q", n, err, posted)
	}
	var b map[string]any
	json.Unmarshal(posted[0], &b)
	if len(b) != 3 {
		t.Fatalf("body has fields beyond v, id and weeks: %v", b)
	}
	// Sent once; W48 is still building.
	if n, _ := Send(context.Background(), d, post, now); n != 0 || len(posted) != 1 {
		t.Fatal("sent twice")
	}
	if c := Show(d, now).ThisWeek.Counts; c[FirstSaving] != 1 || c[UpgradeViewed] != 1 {
		t.Fatalf("W48: %v", c)
	}
}

func TestSendKeepsOnServerTroubleAndDropsRefusals(t *testing.T) {
	d := dirs(t, true)
	Start(d, Reached{}, monday)
	later := monday.AddDate(0, 0, 8)
	down := func(context.Context, []byte) (int, error) { return 0, errors.New("offline") }
	if _, err := Send(context.Background(), d, down, later); err == nil {
		t.Fatal("no error offline")
	}
	busy := func(context.Context, []byte) (int, error) { return 503, nil }
	if _, err := Send(context.Background(), d, busy, later); err == nil {
		t.Fatal("no error on 503")
	}
	if Show(d, later).Pending == "" {
		t.Fatal("batch lost after a failure")
	}
	refused := func(context.Context, []byte) (int, error) { return 400, nil }
	Send(context.Background(), d, refused, later)
	if Show(d, later).Pending != "" {
		t.Fatal("refused batch kept forever")
	}
}

func TestOldWeeksAreDroppedUnsent(t *testing.T) {
	d := dirs(t, true)
	Start(d, Reached{Linked: true, Routed: true, Saved: true}, monday)
	far := monday.AddDate(0, 0, 7*(MaxWeeks+2))
	calls := 0
	Send(context.Background(), d, func(context.Context, []byte) (int, error) { calls++; return 202, nil }, far)
	if calls != 0 {
		t.Fatal("sent a week older than MaxWeeks")
	}
	if strings.Contains(Show(d, far).Pending, "2026-W47") {
		t.Fatal("old week still pending")
	}
}

func TestStopForgetsEverything(t *testing.T) {
	d := dirs(t, true)
	Start(d, Reached{}, monday)
	id := Show(d, monday).ID
	if err := Stop(d); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file(d)); !os.IsNotExist(err) {
		t.Fatal("state kept after Stop")
	}
	if err := Stop(d); err != nil {
		t.Fatal("second Stop:", err)
	}
	Start(d, Reached{}, monday)
	if Show(d, monday).ID == id {
		t.Fatal("a new start kept the old id")
	}
}

func TestStartCountsReachedOnce(t *testing.T) {
	d := dirs(t, true)
	Start(d, Reached{TourDone: true, Routed: true, Saved: true, Linked: true}, monday)
	Start(d, Reached{TourDone: true, Routed: true, Saved: true, Linked: true}, monday)
	c := Show(d, monday).ThisWeek.Counts
	for _, e := range []string{Install, TourDone, FirstRoute, FirstSaving, DeviceLinked} {
		if c[e] != 1 {
			t.Errorf("%s = %d", e, c[e])
		}
	}
	if len(c) != 5 {
		t.Fatalf("counts %v", c)
	}
}
