package local

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"p0ssess/internal/pme/paths"
)

// SetOffline records whether the last call to the engine service failed; the status line dims the
// ghost while it did.
func (e *Engine) SetOffline(failed bool) {
	p := e.Dirs.OfflineMarker()
	if !failed {
		os.Remove(p)
		return
	}
	if _, err := os.Stat(p); err == nil {
		return
	}
	paths.WriteFileAtomic(p, nil, 0o600)
}

// RefreshTable asks the engine service for a newer route table now and says what happened in one line.
func (e *Engine) RefreshTable(ctx context.Context) (string, error) {
	if e.Remote == nil || e.Table == nil {
		return "", errors.New("no engine client")
	}
	e.Table.Checked(e.Now())
	env, etag, same, err := e.Remote.FetchTable(ctx, e.Table.ETag())
	if err != nil {
		e.SetOffline(true)
		return "", err
	}
	e.SetOffline(false)
	if same {
		return fmt.Sprintf("route table %s is current", e.Table.Table().ID), nil
	}
	if err := e.Table.Accept(env, etag, e.Remote.Linked()); err != nil {
		return "", err
	}
	t := e.Table.Table()
	return fmt.Sprintf("fetched route table %s: %d use cases, %d models", t.ID, len(t.UseCases), len(t.Models)), nil
}

// RefreshInBackground fetches a newer route table when the last check is a day old. It never blocks
// the caller, and at most one fetch runs at a time.
func (e *Engine) RefreshInBackground() {
	if e.Table == nil || !e.Table.Due(e.Now()) || !e.refreshing.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer e.refreshing.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		e.RefreshTable(ctx)
	}()
}
