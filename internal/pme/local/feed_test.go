package local

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"p0ssess/internal/history"
	"p0ssess/internal/outcomes"
)

const feedModel = "google/gemini-3.8-flash"

func feedRun(t *testing.T, e *Engine, id, offer string, await bool) {
	t.Helper()
	if err := e.RecordRun(Run{ID: id, Route: "direct", Model: feedModel, CashUSD: 0.004, Status: "done", OfferID: offer,
		UseCase: "video-understand", Stakes: 2, AwaitOutcome: await}); err != nil {
		t.Fatal(err)
	}
}

func feedOffer(t *testing.T, e *Engine, id string) {
	t.Helper()
	err := history.Append(e.Dirs.HistoryFile(), history.Entry{TS: e.Now().UTC(), Type: history.TypeOffer, OfferID: id,
		UseCase: "video-understand", Stakes: 2, Options: []history.OfferOption{{Model: feedModel, Route: "direct"}}})
	if err != nil {
		t.Fatal(err)
	}
}

func outcomesOf(recs []outcomes.Record) []string {
	var out []string
	for _, r := range recs {
		out = append(out, r.Outcome)
	}
	return out
}

func wantOutcomes(t *testing.T, e *Engine, want ...string) {
	t.Helper()
	got := outcomesOf(e.PendingOutcomes())
	if len(got) != len(want) {
		t.Fatalf("pending outcomes %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("pending outcomes %v, want %v", got, want)
		}
	}
}

// A declined or overridden outcome that names its run replaces the run's implied acceptance: one
// record per run, never accepted plus declined.
func TestNamedOutcomeReplacesImpliedAccept(t *testing.T) {
	for _, outcome := range []string{history.Declined, history.Overridden} {
		t.Run(outcome, func(t *testing.T) {
			for _, withOffer := range []bool{false, true} {
				e, _ := setup(t, "")
				offer := ""
				if withOffer {
					offer = "o-feed1"
					feedOffer(t, e, offer)
				}
				feedRun(t, e, "d-1", offer, false)
				en, err := e.RecordOutcome(Outcome{OfferID: offer, RunID: "d-1", Model: feedModel, Outcome: outcome, Chosen: "claude"})
				if err != nil {
					t.Fatal(err)
				}
				if en.RunID != "d-1" || en.CashUSD != 0.004 {
					t.Errorf("offer %v: entry run %q cash %v", withOffer, en.RunID, en.CashUSD)
				}
				wantOutcomes(t, e, outcome)
			}
		})
	}
}

// Without a run id, a declined outcome for an offer finds the offer's run of that model.
func TestDeclinedOfferFindsItsRun(t *testing.T) {
	e, _ := setup(t, "")
	feedOffer(t, e, "o-feed2")
	feedRun(t, e, "d-2", "o-feed2", false)
	en, err := e.RecordOutcome(Outcome{OfferID: "o-feed2", Model: feedModel, Outcome: history.Declined})
	if err != nil || en.RunID != "d-2" {
		t.Fatalf("entry %+v, err %v", en, err)
	}
	wantOutcomes(t, e, history.Declined)
}

func TestUnknownRunIsRefused(t *testing.T) {
	e, _ := setup(t, "")
	if _, err := e.RecordOutcome(Outcome{RunID: "d-nope", Model: feedModel, Outcome: history.Declined}); err == nil {
		t.Error("a declined outcome naming an unknown run was recorded")
	}
}

// Ledgers written by 1.0.0 hold declined outcomes with an offer but no run id. They bind to the offer's
// run, so the run is not also counted as accepted.
func TestLegacyDeclinedIsNotDoubleCounted(t *testing.T) {
	e, _ := setup(t, "")
	feedOffer(t, e, "o-feed3")
	feedRun(t, e, "d-3", "o-feed3", false)
	err := history.Append(e.Dirs.HistoryFile(), history.Entry{TS: e.Now().UTC(), Type: history.TypeOutcome, OfferID: "o-feed3",
		Model: feedModel, Outcome: history.Declined, UseCase: "video-understand", Stakes: 2})
	if err != nil {
		t.Fatal(err)
	}
	wantOutcomes(t, e, history.Declined)
}

// A run held for its outcome stays out of the feed, and out of every send, until it is rated; then it
// goes out once, even though earlier sends moved past it.
func TestHeldRunWaitsForItsOutcome(t *testing.T) {
	e, _ := setup(t, "")
	calls, _ := outcomeServer(t, http.StatusAccepted)
	base := time.Now()
	e.Now = func() time.Time { return base }
	feedRun(t, e, "d-held", "", true)
	wantOutcomes(t, e)
	if n, _ := e.SendOutcomes(context.Background()); n != 0 || atomic.LoadInt32(calls) != 0 {
		t.Fatalf("a held run was sent: %d", n)
	}
	e.Now = func() time.Time { return base.Add(time.Minute) }
	feedRun(t, e, "d-free", "", false)
	if n, err := e.SendOutcomes(context.Background()); n != 1 || err != nil {
		t.Fatalf("the other run: n=%d err=%v", n, err)
	}
	e.Now = func() time.Time { return base.Add(time.Hour) }
	if _, err := e.RecordOutcome(Outcome{RunID: "d-held", Model: feedModel, Outcome: history.Declined}); err != nil {
		t.Fatal(err)
	}
	wantOutcomes(t, e, history.Declined)
	if n, err := e.SendOutcomes(context.Background()); n != 1 || err != nil {
		t.Fatalf("rated held run: n=%d err=%v", n, err)
	}
	// A second opinion on a run already sent is not sent again.
	e.Now = func() time.Time { return base.Add(2 * time.Hour) }
	if _, err := e.RecordOutcome(Outcome{RunID: "d-held", Model: feedModel, Outcome: history.Accepted}); err != nil {
		t.Fatal(err)
	}
	wantOutcomes(t, e)
}

func TestHeldRunAcceptedExplicitly(t *testing.T) {
	e, _ := setup(t, "")
	feedRun(t, e, "d-ok", "", true)
	if _, err := e.RecordOutcome(Outcome{RunID: "d-ok", Model: feedModel, Outcome: history.Accepted}); err != nil {
		t.Fatal(err)
	}
	wantOutcomes(t, e, history.Accepted)
}

// A held run nobody rates within HoldMax is dropped, never sent as accepted.
func TestHeldRunExpires(t *testing.T) {
	e, _ := setup(t, "")
	base := time.Now().AddDate(0, 0, -40)
	e.Now = func() time.Time { return base }
	feedRun(t, e, "d-old", "", true)
	e.Now = func() time.Time { return base.Add(outcomes.HoldMax + time.Hour) }
	wantOutcomes(t, e)
	if _, err := e.RecordOutcome(Outcome{RunID: "d-old", Model: feedModel, Outcome: history.Declined}); err != nil {
		t.Fatal(err)
	}
	wantOutcomes(t, e)
}

// Local tendencies do not count a held, unrated run as accepted either.
func TestHeldRunIsNotImpliedLocally(t *testing.T) {
	e, _ := setup(t, "")
	feedRun(t, e, "d-h", "", true)
	if e.Ledger().UsedModels()[feedModel] {
		t.Error("a held run counted as accepted")
	}
	feedRun(t, e, "d-u", "", false)
	if !e.Ledger().UsedModels()[feedModel] {
		t.Error("an unheld run should count as accepted")
	}
}
