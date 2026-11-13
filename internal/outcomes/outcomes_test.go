package outcomes

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"p0ssess/internal/history"
	"p0ssess/internal/pme/paths"
)

var now = time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC) // 2026-W46

func at(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t.Add(9 * time.Hour)
}

func TestKindOf(t *testing.T) {
	for in, want := range map[string]string{
		"image-generate":         "image.generate",
		"code-implement-feature": "code.implement-feature",
		"code":                   "code",
		"":                       "",
	} {
		if got := KindOf(in); got != want {
			t.Errorf("KindOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBucket(t *testing.T) {
	for _, c := range []struct {
		usd  float64
		want string
	}{
		{0, "0"}, {-1, "0"}, {0.0001, "<1c"}, {0.0099, "<1c"}, {0.01, "1-5c"}, {0.0499, "1-5c"},
		{0.05, "5-25c"}, {0.2499, "5-25c"}, {0.25, "25c-1"}, {1, "25c-1"}, {1.0001, ">1"}, {50, ">1"},
	} {
		if got := Bucket(c.usd); got != c.want {
			t.Errorf("Bucket(%v) = %q, want %q", c.usd, got, c.want)
		}
	}
}

func TestWeek(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"2026-12-31", "2026-W53"},
		{"2027-01-01", "2026-W53"},
		{"2027-01-03", "2026-W53"},
		{"2027-01-04", "2027-W01"},
		{"2026-11-12", "2026-W46"},
	} {
		if got := Week(at(c.in)); got != c.want {
			t.Errorf("Week(%s) = %q, want %q", c.in, got, c.want)
		}
	}
	// A zone ahead of UTC still reports the UTC week.
	loc := time.FixedZone("x", 14*3600)
	if got := Week(time.Date(2027, 1, 4, 5, 0, 0, 0, loc)); got != "2026-W53" {
		t.Errorf("Week in +14 zone = %q", got)
	}
}

func run(id, uc, model, route string, stakes int, ts string, cash float64) history.Entry {
	return history.Entry{TS: at(ts), Type: history.TypeRun, RunID: id, UseCase: uc, Model: model, Route: route,
		Stakes: stakes, CashUSD: cash, Status: "done", Session: "sess-secret", Project: "/home/u/secret-project"}
}

func TestBuild(t *testing.T) {
	const m = "google/gemini-3.4-flash-image"
	offer := history.Entry{TS: at("2026-10-20"), Type: history.TypeOffer, OfferID: "o-1", UseCase: "code-review", Stakes: 3,
		Options: []history.OfferOption{{Model: "deepseek/deepseek-v4", Route: "agent"}}}

	cases := []struct {
		name    string
		entries []history.Entry
		after   string
		want    []Record
	}{
		{"accepted by default", []history.Entry{run("r1", "image-generate", m, "direct", 2, "2026-11-04", 0.004)},
			"", []Record{{1, "image.generate", m, "direct", 2, "accepted", "<1c", "2026-W45"}}},
		{"redo outcome names the run", []history.Entry{
			run("r1", "image-generate", m, "direct", 2, "2026-11-04", 0.2),
			{TS: at("2026-11-05"), Type: history.TypeOutcome, RunID: "r1", Model: m, Outcome: history.Redo},
		}, "", []Record{{1, "image.generate", m, "direct", 2, "redo", "5-25c", "2026-W45"}}},
		{"explicit accepted keeps accepted", []history.Entry{
			run("r1", "image-generate", m, "direct", 2, "2026-11-04", 0),
			{TS: at("2026-11-05"), Type: history.TypeOutcome, RunID: "r1", Model: m, Outcome: history.Accepted},
		}, "", []Record{{1, "image.generate", m, "direct", 2, "accepted", "0", "2026-W45"}}},
		{"outcome naming a session covers its turns", []history.Entry{
			run("s9/1", "code-implement-feature", "x-ai/grok-5", "agent", 2, "2026-11-04", 0.3),
			run("s9/2", "code-implement-feature", "x-ai/grok-5", "agent", 2, "2026-11-04", 0.3),
			{TS: at("2026-11-05"), Type: history.TypeOutcome, RunID: "s9", Model: "x-ai/grok-5", Outcome: history.Redo},
		}, "", []Record{
			{1, "code.implement-feature", "x-ai/grok-5", "agent", 2, "redo", "25c-1", "2026-W45"},
			{1, "code.implement-feature", "x-ai/grok-5", "agent", 2, "redo", "25c-1", "2026-W45"},
		}},
		{"settled cost is used", []history.Entry{
			func() history.Entry {
				e := run("r1", "image-generate", m, "direct", 2, "2026-11-04", 2)
				e.Estimated = true
				return e
			}(),
			{TS: at("2026-11-05"), Type: history.TypeSettle, RunID: "r1", CashUSD: 0.03},
		}, "", []Record{{1, "image.generate", m, "direct", 2, "accepted", "1-5c", "2026-W45"}}},
		{"declined offer with no run", []history.Entry{offer,
			{TS: at("2026-10-21"), Type: history.TypeOutcome, OfferID: "o-1", Model: "deepseek/deepseek-v4", Outcome: history.Declined},
		}, "", []Record{{1, "code.review", "deepseek/deepseek-v4", "agent", 3, "declined", "0", "2026-W43"}}},
		{"overridden without route is skipped", []history.Entry{
			{TS: at("2026-10-21"), Type: history.TypeOutcome, UseCase: "code-review", Stakes: 2, Model: m, Outcome: history.Overridden},
		}, "", nil},
		{"outcome with a run is not doubled", []history.Entry{
			run("r1", "image-generate", m, "direct", 2, "2026-11-04", 0),
			{TS: at("2026-11-05"), Type: history.TypeOutcome, RunID: "r1", Model: m, Outcome: history.Overridden},
		}, "", []Record{{1, "image.generate", m, "direct", 2, "overridden", "0", "2026-W45"}}},
		{"unknown fields skipped", []history.Entry{
			run("a", "", m, "direct", 2, "2026-11-04", 0),
			run("b", "image-generate", m, "direct", 0, "2026-11-04", 0),
			run("c", "image-generate", m, "direct", 5, "2026-11-04", 0),
			run("d", "image-generate", "gemini", "direct", 2, "2026-11-04", 0),
			run("e", "image-generate", "Google/Gemini", "direct", 2, "2026-11-04", 0),
			run("f", "image-generate", m, "reported", 2, "2026-11-04", 0),
			run("g", "image-generate", m, "", 2, "2026-11-04", 0),
			run("h", "image-generate", "", "direct", 2, "2026-11-04", 0),
			func() history.Entry {
				e := run("i", "image-generate", m, "direct", 2, "2026-11-04", 0)
				e.Status = "failed"
				return e
			}(),
		}, "", nil},
		{"offer fills missing run fields", []history.Entry{offer,
			func() history.Entry {
				e := run("r1", "", "deepseek/deepseek-v4", "", 0, "2026-11-04", 0.5)
				e.OfferID = "o-1"
				return e
			}(),
		}, "", []Record{{1, "code.review", "deepseek/deepseek-v4", "agent", 3, "accepted", "25c-1", "2026-W45"}}},
		{"current week included", []history.Entry{run("r1", "image-generate", m, "direct", 2, "2026-11-10", 0)},
			"", []Record{{1, "image.generate", m, "direct", 2, "accepted", "0", "2026-W46"}}},
		{"calibration results are never recorded", []history.Entry{
			{TS: at("2026-11-10"), Type: history.TypeOutcome, UseCase: "image-generate", Stakes: 2, Model: m,
				Outcome: history.Declined, Route: history.RouteCalibrate, Reason: "calibration"},
			{TS: at("2026-11-10"), Type: history.TypeOutcome, UseCase: "image-generate", Stakes: 2, Model: m,
				Outcome: history.Accepted, Route: history.RouteCalibrate},
			run("c1", "image-generate", m, history.RouteCalibrate, 2, "2026-11-10", 0.01),
			{TS: at("2026-11-10"), Type: history.TypeOffer, OfferID: "o-9", UseCase: "image-generate", Stakes: 2,
				Options: []history.OfferOption{{Model: m, Route: history.RouteCalibrate}}},
			{TS: at("2026-11-10"), Type: history.TypeOutcome, OfferID: "o-9", Model: m, Outcome: history.Declined},
		}, "", nil},
		{"older than 8 weeks excluded", []history.Entry{
			run("r1", "image-generate", m, "direct", 2, "2026-09-07", 0), // W37, nine weeks back
			run("r2", "image-generate", m, "direct", 2, "2026-09-14", 0), // W38, eight weeks back
		}, "", []Record{{1, "image.generate", m, "direct", 2, "accepted", "0", "2026-W38"}}},
		{"entries at or before the sent mark are skipped", []history.Entry{
			run("r1", "image-generate", m, "direct", 2, "2026-10-28", 0), // W44
			run("r2", "image-generate", m, "direct", 2, "2026-11-04", 0), // W45
		}, "2026-10-28", []Record{{1, "image.generate", m, "direct", 2, "accepted", "0", "2026-W45"}}},
		{"ordered by week", []history.Entry{
			run("r2", "image-generate", m, "direct", 2, "2026-11-04", 0),
			run("r1", "image-generate", m, "direct", 2, "2026-10-28", 0),
		}, "", []Record{
			{1, "image.generate", m, "direct", 2, "accepted", "0", "2026-W44"},
			{1, "image.generate", m, "direct", 2, "accepted", "0", "2026-W45"},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var after time.Time
			if c.after != "" {
				after = at(c.after)
			}
			got := Build(c.entries, now, after)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got  %+v\nwant %+v", got, c.want)
			}
		})
	}
}

func TestBuildCap(t *testing.T) {
	const m = "google/gemini-3.4-flash-image"
	var es []history.Entry
	for i := 0; i < 1500; i++ {
		es = append(es, run("a", "image-generate", m, "direct", 2, "2026-10-28", 0))
	}
	for i := 0; i < 1500; i++ {
		es = append(es, run("b", "image-generate", m, "direct", 2, "2026-11-04", 0))
	}
	got := Build(es, now, time.Time{})
	if len(got) != MaxRecords {
		t.Fatalf("len = %d", len(got))
	}
	if got[len(got)-1].Week != "2026-W45" || !sort.SliceIsSorted(got, func(i, j int) bool { return got[i].Week < got[j].Week }) {
		t.Errorf("newest weeks not kept in order")
	}
	if n := countWeek(got, "2026-W45"); n != 1500 {
		t.Errorf("newest week has %d records", n)
	}
}

func countWeek(rs []Record, w string) (n int) {
	for _, r := range rs {
		if r.Week == w {
			n++
		}
	}
	return
}

func TestLines(t *testing.T) {
	recs := []Record{{1, "image.generate", "google/gemini-3.4-flash-image", "direct", 2, "accepted", "<1c", "2026-W45"}}
	lines := Lines(recs)
	if len(lines) != 1 || strings.Contains(lines[0], "\n") {
		t.Fatalf("lines = %q", lines)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &m); err != nil {
		t.Fatal(err)
	}
	want := []string{"cost", "kind", "model", "outcome", "route", "stakes", "v", "week"}
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("keys = %v", keys)
	}
	const sample = `{"v":1,"kind":"image.generate","model":"google/gemini-3.4-flash-image","route":"direct","stakes":2,"outcome":"accepted","cost":"<1c","week":"2026-W45"}`
	if lines[0] != sample {
		t.Errorf("line = %s", lines[0])
	}
}

func home(t *testing.T) paths.Dirs {
	t.Helper()
	t.Setenv("P0SSESS_HOME", t.TempDir())
	return paths.Default()
}

func seed(t *testing.T, d paths.Dirs, es ...history.Entry) {
	t.Helper()
	for _, e := range es {
		if err := history.Append(d.HistoryFile(), e); err != nil {
			t.Fatal(err)
		}
	}
}

type fake struct {
	status int
	err    error
	calls  int
	body   []byte
}

func (f *fake) post(_ context.Context, body []byte) (int, error) {
	f.calls++
	f.body = body
	return f.status, f.err
}

func readState4(t *testing.T, d paths.Dirs) state {
	t.Helper()
	b, err := os.ReadFile(stateFile(d))
	if err != nil {
		t.Fatal(err)
	}
	var st state
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestSend(t *testing.T) {
	const m = "google/gemini-3.4-flash-image"
	ctx := context.Background()
	runs := func(d paths.Dirs) {
		seed(t, d, run("run-secret-1", "image-generate", m, "direct", 2, "2026-11-04", 0.004),
			run("run-secret-2", "image-generate", m, "direct", 2, "2026-11-11", 0.4))
	}

	t.Run("disabled touches nothing", func(t *testing.T) {
		d := home(t)
		runs(d)
		f := &fake{status: 202}
		n, err := Send(ctx, d, false, f.post, now)
		if n != 0 || err != nil || f.calls != 0 {
			t.Fatalf("n=%d err=%v calls=%d", n, err, f.calls)
		}
		if _, err := os.Stat(stateFile(d)); !os.IsNotExist(err) {
			t.Errorf("state file exists: %v", err)
		}
	})

	t.Run("202 sends this week's records too, then only what is new", func(t *testing.T) {
		d := home(t)
		runs(d)
		f := &fake{status: 202}
		n, err := Send(ctx, d, true, f.post, now)
		if n != 2 || err != nil || f.calls != 1 {
			t.Fatalf("n=%d err=%v calls=%d", n, err, f.calls)
		}
		fi, _ := os.Stat(stateFile(d))
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v", fi.Mode().Perm())
		}
		var body struct {
			V       int      `json:"v"`
			Records []Record `json:"records"`
		}
		if err := json.Unmarshal(f.body, &body); err != nil || body.V != 1 || len(body.Records) != 2 {
			t.Errorf("body = %s (%v)", f.body, err)
		}
		// The next session end has nothing to say: no request at all.
		if n, err := Send(ctx, d, true, f.post, now); n != 0 || err != nil || f.calls != 1 {
			t.Fatalf("nothing new: n=%d err=%v calls=%d", n, err, f.calls)
		}
		// A run finished since goes out alone.
		seed(t, d, history.Entry{TS: now, Type: history.TypeRun, RunID: "r3", UseCase: "image-generate", Model: m,
			Route: "direct", Stakes: 2, Status: "done"})
		if n, err := Send(ctx, d, true, f.post, now); n != 1 || err != nil || f.calls != 2 {
			t.Fatalf("new run: n=%d err=%v calls=%d", n, err, f.calls)
		}
		if p, _ := Pending(d, now); len(p) != 0 {
			t.Errorf("pending = %v", p)
		}
	})

	for _, status := range []int{400, 413} {
		t.Run("refused batch is dropped", func(t *testing.T) {
			d := home(t)
			runs(d)
			f := &fake{status: status}
			n, err := Send(ctx, d, true, f.post, now)
			if n != 0 || err != nil {
				t.Fatalf("n=%d err=%v", n, err)
			}
			if p, _ := Pending(d, now); len(p) != 0 {
				t.Errorf("pending = %v", p)
			}
		})
	}

	for _, c := range []struct {
		name   string
		status int
		err    error
	}{{"429", 429, nil}, {"500", 503, nil}, {"network", 0, errors.New("dial failed")}} {
		t.Run("a missed send goes out with the next one: "+c.name, func(t *testing.T) {
			d := home(t)
			runs(d)
			f := &fake{status: c.status, err: c.err}
			n, err := Send(ctx, d, true, f.post, now)
			if n != 0 || err == nil {
				t.Fatalf("n=%d err=%v", n, err)
			}
			if _, err := os.Stat(stateFile(d)); !os.IsNotExist(err) {
				t.Errorf("state written: %v", err)
			}
			if p, _ := Pending(d, now); len(p) != 2 {
				t.Errorf("pending = %v", p)
			}
			// The next session ends with another run; both batches' records go together.
			seed(t, d, history.Entry{TS: now, Type: history.TypeRun, RunID: "r3", UseCase: "image-generate", Model: m,
				Route: "direct", Stakes: 2, Status: "done"})
			f.status, f.err = 202, nil
			if n, err := Send(ctx, d, true, f.post, now); n != 3 || err != nil {
				t.Errorf("retry: n=%d err=%v", n, err)
			}
		})
	}

	t.Run("nothing pending sends nothing and writes nothing", func(t *testing.T) {
		d := home(t)
		f := &fake{status: 202}
		n, err := Send(ctx, d, true, f.post, now)
		if n != 0 || err != nil || f.calls != 0 {
			t.Fatalf("n=%d err=%v calls=%d", n, err, f.calls)
		}
		if _, err := os.Stat(stateFile(d)); !os.IsNotExist(err) {
			t.Errorf("state written: %v", err)
		}
	})

	t.Run("calibration results are never sent", func(t *testing.T) {
		d := home(t)
		seed(t, d,
			history.Entry{TS: at("2026-11-10"), Type: history.TypeOutcome, UseCase: "image-generate", Stakes: 2, Model: m,
				Outcome: history.Declined, Route: history.RouteCalibrate, Reason: "calibration"},
			history.Entry{TS: at("2026-11-10"), Type: history.TypeOutcome, UseCase: "image-generate", Stakes: 2, Model: m,
				Outcome: history.Accepted, Route: history.RouteCalibrate, Reason: "calibration"})
		f := &fake{status: 202}
		if n, err := Send(ctx, d, true, f.post, now); n != 0 || err != nil || f.calls != 0 {
			t.Fatalf("n=%d err=%v calls=%d", n, err, f.calls)
		}
	})

	t.Run("two senders at once send each record once", func(t *testing.T) {
		d := home(t)
		runs(d)
		var mu sync.Mutex
		total := 0
		post := func(_ context.Context, body []byte) (int, error) {
			var b struct{ Records []Record }
			json.Unmarshal(body, &b)
			mu.Lock()
			total += len(b.Records)
			mu.Unlock()
			return 202, nil
		}
		var wg sync.WaitGroup
		for i := 0; i < 6; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); Send(ctx, d, true, post, now) }()
		}
		wg.Wait()
		if total != 2 {
			t.Errorf("%d records sent, want 2", total)
		}
	})
}

func TestBodyHasNoLedgerIdentifiers(t *testing.T) {
	const m = "google/gemini-3.4-flash-image"
	d := home(t)
	e := run("run-secret-1", "image-generate", m, "direct", 2, "2026-11-04", 0.004)
	e.OfferID = "o-secret"
	e.Reason = "private reason text"
	seed(t, d, e, history.Entry{TS: at("2026-11-05"), Type: history.TypeOutcome, RunID: "run-secret-1", Model: m,
		Outcome: history.Redo, Session: "sess-secret", Project: "/home/u/secret-project", Reason: "private reason text"})
	f := &fake{status: 202}
	if n, err := Send(context.Background(), d, true, f.post, now); n != 1 || err != nil {
		t.Fatalf("n=%d err=%v", n, err)
	}
	for _, leak := range []string{"sess-secret", "secret-project", "run-secret", "o-secret", "private reason", "0.004"} {
		if strings.Contains(string(f.body), leak) {
			t.Errorf("body leaks %q: %s", leak, f.body)
		}
	}
}
