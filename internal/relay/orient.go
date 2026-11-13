package relay

import (
	"path/filepath"
	"strings"

	"p0ssess/internal/jail"
)

// orient is the task as the model receives it. Claude writes paths as it sees them, in the repository,
// but an edit run works in a worktree (and a cloud run under another root), where the repository is
// read-only or absent. Cheap models then spent their runs probing mounts and write access and stopped
// for permission. So repository paths are rewritten to the copy the model works in, and a short note
// says where it is, what it may do without asking, and how to report.
func orient(rec *SessionRecord, j *jail.Jail, task string) string {
	from := rec.Repo
	to := rec.Repo
	if rec.Worktree != "" {
		to = rec.Worktree
	}
	if j != nil {
		to = j.RemotePath(to)
	}
	rewritten := false
	if from != "" && to != from {
		task, rewritten = rewritePaths(task, from, to)
	}
	seen := rec.Location
	if j != nil {
		seen = j.RemotePath(seen)
	}
	var b strings.Builder
	b.WriteString(task)
	b.WriteString("\n\n---\n(p0ssess) ")
	if rec.Mode == "edit" {
		b.WriteString("You work in a private copy of the repository; your current directory is " + seen + ". ")
		if rewritten {
			b.WriteString("Paths in the task already point into this copy. ")
		}
		b.WriteString("Read and write files here, with relative paths. Your changes are collected from this copy " +
			"automatically when you finish: do not commit, push, or check whether you can write. Run the project's own " +
			"tests and scripts as needed. Finish with a short report: each file you changed on one line, then the test " +
			"command you ran and its pass and fail counts. Do not paste file contents or the diff into the report.")
	} else {
		b.WriteString("This run is read-only: give the result in your reply. Do not try to write files, and do not " +
			"probe mounts or permissions.")
	}
	return b.String()
}

// rewritePaths replaces the repository root from with to wherever it starts a path, except paths already
// inside p0ssess's own directory (a worktree named on purpose).
func rewritePaths(s, from, to string) (string, bool) {
	from = filepath.Clean(from)
	var b strings.Builder
	changed := false
	for {
		i := strings.Index(s, from)
		if i < 0 {
			b.WriteString(s)
			break
		}
		end := i + len(from)
		rest := s[end:]
		boundary := rest == "" || !isPathChar(rest[0]) || rest[0] == '/' ||
			rest[0] == '.' && (len(rest) == 1 || rest[1] == ' ' || rest[1] == '\n')
		startOK := i == 0 || !isPathChar(s[i-1])
		inOwn := strings.HasPrefix(rest, "/.p0ssess/") || rest == "/.p0ssess"
		if boundary && startOK && !inOwn {
			b.WriteString(s[:i])
			b.WriteString(to)
			changed = true
		} else {
			b.WriteString(s[:end])
		}
		s = rest
	}
	return b.String(), changed
}

func isPathChar(c byte) bool {
	return c == '/' || c == '.' || c == '-' || c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}
