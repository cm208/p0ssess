package usage

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func f(v float64) *float64 { return &v }
func i(v int64) *int64     { return &v }

func TestProject(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		five   *Window
		week   *Window
		at     time.Time
		known  bool
		want   float64
		window string
	}{
		{"half used at half time", &Window{f(50), i(now.Add(150 * time.Minute).Unix())}, nil, now, true, 1.0, "5-hour"},
		{"early window bounded by eMin", &Window{f(10), i(now.Add(299 * time.Minute).Unix())}, nil, now, true, 10.0 / 15, "5-hour"},
		{"weekly worse", &Window{f(10), i(now.Add(150 * time.Minute).Unix())}, &Window{f(70), i(now.Add(84 * time.Hour).Unix())}, now, true, 1.4, "weekly"},
		{"window reset since reading", &Window{f(90), i(now.Add(-time.Minute).Unix())}, nil, now.Add(-30 * time.Minute), true, 0, ""},
		{"stale reading", &Window{f(50), i(now.Add(time.Hour).Unix())}, nil, now.Add(-3 * time.Hour), false, 0, ""},
	}
	for _, tc := range cases {
		s := State{RateLimits: &RateLimits{FiveHour: tc.five, SevenDay: tc.week}, ReadingAt: tc.at}
		p := s.Project(now, 0.15, 2*time.Hour)
		if p.Known != tc.known || math.Abs(p.Projected-tc.want) > 1e-6 || p.Window != tc.window {
			t.Errorf("%s: got %+v", tc.name, p)
		}
	}
	if p := (State{}).Project(now, 0.15, time.Hour); p.Known {
		t.Error("no reading should be unknown")
	}
}

func TestRecordAndSessions(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, ".git"), 0o755)
	var s State
	in := StatusInput{SessionID: "abc", Cwd: dir}
	in.Model.ID = "claude-opus-5-5"
	s.Record(in, time.Now())
	if s.Subscriber || s.RateLimits != nil {
		t.Error("no rate_limits should not mark a subscriber")
	}
	in.RateLimits = &RateLimits{FiveHour: &Window{f(20), i(time.Now().Add(time.Hour).Unix())}}
	s.Record(in, time.Now())
	if !s.Subscriber || s.LeadModel != "claude-opus-5-5" {
		t.Errorf("record: %+v", s)
	}
	se, ok := s.SessionFor(filepath.Join(dir))
	if !ok || se.ID != "abc" || se.Project != dir {
		t.Errorf("SessionFor = %+v", se)
	}
	// Claude Code's project directory need not be a repository: a session that moved below it is
	// still found from it.
	launch := t.TempDir()
	s.TouchSession(Session{ID: "moved", Cwd: filepath.Join(launch, "repo"), Project: launch, Seen: time.Now()})
	if se, ok := s.SessionFor(launch); !ok || se.ID != "moved" {
		t.Errorf("SessionFor(project dir) = %+v %v", se, ok)
	}
	// Another project's session (and its session-scoped rules) never applies here.
	if se, ok := s.SessionFor(t.TempDir()); ok {
		t.Errorf("SessionFor fell back to %+v for an unrelated directory", se)
	}
	path := filepath.Join(dir, "usage.json")
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	back, err := Load(path)
	if err != nil || !back.Subscriber {
		t.Fatalf("round trip: %v %+v", err, back)
	}
}

func TestJobs(t *testing.T) {
	dir := t.TempDir()
	j, err := StartJob(dir, "google/gemini-3.8-flash", "video-understand", os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	StartJob(dir, "dead/model", "", 999999999)
	run := Running(dir, time.Now())
	if len(run) != 1 || run[0].ID != j.ID {
		t.Fatalf("running = %+v", run)
	}
	FinishJob(dir, j.ID)
	if len(Running(dir, time.Now())) != 0 {
		t.Error("finished job still listed")
	}
}

func TestConcurrentUpdatesAreAllKept(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	for n := 0; n < 15; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := Update(path, func(s *State) {
				s.TouchSession(Session{ID: fmt.Sprintf("s%d", n), Seen: now.Add(time.Duration(n) * time.Second)})
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Sessions) != 15 {
		t.Errorf("%d sessions kept after 15 concurrent updates", len(s.Sessions))
	}
}
