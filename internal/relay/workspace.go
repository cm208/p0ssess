package relay

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"p0ssess/internal/pme/paths"
)

const (
	scratchMaxFiles = 20
	scratchMaxBytes = 20 << 20
	carryMaxFiles   = 2000
	carryMaxBytes   = 50 << 20
)

var commitIdentity = []string{"-c", "user.name=p0ssess", "-c", "user.email=p0ssess@localhost", "-c", "commit.gpgsign=false"}

// scratchRepo makes a throwaway git repository for work that names no repository, with copies of the
// files the task names, so the jail never mounts the caller's own directories.
func scratchRepo(files []string) (string, map[string]string, error) {
	dir, err := os.MkdirTemp("", "p0ssess-scratch-")
	if err != nil {
		return "", nil, err
	}
	copies := map[string]string{}
	var total int64
	used := map[string]bool{}
	for _, f := range files {
		st, err := os.Stat(f)
		if err != nil || !st.Mode().IsRegular() || len(copies) >= scratchMaxFiles || total+st.Size() > scratchMaxBytes {
			continue
		}
		if secretName.MatchString(filepath.Base(f)) {
			continue
		}
		name := filepath.Base(f)
		for i := 2; used[name]; i++ {
			name = fmt.Sprintf("%d-%s", i, filepath.Base(f))
		}
		used[name] = true
		dst := filepath.Join(dir, name)
		if err := copyRegular(f, dst); err != nil {
			continue
		}
		total += st.Size()
		copies[f] = dst
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"},
		append(append([]string{}, commitIdentity...), "commit", "-q", "--allow-empty", "--no-verify", "-m", "p0ssess scratch")} {
		if _, err := git(dir, args...); err != nil {
			os.RemoveAll(dir)
			return "", nil, err
		}
	}
	return dir, copies, nil
}

func copyRegular(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// dirtyCount is how many paths of repo's working tree differ from HEAD, untracked files included.
func dirtyCount(repo string) (int, error) {
	out, err := git(repo, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range strings.Split(out, "\x00") {
		if len(e) > 3 && !strings.HasPrefix(e[3:], ".p0ssess/") {
			n++
		}
	}
	return n, nil
}

// carryDirty copies the caller's uncommitted changes (staged, unstaged and untracked files) into a
// fresh worktree and commits them there, so the model starts from what the caller sees and the run's
// diff holds only its own changes. It returns the new base and how many paths were carried.
func carryDirty(repo, wt string) (string, int, error) {
	n, err := dirtyCount(repo)
	if err != nil || n == 0 {
		return "", 0, err
	}
	patch, err := git(repo, "diff", "HEAD", "--binary", "--no-color", "--no-ext-diff")
	if err != nil {
		return "", 0, err
	}
	if patch != "" {
		f, err := os.CreateTemp("", "p0ssess-carry-*.patch")
		if err != nil {
			return "", 0, err
		}
		f.WriteString(patch)
		f.Close()
		defer os.Remove(f.Name())
		if _, err := git(wt, "apply", "--binary", "--whitespace=nowarn", f.Name()); err != nil {
			return "", 0, fmt.Errorf("copying your uncommitted changes: %w", err)
		}
	}
	untracked, err := git(repo, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return "", 0, err
	}
	var total int64
	files := 0
	for _, name := range strings.Split(untracked, "\x00") {
		if name == "" || strings.HasPrefix(name, ".p0ssess/") || files >= carryMaxFiles {
			continue
		}
		src := filepath.Join(repo, name)
		st, err := os.Lstat(src)
		if err != nil || !st.Mode().IsRegular() || total+st.Size() > carryMaxBytes {
			continue
		}
		dst := filepath.Join(wt, name)
		if !strings.HasPrefix(dst, filepath.Clean(wt)+string(filepath.Separator)) {
			continue
		}
		os.MkdirAll(filepath.Dir(dst), 0o755)
		if err := copyRegular(src, dst); err == nil {
			total += st.Size()
			files++
		}
	}
	if _, err := git(wt, "add", "-A"); err != nil {
		return "", 0, err
	}
	args := append(append([]string{}, commitIdentity...), "commit", "-q", "--no-verify", "--allow-empty", "-m", "p0ssess: the caller's uncommitted changes")
	if _, err := git(wt, args...); err != nil {
		return "", 0, err
	}
	base, err := git(wt, "rev-parse", "HEAD")
	return strings.TrimSpace(base), n, err
}

// applyToCaller applies a run's patch to the caller's checkout when it is still clean and on the commit
// the run started from. It returns "" when it applied, else why not.
func applyToCaller(repo, headAtStart, patch string) string {
	head, err := git(repo, "rev-parse", "HEAD")
	if err != nil {
		return "the checkout could not be read"
	}
	if strings.TrimSpace(head) != headAtStart {
		return "the checkout moved to another commit during the run"
	}
	if n, err := dirtyCount(repo); err != nil || n > 0 {
		return "the checkout has uncommitted changes"
	}
	if _, err := git(repo, "apply", "--check", "--binary", patch); err != nil {
		return "the patch does not apply cleanly"
	}
	if _, err := git(repo, "apply", "--binary", patch); err != nil {
		return "applying failed"
	}
	return ""
}

// applyScratch copies a scratch run's changed files back over the originals they were copied from, each
// only when the original is unchanged since the copy; the original is kept in the run's directory first.
// It returns what it copied back, the commands that undo that, the copy commands left for files it did
// not touch, and why it left them.
func applyScratch(rec *SessionRecord, wt string) (applied, reverts, keep, notes []string) {
	origs := make([]string, 0, len(rec.Copies))
	for o := range rec.Copies {
		origs = append(origs, o)
	}
	sort.Strings(origs)
	for i, orig := range origs {
		cp := rec.Copies[orig]
		changed := filepath.Join(wt, filepath.Base(cp))
		st, err := os.Lstat(changed)
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		now, err1 := os.ReadFile(changed)
		base, err2 := os.ReadFile(cp)
		if err1 != nil || err2 != nil || bytes.Equal(now, base) {
			continue
		}
		left := "cp " + shellQuote(changed) + " " + shellQuote(orig)
		ost, err := os.Stat(orig)
		cur, rerr := os.ReadFile(orig)
		if err != nil || rerr != nil || !ost.Mode().IsRegular() || !bytes.Equal(cur, base) {
			keep = append(keep, left)
			notes = append(notes, "not copied back to "+orig+": it changed during the run")
			continue
		}
		backup := filepath.Join(RunDir(rec.SessionID), "originals", fmt.Sprintf("%d-%s", i+1, filepath.Base(orig)))
		if err := paths.WriteFileAtomic(backup, cur, 0o600); err != nil {
			keep = append(keep, left)
			continue
		}
		if err := paths.WriteFileAtomic(orig, now, ost.Mode().Perm()); err != nil {
			keep = append(keep, left)
			notes = append(notes, "not copied back to "+orig+": "+err.Error())
			continue
		}
		applied = append(applied, orig+" (copied back)")
		reverts = append(reverts, "cp "+shellQuote(backup)+" "+shellQuote(orig))
	}
	a, r, k, n := placeNew(rec, wt)
	return append(applied, a...), append(reverts, r...), append(keep, k...), append(notes, n...)
}

// placeNew puts the files a scratch run created into the directory the task named, never over an
// existing file; without one it says where they are.
func placeNew(rec *SessionRecord, wt string) (applied, reverts, keep, notes []string) {
	copied := map[string]bool{}
	for _, cp := range rec.Copies {
		copied[filepath.Base(cp)] = true
	}
	var fresh []string
	var total int64
	filepath.WalkDir(wt, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(wt, p)
		if d.IsDir() {
			if rel == ".git" || rel == ".p0ssess" {
				return filepath.SkipDir
			}
			return nil
		}
		if rel == ".git" || copied[rel] || !d.Type().IsRegular() {
			return nil
		}
		if info, err := d.Info(); err == nil && len(fresh) < scratchMaxFiles && total+info.Size() <= scratchMaxBytes {
			total += info.Size()
			fresh = append(fresh, rel)
		}
		return nil
	})
	sort.Strings(fresh)
	for _, rel := range fresh {
		src := filepath.Join(wt, rel)
		if rec.Dest == "" {
			notes = append(notes, "new file left in the scratch copy: "+src)
			continue
		}
		dst := filepath.Join(rec.Dest, rel)
		left := "mkdir -p " + shellQuote(filepath.Dir(dst)) + " && cp " + shellQuote(src) + " " + shellQuote(dst)
		if _, err := os.Lstat(dst); err == nil {
			keep = append(keep, left)
			notes = append(notes, "not copied to "+dst+": a file is already there")
			continue
		}
		b, err := os.ReadFile(src)
		if err == nil {
			err = paths.WriteFileAtomic(dst, b, 0o644)
		}
		if err != nil {
			keep = append(keep, left)
			notes = append(notes, "not copied to "+dst+": "+err.Error())
			continue
		}
		applied = append(applied, dst+" (new)")
		reverts = append(reverts, "rm "+shellQuote(dst))
	}
	return applied, reverts, keep, notes
}
