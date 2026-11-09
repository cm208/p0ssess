package relay

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"p0ssess/internal/jail"
	"p0ssess/internal/opencode"
)

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	if err := cmd.Run(); err != nil {
		return so.String(), fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(se.String()))
	}
	return so.String(), nil
}

// addWorktree creates a detached worktree of HEAD under <repo>/.p0ssess/wt/.
func addWorktree(repo string) (string, string, error) {
	base, err := git(repo, "rev-parse", "HEAD")
	if err != nil {
		return "", "", fmt.Errorf("edit mode needs at least one commit: %w", err)
	}
	b := make([]byte, 3)
	rand.Read(b)
	name := "w" + time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b)
	wt := filepath.Join(repo, jail.WorktreeDir, name)
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		return "", "", err
	}
	if _, err := git(repo, "worktree", "add", "--detach", wt, "HEAD"); err != nil {
		return "", "", err
	}
	return wt, strings.TrimSpace(base), nil
}

func removeWorktree(repo, wt string) {
	git(repo, "worktree", "remove", "--force", wt)
}

// worktreeDiff returns the stat and full patch of everything the task changed, untracked files included.
// Nothing is committed or merged; the worktree's own index is updated so new files show up.
func worktreeDiff(wt, base string) (string, string, error) {
	if _, err := git(wt, "add", "-A"); err != nil {
		return "", "", err
	}
	stat, err := git(wt, "diff", "--cached", "--stat", base)
	if err != nil {
		return "", "", err
	}
	patch, err := git(wt, "diff", "--cached", "--no-color", "--no-ext-diff", base)
	return strings.TrimSpace(stat), patch, err
}

var secretName = regexp.MustCompile(`(?i)(^\.env($|\.)|^\.envrc$|^\.netrc$|^id_(rsa|dsa|ecdsa|ed25519)|\.(pem|key|p12|pfx|kdbx|jks|keystore)$|^credentials|(^|[._-])secrets?([._-]|$))`)

const maxAttachment = 20 << 20

// attachments turns file paths into data: URIs, so the jail never needs the files mounted.
func attachments(paths []string) ([]opencode.FileAttachment, error) {
	var out []opencode.FileAttachment
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, err
		}
		base := filepath.Base(abs)
		if secretName.MatchString(base) || strings.Contains(abs, "/.ssh/") {
			return nil, fmt.Errorf("refusing to attach %s: it looks like a secret", base)
		}
		st, err := os.Stat(abs)
		if err != nil {
			return nil, err
		}
		if !st.Mode().IsRegular() {
			return nil, fmt.Errorf("%s is not a regular file", abs)
		}
		if st.Size() > maxAttachment {
			return nil, fmt.Errorf("%s is larger than 20 MiB", base)
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			return nil, err
		}
		mt := mediaType(base, data)
		switch {
		case strings.HasPrefix(mt, "audio/"), strings.HasPrefix(mt, "video/"):
			return nil, fmt.Errorf("%s is %s; OpenCode 2.2.5 drops audio and video attachments", base, mt)
		case mt == "text/plain", mt == "application/pdf",
			mt == "image/png", mt == "image/jpeg", mt == "image/gif", mt == "image/webp":
		default:
			return nil, fmt.Errorf("%s has type %s; only text, PDF and PNG/JPEG/GIF/WebP images reach the model", base, mt)
		}
		out = append(out, opencode.FileAttachment{
			URI:  "data:" + mt + ";base64," + base64.StdEncoding.EncodeToString(data),
			Name: base,
		})
	}
	return out, nil
}

func mediaType(name string, data []byte) string {
	sniff := http.DetectContentType(data)
	if t, _, _ := strings.Cut(sniff, ";"); strings.HasPrefix(t, "image/") || t == "application/pdf" ||
		strings.HasPrefix(t, "audio/") || strings.HasPrefix(t, "video/") {
		return t
	}
	if strings.HasPrefix(sniff, "text/") {
		return "text/plain"
	}
	if t := mime.TypeByExtension(filepath.Ext(name)); t != "" {
		t, _, _ = strings.Cut(t, ";")
		if strings.HasPrefix(t, "text/") {
			return "text/plain"
		}
		return t
	}
	return sniff
}
