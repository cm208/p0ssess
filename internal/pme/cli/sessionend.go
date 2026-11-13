package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"time"

	"p0ssess/internal/pme/local"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/proc"
	"p0ssess/internal/team"
)

// sendTimeout bounds the upload that follows a session's end. The records stay pending when it runs out.
const sendTimeout = 4 * time.Second

// startSender starts the detached upload; tests replace it.
var startSender = func() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(exe, "hook", "session-end-send")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	_ = proc.StartDetached(cmd)
	if cmd.Process != nil {
		_ = cmd.Process.Release()
	}
}

// sessionEnd is the SessionEnd hook. Claude Code is exiting, so it only starts a detached copy of this
// program to do the upload and returns at once; it never waits on the network. A repository with a
// .p0ssess-private file sends nothing; its records go out at a later session end.
func sessionEnd(in io.Reader, d paths.Dirs) int {
	var input struct {
		Cwd string `json:"cwd"`
	}
	raw, _ := io.ReadAll(io.LimitReader(in, 1<<20))
	_ = json.Unmarshal(raw, &input)
	if paths.Private(input.Cwd) {
		return 0
	}
	startSender()
	return 0
}

// sendOutcomesNow is the detached half: it sends the pending outcome records and the Team spend
// queue, giving up after sendTimeout. It is silent, since nobody is looking at its output.
func sendOutcomesNow(d paths.Dirs) int {
	// A stuck lock or a slow disk must not leave this process around.
	time.AfterFunc(sendTimeout+2*time.Second, func() { os.Exit(0) })
	e, err := loadEngine(d)
	if err != nil {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
	defer cancel()
	sendAll(ctx, e)
	return 0
}

// sendAll sends the outcome records and the Team spend queue side by side.
func sendAll(ctx context.Context, e *local.Engine) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = team.Flush(ctx, team.NewClient(e.Dirs, Version))
	}()
	_, _ = e.SendOutcomes(ctx)
	<-done
}
