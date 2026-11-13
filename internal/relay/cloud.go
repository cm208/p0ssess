package relay

import (
	"context"
	"time"

	"p0ssess/internal/jail"
)

// syncCloud pulls a cloud edit session's changes into its local worktree before the worktree is diffed.
func (e *Engine) syncCloud(rec *SessionRecord) error {
	e.mu.Lock()
	sj := e.jails[rec.SessionID]
	e.mu.Unlock()
	if sj == nil || !sj.j.Cloud {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return sj.j.Sync(ctx)
}

func cloudNote(j *jail.Jail) string {
	n := "Ran in the p0ssess cloud sandbox; paths under " + j.RemoteDir + " there are " + j.LocalRoot + " here."
	if j.UploadNote != "" {
		n += " Upload: " + j.UploadNote + "."
	}
	return n
}
