package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"p0ssess/internal/relay"
)

// TestKeepWatching covers M7: the command line keeps waiting on a still_running run, so the watch that
// enforces the cap and records the cost stays alive, unless --detach says otherwise.
func TestKeepWatching(t *testing.T) {
	statuses := []string{"still_running", "still_running", "done"}
	calls := 0
	wait := func(_ context.Context, sid string) (*relay.Result, error) {
		calls++
		if sid != "ses-1" {
			t.Errorf("waited on %q", sid)
		}
		return &relay.Result{SessionID: sid, Status: statuses[calls]}, nil
	}
	start := &relay.Result{SessionID: "ses-1", Status: statuses[0]}
	res, err := keepWatching(context.Background(), start, nil, false, wait)
	if err != nil || res.Status != "done" || calls != 2 || strings.Contains(res.Note, "detached") {
		t.Errorf("watching: %+v %v after %d waits", res, err, calls)
	}

	calls = 0
	res, err = keepWatching(context.Background(), &relay.Result{SessionID: "ses-1", Status: "still_running"}, nil, true, wait)
	if err != nil || calls != 0 || res.Status != "still_running" || !strings.Contains(res.Note, "counts as spent at the full cap") {
		t.Errorf("detached: %+v %v after %d waits", res, err, calls)
	}

	calls = 0
	res, _ = keepWatching(context.Background(), &relay.Result{SessionID: "ses-1", Status: "needs_permission"}, nil, false, wait)
	if calls != 0 || res.Status != "needs_permission" {
		t.Errorf("a run paused on a permission request was waited on: %+v", res)
	}

	boom := errors.New("jail gone")
	_, err = keepWatching(context.Background(), start, nil, false, func(context.Context, string) (*relay.Result, error) { return nil, boom })
	if !errors.Is(err, boom) {
		t.Errorf("wait error lost: %v", err)
	}
}
