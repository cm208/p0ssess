package cloudsandbox

import (
	"bufio"
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// CheckPatch refuses a diff from the sandbox host whose paths git should never see: absolute paths,
// "..", or any .git component. git apply has its own checks; this one runs first and fails closed.
func CheckPatch(patch []byte) error {
	sc := bufio.NewScanner(bytes.NewReader(patch))
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	inHunk := false
	for sc.Scan() {
		l := sc.Text()
		if strings.HasPrefix(l, "diff --git ") {
			inHunk = false
			rest := strings.TrimPrefix(l, "diff --git ")
			a, b, ok := splitGitPair(rest)
			if !ok {
				return fmt.Errorf("unreadable diff header %q", clipLine(l))
			}
			for _, p := range []string{a, b} {
				if err := checkPatchPath(p); err != nil {
					return err
				}
			}
			continue
		}
		if strings.HasPrefix(l, "@@") {
			inHunk = true
			continue
		}
		if inHunk && (strings.HasPrefix(l, "+") || strings.HasPrefix(l, "-") || strings.HasPrefix(l, " ") || l == "" || strings.HasPrefix(l, "\\")) {
			continue
		}
		inHunk = false
		for _, pre := range []string{"--- ", "+++ ", "rename from ", "rename to ", "copy from ", "copy to "} {
			if p, ok := strings.CutPrefix(l, pre); ok {
				if p == "/dev/null" {
					break
				}
				if pre == "--- " || pre == "+++ " {
					p = unquote(p)
					if !strings.HasPrefix(p, "a/") && !strings.HasPrefix(p, "b/") {
						return fmt.Errorf("diff path %q has no a/ or b/ prefix", clipLine(p))
					}
					p = p[2:]
				} else {
					p = unquote(p)
				}
				if err := checkPatchPath(p); err != nil {
					return err
				}
			}
		}
	}
	return sc.Err()
}

func clipLine(s string) string {
	if len(s) > 80 {
		return s[:80] + "..."
	}
	return s
}

func unquote(p string) string {
	if strings.HasPrefix(p, `"`) {
		if u, err := strconv.Unquote(p); err == nil {
			return u
		}
	}
	return p
}

// splitGitPair splits `a/x b/x` (possibly quoted) into the two paths without their prefixes.
func splitGitPair(s string) (string, string, bool) {
	var parts []string
	for len(s) > 0 && len(parts) < 2 {
		s = strings.TrimLeft(s, " ")
		if strings.HasPrefix(s, `"`) {
			end := 1
			for end < len(s) && (s[end] != '"' || s[end-1] == '\\') {
				end++
			}
			if end >= len(s) {
				return "", "", false
			}
			parts = append(parts, unquote(s[:end+1]))
			s = s[end+1:]
			continue
		}
		// Unquoted names may contain spaces; git writes them as "a/<p> b/<p>" with equal halves.
		if len(parts) == 0 && strings.HasPrefix(s, "a/") {
			if i := strings.Index(s, " b/"); i >= 0 {
				parts = append(parts, s[:i])
				s = s[i+1:]
				continue
			}
		}
		parts = append(parts, s)
		s = ""
	}
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "a/") || !strings.HasPrefix(parts[1], "b/") {
		return "", "", false
	}
	return parts[0][2:], parts[1][2:], true
}

func checkPatchPath(p string) error {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") || strings.ContainsRune(p, 0) {
		return fmt.Errorf("refusing a sandbox diff that touches %q", clipLine(p))
	}
	for _, c := range strings.Split(p, "/") {
		if c == ".." || c == "." || c == "" || strings.EqualFold(c, ".git") || strings.EqualFold(strings.TrimRight(c, ". "), ".git") {
			return fmt.Errorf("refusing a sandbox diff that touches %q", clipLine(p))
		}
	}
	return nil
}

// ApplyToWorktree resets an edit session's worktree to its base and applies the sandbox's diff. git runs
// with its directories pinned (env), like the relay's own diff, so a file in the worktree cannot
// redirect it.
func ApplyToWorktree(wt string, env []string, base string, patch []byte) error {
	if err := CheckPatch(patch); err != nil {
		return err
	}
	run := func(stdin []byte, args ...string) error {
		cmd := exec.Command("git", args...)
		cmd.Dir = wt
		cmd.Env = append(gitEnv(), env...)
		if stdin != nil {
			cmd.Stdin = bytes.NewReader(stdin)
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	if err := run(nil, "-c", "core.hooksPath=/dev/null", "reset", "-q", "--hard", base); err != nil {
		return err
	}
	if err := run(nil, "clean", "-q", "-f", "-d", "-x"); err != nil {
		return err
	}
	if len(bytes.TrimSpace(patch)) == 0 {
		return nil
	}
	return run(patch, "-c", "core.hooksPath=/dev/null", "apply", "--binary", "--whitespace=nowarn", "-")
}
