// Package cloudsandbox is the laptop end of the cloud sandbox: it uploads a repository's tracked files,
// keeps the session's one TLS connection, answers the sandbox's model calls with the user's key under the
// run's cost cap, and brings the sandbox's edits back as a diff.
package cloudsandbox

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"regexp"
	"sort"
	"strings"
)

const (
	MaxUploadBytes = 200 << 20
	MaxUploadFiles = 100000
	PrivateMarker  = ".p0ssess-private"
)

// ErrPrivate is returned for a repository that holds a .p0ssess-private file anywhere in its tracked tree.
var ErrPrivate = errors.New("this repository has a .p0ssess-private file; it is never uploaded")

// TooLargeError is returned when the tracked files pass the upload limits.
type TooLargeError struct {
	Files int
	Bytes int64
}

func (e *TooLargeError) Error() string {
	return fmt.Sprintf("the repository's tracked files are too large for the cloud sandbox (%d files, %d MiB; the limit is %d files and %d MiB)",
		e.Files, e.Bytes>>20, MaxUploadFiles, MaxUploadBytes>>20)
}

var secretBase = regexp.MustCompile(`(?i)^(\.env(\..*)?|\.envrc|\.netrc|\.pgpass|\.git-credentials|\.npmrc|\.pypirc|\.?htpasswd|id_(rsa|dsa|ecdsa|ed25519).*|.*\.(pem|key|p12|pfx|kdbx|jks|keystore)|credentials.*|(.*[._-])?secrets?([._-].*)?)$`)

var secretDirs = map[string]bool{".ssh": true, ".gnupg": true, ".aws": true, ".kube": true, ".docker": true}

// SecretPath reports whether a slash-separated relative path looks like a secret, by the list in the
// sandbox contract. The host applies the same list and rejects an upload that holds one.
func SecretPath(p string) bool {
	parts := strings.Split(p, "/")
	for _, c := range parts[:len(parts)-1] {
		if secretDirs[strings.ToLower(c)] {
			return true
		}
	}
	return secretBase.MatchString(parts[len(parts)-1])
}

// Manifest is what an upload holds and what it left out.
type Manifest struct {
	Files   int
	Bytes   int64
	Secrets []string // tracked files skipped because they look like secrets
	Skipped []string // tracked entries the sandbox cannot take: submodules, escaping symlinks, odd names
}

// Note is one line for the run's result about what was left out, or "".
func (m Manifest) Note() string {
	var parts []string
	if n := len(m.Secrets); n > 0 {
		parts = append(parts, fmt.Sprintf("not uploaded, they look like secrets: %s", short(m.Secrets)))
	}
	if n := len(m.Skipped); n > 0 {
		parts = append(parts, fmt.Sprintf("not uploaded: %s", short(m.Skipped)))
	}
	return strings.Join(parts, "; ")
}

func short(list []string) string {
	if len(list) <= 5 {
		return strings.Join(list, ", ")
	}
	return strings.Join(list[:5], ", ") + fmt.Sprintf(" and %d more", len(list)-5)
}

type tracked struct {
	path string
	mode string // git's index mode: 100644, 100755, 120000, 160000
}

// listTracked returns the files git tracks under dir, which must be the top of a work tree.
func listTracked(dir string) ([]tracked, error) {
	cmd := exec.Command("git", "-C", dir, "ls-files", "-z", "--stage", "--full-name")
	cmd.Env = gitEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	seen := map[string]bool{}
	var list []tracked
	for _, rec := range bytes.Split(out, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		// "<mode> <object> <stage>\t<path>"
		meta, p, ok := bytes.Cut(rec, []byte{'\t'})
		if !ok {
			continue
		}
		f := strings.Fields(string(meta))
		if len(f) != 3 || seen[string(p)] {
			continue // a conflicted path has several stages; take it once
		}
		seen[string(p)] = true
		list = append(list, tracked{path: string(p), mode: f[0]})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].path < list[j].path })
	return list, nil
}

// gitEnv is the environment without anything that would point git at another repository.
func gitEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "GIT_") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_OPTIONAL_LOCKS=0")
}

// badName reports a path the sandbox host refuses (sandbox.md, "Upload format").
func badName(p string) bool {
	if p == "" || len(p) > 4096 || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return true
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	for _, c := range strings.Split(p, "/") {
		if c == "" || c == "." || c == ".." || len(c) > 255 || strings.EqualFold(c, ".git") {
			return true
		}
	}
	return false
}

// symlinkEscapes reports whether a link at p with this target is absolute or climbs out of the tree.
func symlinkEscapes(p, target string) bool {
	if target == "" || strings.HasPrefix(target, "/") || strings.Contains(target, "\\") {
		return true
	}
	named := false
	for _, c := range strings.Split(target, "/") {
		switch {
		case strings.EqualFold(c, ".git"):
			return true
		case c == "..":
			if named {
				return true // "a/../b" can climb out through a symlink chain
			}
		case c != "" && c != ".":
			named = true
		}
	}
	joined := path.Join(path.Dir(p), target)
	return joined == ".." || strings.HasPrefix(joined, "../")
}

// WriteUpload writes a gzip-compressed tar of the files git tracks in dir (the top of a work tree) to w,
// with their contents as they are on disk. Files that look like secrets are left out and listed; a
// repository holding .p0ssess-private anywhere is refused before anything is written. Every file is read
// through an os.Root, so a symlinked directory in the checkout cannot pull in files from outside it.
func WriteUpload(dir string, w io.Writer) (Manifest, error) {
	var m Manifest
	list, err := listTracked(dir)
	if err != nil {
		return m, err
	}
	for _, t := range list {
		if strings.EqualFold(path.Base(t.path), PrivateMarker) {
			return m, ErrPrivate
		}
	}
	if _, err := os.Lstat(dir + string(os.PathSeparator) + PrivateMarker); err == nil {
		return m, ErrPrivate
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return m, err
	}
	defer root.Close()

	// Size the upload first, so an oversized repository fails before any bytes leave.
	type entry struct {
		tracked
		size int64
		link string
	}
	var entries []entry
	var total int64
	for _, t := range list {
		switch {
		case t.mode == "160000":
			m.Skipped = append(m.Skipped, t.path+" (submodule)")
			continue
		case badName(t.path):
			m.Skipped = append(m.Skipped, fmt.Sprintf("%q (name)", t.path))
			continue
		case SecretPath(t.path):
			m.Secrets = append(m.Secrets, t.path)
			continue
		}
		fi, err := root.Lstat(t.path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue // deleted in the checkout
			}
			m.Skipped = append(m.Skipped, t.path+" (unreadable)")
			continue
		}
		e := entry{tracked: t}
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			target, err := root.Readlink(t.path)
			if err != nil || symlinkEscapes(t.path, target) {
				m.Skipped = append(m.Skipped, t.path+" (symlink out of the repository)")
				continue
			}
			e.link = target
		case fi.Mode().IsRegular():
			e.size = fi.Size()
		default:
			m.Skipped = append(m.Skipped, t.path+" (not a file)")
			continue
		}
		total += e.size
		entries = append(entries, e)
	}
	if total > MaxUploadBytes || len(entries) > MaxUploadFiles {
		return m, &TooLargeError{Files: len(entries), Bytes: total}
	}

	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	var written int64
	for _, e := range entries {
		if e.link != "" {
			if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeSymlink, Name: e.path, Linkname: e.link, Mode: 0o777, Format: tar.FormatPAX}); err != nil {
				return m, err
			}
			m.Files++
			continue
		}
		f, err := root.Open(e.path)
		if err != nil {
			m.Skipped = append(m.Skipped, e.path+" (unreadable)")
			continue
		}
		fi, err := f.Stat()
		if err != nil || !fi.Mode().IsRegular() {
			f.Close()
			m.Skipped = append(m.Skipped, e.path+" (changed while uploading)")
			continue
		}
		size := fi.Size()
		if written+size > MaxUploadBytes {
			f.Close()
			return m, &TooLargeError{Files: len(entries), Bytes: written + size}
		}
		mode := int64(0o644)
		if fi.Mode()&0o111 != 0 || e.mode == "100755" {
			mode = 0o755
		}
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: e.path, Size: size, Mode: mode, Format: tar.FormatPAX}); err != nil {
			f.Close()
			return m, err
		}
		n, err := io.Copy(tw, io.LimitReader(f, size))
		f.Close()
		if err != nil {
			return m, err
		}
		if n != size {
			return m, fmt.Errorf("%s changed size while uploading", e.path)
		}
		written += n
		m.Files++
	}
	m.Bytes = written
	if err := tw.Close(); err != nil {
		return m, err
	}
	return m, gz.Close()
}
