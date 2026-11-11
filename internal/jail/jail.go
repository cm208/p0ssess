// Package jail runs the pinned OpenCode server in a rootless podman pod with LAN and VPN egress blocked.
// Every relay session gets its own pod: it is created for the session's first run and removed when the
// session ends, so no two sessions share a server, a database or a worktree.
//
//	pod p0ssess-<random>    userns keep-id, 127.0.0.1:<random port> -> 4096, nft egress filter in its netns
//	  <pod>-proxy            holds the OpenRouter key, forwards to openrouter.ai, stops at the deadline
//	  <pod>-egress           only with shell internet on: the shell's proxy to public addresses
//	  <pod>-server           opencode serve; private tmpfs /state; the repository read-only with .p0ssess/
//	                         and .git/worktrees/ hidden; an edit run's own worktree read-write
package jail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"p0ssess/internal/config"
	"p0ssess/internal/opencode"
	"p0ssess/internal/pme/paths"
)

const (
	containerPort = 4096
	WorktreeDir   = ".p0ssess/wt"

	ctlMount    = "/run/p0ssess-ctl"
	egressMount = "/run/p0ssess-egress"
	// EgressSocketEnv tells the shell wrapper where the egress proxy's socket is; set only with shell internet.
	EgressSocketEnv = "P0SSESS_SHELL_EGRESS"

	// startGrace covers the time between writing a record and the first prompt.
	startGrace = 10 * time.Minute
	// orphanAge is how old a pod with no record (or a record with no pod) must be before a sweep removes
	// it, so a jail another process is still creating is left alone.
	orphanAge = 2 * time.Minute
)

// Spec is what one session's jail needs.
type Spec struct {
	Repo string
	// Worktree is an edit session's own worktree, mounted read-write; empty for a read session.
	Worktree      string
	ShellInternet bool
	ZDR           bool
}

// Jail is the record of one session's pod, kept in <state>/relay/jails/<name>/jail.json.
type Jail struct {
	Name          string    `json:"name"`
	Repo          string    `json:"repo"`
	Worktree      string    `json:"worktree,omitempty"`
	Port          int       `json:"port"`
	Image         string    `json:"image"`
	Created       time.Time `json:"created"`
	ZDR           bool      `json:"zdr,omitempty"`
	ShellInternet bool      `json:"shell_internet,omitempty"`
	Session       string    `json:"session,omitempty"`
	// Owner is the process that removes the jail when the session ends. A sweep removes the jail of an
	// owner that died, unless it was detached on purpose; then KeepUntil bounds it.
	Owner     Owner     `json:"owner"`
	Detached  bool      `json:"detached,omitempty"`
	KeepUntil time.Time `json:"keep_until"`
	StartupS  float64   `json:"startup_s,omitempty"`
}

// Dir is where jail records live.
func Dir() string { return config.RelayPath("jails") }

// StateLabel ties pods to the state directory that holds their records, so a sweep only ever looks at
// jails of its own p0ssess home (tests and scratch setups use their own).
func StateLabel() string {
	h := sha256.Sum256([]byte(config.RelayDir()))
	return "p0ssess.state=" + hex.EncodeToString(h[:6])
}

func jailDir(name string) string      { return filepath.Join(Dir(), name) }
func recordPath(name string) string   { return filepath.Join(jailDir(name), "jail.json") }
func passwordPath(name string) string { return filepath.Join(jailDir(name), "pw") }
func ctlDir(name string) string       { return filepath.Join(jailDir(name), "ctl") }
func egressDir(name string) string    { return filepath.Join(jailDir(name), "egress") }

// Canonical resolves symlinks so the mount path, the session directory and Claude's paths agree.
func Canonical(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if !st.IsDir() {
		return "", fmt.Errorf("%s is not a directory", real)
	}
	return real, nil
}

// NotMountable is a directory the agent path refuses to mount into a jail.
type NotMountable struct {
	Dir, Why string
}

func (e *NotMountable) Error() string {
	return fmt.Sprintf("refused: the agent path mounts only a project's git repository, and %s %s. "+
		"Pass cwd with the project's directory (inside its git repository), or use the direct tool", e.Dir, e.Why)
}

// RepoRoot returns the git top level of dir. Outside a git work tree it refuses: the jail never mounts
// an arbitrary directory.
func RepoRoot(dir string) (string, error) {
	c, err := Canonical(dir)
	if err != nil {
		return "", err
	}
	cmd := exec.Command("git", "-C", c, "rev-parse", "--show-toplevel")
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_DIR=") && !strings.HasPrefix(kv, "GIT_WORK_TREE=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	out, err := cmd.Output()
	top := strings.TrimSpace(string(out))
	if err != nil || top == "" {
		return "", &NotMountable{c, "is not inside a git work tree"}
	}
	return Canonical(top)
}

// secretNames at the top of a directory mark a home directory or a credential store.
var secretNames = []string{".ssh", ".gnupg", ".config", ".aws", ".azure", ".kube", ".docker", ".password-store",
	".netrc", ".git-credentials", ".pgpass"}

// CheckMountable refuses a repository the jail must not mount: one that is or holds the home directory or
// the root, is not the top of a git work tree, has credential directories at its top, or holds p0ssess's
// own config, cache, state or key file.
func CheckMountable(dir, keyFile string) error {
	c, err := Canonical(dir)
	if err != nil {
		return err
	}
	if c == "/" {
		return &NotMountable{c, "is the root directory"}
	}
	if h, err := os.UserHomeDir(); err == nil {
		if home, err := Canonical(h); err == nil && (c == home || inside(c, home)) {
			return &NotMountable{c, "is or contains your home directory"}
		}
	}
	top, err := RepoRoot(c)
	if err != nil {
		return err
	}
	if top != c {
		return &NotMountable{c, "is not the top of its git repository (" + top + ")"}
	}
	for _, n := range secretNames {
		if _, err := os.Lstat(filepath.Join(c, n)); err == nil {
			return &NotMountable{c, "has " + n + " at its top, like a home directory or a credential store"}
		}
	}
	d := paths.Default()
	for _, p := range []string{d.Config, d.Cache, d.State, keyFile} {
		if p == "" {
			continue
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		if r, err := filepath.EvalSymlinks(abs); err == nil {
			abs = r
		}
		if abs == c || inside(c, abs) || inside(abs, c) {
			return &NotMountable{c, "holds or is inside p0ssess's own files (" + abs + ")"}
		}
	}
	return nil
}

// inside reports whether p is strictly below dir.
func inside(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, "../")
}

func newName() string {
	b := make([]byte, 6)
	rand.Read(b)
	return "p0ssess-" + hex.EncodeToString(b)
}

// Load reads a jail record by pod name.
func Load(name string) (*Jail, error) {
	if strings.ContainsAny(name, "/\\") || !strings.HasPrefix(name, "p0ssess-") {
		return nil, fmt.Errorf("not a jail name: %q", name)
	}
	b, err := os.ReadFile(recordPath(name))
	if err != nil {
		return nil, err
	}
	var j Jail
	return &j, json.Unmarshal(b, &j)
}

// List returns every jail record.
func List() ([]*Jail, error) {
	ents, err := os.ReadDir(Dir())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	var out []*Jail
	for _, e := range ents {
		if j, err := Load(e.Name()); err == nil {
			out = append(out, j)
		}
	}
	return out, nil
}

func (j *Jail) save() error {
	if err := os.MkdirAll(jailDir(j.Name), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(j, "", "  ")
	tmp := recordPath(j.Name) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, recordPath(j.Name))
}

func (j *Jail) URL() string { return "http://127.0.0.1:" + strconv.Itoa(j.Port) }

// Client returns an API client for the jailed server.
func (j *Jail) Client() (*opencode.Client, error) {
	pw, err := os.ReadFile(passwordPath(j.Name))
	if err != nil {
		return nil, err
	}
	return opencode.New(j.URL(), strings.TrimSpace(string(pw))), nil
}

// SetDeadline moves the key proxy's deadline: after t it refuses every model request.
func (j *Jail) SetDeadline(t time.Time) error {
	p := filepath.Join(ctlDir(j.Name), "deadline")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.FormatInt(t.Unix(), 10)+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Keep records how long the jail must survive if its owner goes away, and moves the deadline with it.
func (j *Jail) Keep(until, deadline time.Time) error {
	j.KeepUntil = until
	if err := j.SetDeadline(deadline); err != nil {
		return err
	}
	return j.save()
}

// Adopt makes this process the jail's owner, for example a later `p0ssess wait` on a detached run.
func (j *Jail) Adopt() error {
	j.Owner, j.Detached = Self(), false
	return j.save()
}

// Detach leaves the jail with no owning process until keepUntil; a sweep removes it after that.
func (j *Jail) Detach(keepUntil time.Time) error {
	j.Detached, j.KeepUntil = true, keepUntil
	return j.save()
}

func podman(ctx context.Context, env []string, stdin io.Reader, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "podman", args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = stdin
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	if err != nil {
		return so.String(), fmt.Errorf("podman %s: %v: %s", args[0], err, strings.TrimSpace(se.String()))
	}
	return so.String(), nil
}

func newPassword() string {
	buf := make([]byte, 32)
	rand.Read(buf)
	return base64.RawURLEncoding.EncodeToString(buf)
}

// PrepareGit creates the worktree directories and keeps .p0ssess/ out of git status.
func PrepareGit(repo string) error {
	gitDir := filepath.Join(repo, ".git")
	if st, err := os.Stat(gitDir); err != nil || !st.IsDir() {
		return errors.New("edit mode needs a repository whose .git is a directory")
	}
	for _, d := range []string{filepath.Join(repo, WorktreeDir), filepath.Join(gitDir, "worktrees"), filepath.Join(gitDir, "info")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	ex := filepath.Join(gitDir, "info", "exclude")
	b, _ := os.ReadFile(ex)
	if !bytes.Contains(b, []byte("/.p0ssess/")) {
		f, err := os.OpenFile(ex, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		if len(b) > 0 && b[len(b)-1] != '\n' {
			f.WriteString("\n")
		}
		f.WriteString("/.p0ssess/\n")
		f.Close()
	}
	return nil
}

func (j *Jail) infraPID(ctx context.Context) (string, error) {
	out, err := podman(ctx, nil, nil, "inspect", "-f", "{{.State.Pid}}", j.Name+"-infra")
	pid := strings.TrimSpace(out)
	if err != nil || pid == "" || pid == "0" {
		return "", fmt.Errorf("pod infra container is not running: %v", err)
	}
	return pid, nil
}

func (j *Jail) applyNft(ctx context.Context, extraV4, extraV6 []string) error {
	pid, err := j.infraPID(ctx)
	if err != nil {
		return err
	}
	rules := NftRules(extraV4, extraV6)
	// The rules must come through stdin: inside the user namespace nft cannot open a host file path.
	if _, err := podman(ctx, nil, strings.NewReader(rules), "unshare", "nsenter", "-t", pid, "-n", "nft", "-f", "-"); err != nil {
		return err
	}
	return j.checkNft(ctx)
}

// checkNft confirms the egress table is loaded in the pod's network namespace.
func (j *Jail) checkNft(ctx context.Context) error {
	pid, err := j.infraPID(ctx)
	if err != nil {
		return err
	}
	out, err := podman(ctx, nil, nil, "unshare", "nsenter", "-t", pid, "-n", "nft", "list", "table", "inet", NftTable)
	if err != nil {
		return fmt.Errorf("egress filter missing: %w", err)
	}
	if !strings.Contains(out, "10.0.0.0/8") || !strings.Contains(out, "reject") {
		return errors.New("egress filter incomplete")
	}
	return nil
}

func mountOpt(cfg config.Config, mode string) string {
	if cfg.SELinux == "disable" {
		return mode
	}
	return mode + ",z"
}

func securityOpts(cfg config.Config) []string {
	o := []string{"--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "2048"}
	if cfg.SELinux == "disable" {
		o = append(o, "--security-opt", "label=disable")
	}
	return o
}

// blockedPrefixes splits the host's own prefixes and the user's blocked_cidrs into IPv4 and IPv6.
func blockedPrefixes(cfg config.Config) (v4, v6 []string) {
	v4, v6 = HostPrefixes()
	for _, s := range cfg.BlockedCIDRs {
		if strings.Contains(s, ":") {
			v6 = append(v6, s)
		} else {
			v4 = append(v4, s)
		}
	}
	return v4, v6
}

func binary() (string, error) {
	exe := os.Getenv("P0SSESS_BIN") // the binary the containers run; tests point it at a fresh build
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return "", err
		}
	}
	return filepath.EvalSymlinks(exe)
}

// Start creates a new jail for one session and returns it once the server answers. The record is
// written before the pod exists, so a sweep in another process never takes a jail being built for an
// orphan.
func Start(ctx context.Context, spec Spec, cfg config.Config, log io.Writer) (*Jail, error) {
	t0 := time.Now()
	repo, err := Canonical(spec.Repo)
	if err != nil {
		return nil, err
	}
	if err := CheckMountable(repo, cfg.OpenRouterKeyFile); err != nil {
		return nil, err
	}
	key, err := cfg.OpenRouterKey()
	if err != nil {
		return nil, err
	}
	image := cfg.Image
	if image == "" {
		image = DefaultImage
	}
	if _, err := podman(ctx, nil, nil, "image", "exists", image); err != nil {
		return nil, fmt.Errorf("jail image %s is missing; run `p0ssess jail build`", image)
	}
	exe, err := binary()
	if err != nil {
		return nil, err
	}
	ocCfg, err := OpenCodeConfig()
	if err != nil {
		return nil, err
	}
	j := &Jail{Name: newName(), Repo: repo, Worktree: spec.Worktree, Image: image, Created: t0.UTC(),
		ZDR: spec.ZDR, ShellInternet: spec.ShellInternet, Owner: Self(), KeepUntil: t0.Add(startGrace).UTC()}
	pw := newPassword()
	for _, d := range []string{ctlDir(j.Name), egressDir(j.Name)} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(passwordPath(j.Name), []byte(pw+"\n"), 0o600); err != nil {
		return nil, err
	}
	if err := j.SetDeadline(t0.Add(startGrace)); err != nil {
		return nil, err
	}
	if err := j.save(); err != nil {
		return nil, err
	}
	fail := func(err error) (*Jail, error) {
		_ = j.Remove(context.Background())
		return nil, err
	}

	if _, err := podman(ctx, nil, nil, "pod", "create", "--name", j.Name, "--infra-name", j.Name+"-infra",
		"--userns=keep-id", "--network", "pasta", "-p", "127.0.0.1::"+strconv.Itoa(containerPort),
		"--label", "p0ssess=1", "--label", "p0ssess.run=1", "--label", StateLabel(), "--label", "p0ssess.repo="+repo); err != nil {
		return fail(err)
	}
	if _, err := podman(ctx, nil, nil, "pod", "start", j.Name); err != nil {
		return fail(err)
	}
	v4, v6 := blockedPrefixes(cfg)
	if err := j.applyNft(ctx, v4, v6); err != nil {
		return fail(fmt.Errorf("applying the egress filter failed, jail not started: %w", err))
	}
	if j.Port, err = j.hostPort(ctx); err != nil {
		return fail(err)
	}

	args := append([]string{"run", "-d", "--pod", j.Name, "--name", j.Name + "-proxy", "--label", "p0ssess=1"}, securityOpts(cfg)...)
	args = append(args, "-e", "OPENROUTER_API_KEY",
		"-v", exe+":/opt/p0ssess:"+mountOpt(cfg, "ro"),
		"-v", ctlDir(j.Name)+":"+ctlMount+":"+mountOpt(cfg, "ro"),
		"--entrypoint", "/opt/p0ssess", image, "proxy", "--listen", ProxyAddr, "--deadline-file", ctlMount+"/deadline")
	if spec.ZDR {
		args = append(args, "--zdr")
	}
	if _, err := podman(ctx, []string{"OPENROUTER_API_KEY=" + key}, nil, args...); err != nil {
		return fail(err)
	}

	if spec.ShellInternet {
		args = append([]string{"run", "-d", "--pod", j.Name, "--name", j.Name + "-egress", "--label", "p0ssess=1"}, securityOpts(cfg)...)
		args = append(args, "-v", exe+":/opt/p0ssess:"+mountOpt(cfg, "ro"),
			"-v", egressDir(j.Name)+":"+egressMount+":"+mountOpt(cfg, "rw"),
			"--entrypoint", "/opt/p0ssess", image, "egress", "--socket", egressMount+"/egress.sock")
		for _, p := range append(v4, v6...) {
			args = append(args, "--block", p)
		}
		if _, err := podman(ctx, nil, nil, args...); err != nil {
			return fail(err)
		}
		if err := waitFile(ctx, filepath.Join(egressDir(j.Name), "egress.sock"), 20*time.Second); err != nil {
			return fail(fmt.Errorf("the shell's egress proxy did not start: %w", err))
		}
	}

	args = append([]string{"run", "-d", "--pod", j.Name, "--name", j.Name + "-server", "--label", "p0ssess=1"}, securityOpts(cfg)...)
	args = append(args, "--tmpfs", "/tmp:rw,size=1g,mode=1777",
		"--tmpfs", "/state:rw,size=1g,mode=0700,U",
		"-v", exe+":"+ServerExePath+":"+mountOpt(cfg, "ro"),
		"-v", repo+":"+repo+":"+mountOpt(cfg, "ro"))
	// Other sessions' worktrees and their git admin directories live under these two; hide both, then
	// mount this session's own worktree back in.
	for _, d := range []string{filepath.Join(repo, ".p0ssess"), filepath.Join(repo, ".git", "worktrees")} {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			args = append(args, "--mount", "type=tmpfs,destination="+d+",tmpfs-size=64k,tmpfs-mode=0555,notmpcopyup")
		}
	}
	if spec.Worktree != "" {
		admin := filepath.Join(repo, ".git", "worktrees", filepath.Base(spec.Worktree))
		args = append(args, "-v", spec.Worktree+":"+spec.Worktree+":"+mountOpt(cfg, "rw"),
			"-v", admin+":"+admin+":"+mountOpt(cfg, "rw"))
	}
	env := []string{
		"OPENCODE_PASSWORD=" + pw,
		"OPENCODE_CONFIG_CONTENT=" + string(ocCfg),
		"OPENROUTER_API_KEY=" + ProxyKeyPlaceholder,
	}
	if spec.ShellInternet {
		args = append(args, "-v", egressDir(j.Name)+":"+egressMount+":"+mountOpt(cfg, "ro"),
			"-e", EgressSocketEnv+"="+egressMount+"/egress.sock")
	}
	args = append(args,
		"-e", "OPENCODE_PASSWORD", "-e", "OPENCODE_CONFIG_CONTENT", "-e", "OPENROUTER_API_KEY",
		"-e", "HOME=/state/home", "-e", "XDG_CONFIG_HOME=/state/config", "-e", "XDG_DATA_HOME=/state/data",
		"-e", "XDG_STATE_HOME=/state/state", "-e", "XDG_CACHE_HOME=/state/cache",
		"-e", "OPENCODE_DISABLE_PROJECT_CONFIG=1", "-e", "OPENCODE_DISABLE_CLAUDE_CODE=1",
		"-e", "OPENCODE_DISABLE_CLAUDE_CODE_SKILLS=1", "-e", "GIT_CONFIG_NOSYSTEM=1", "-e", "PYTHONDONTWRITEBYTECODE=1",
		"-w", repo, image, "opencode", "serve", "--hostname", "0.0.0.0", "--port", strconv.Itoa(containerPort))
	if _, err := podman(ctx, env, nil, args...); err != nil {
		return fail(err)
	}
	if err := j.waitReady(ctx, 90*time.Second); err != nil {
		return fail(err)
	}
	j.StartupS = time.Since(t0).Round(time.Millisecond).Seconds()
	if err := j.save(); err != nil {
		return fail(err)
	}
	fmt.Fprintf(log, "jail %s ready in %.2fs for %s\n", j.Name, j.StartupS, repo)
	return j, nil
}

func (j *Jail) hostPort(ctx context.Context) (int, error) {
	out, err := podman(ctx, nil, nil, "port", j.Name+"-infra", strconv.Itoa(containerPort)+"/tcp")
	if err != nil {
		return 0, err
	}
	line := strings.TrimSpace(strings.SplitN(strings.TrimSpace(out), "\n", 2)[0])
	host, port, err := net.SplitHostPort(line)
	if err != nil || host != "127.0.0.1" {
		return 0, fmt.Errorf("unexpected published port %q", line)
	}
	return strconv.Atoi(port)
}

func waitFile(ctx context.Context, p string, d time.Duration) error {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(p); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	return fmt.Errorf("%s did not appear", filepath.Base(p))
}

func (j *Jail) waitReady(ctx context.Context, d time.Duration) error {
	c, err := j.Client()
	if err != nil {
		return err
	}
	deadline := time.Now().Add(d)
	var last error
	for time.Now().Before(deadline) {
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		_, last = c.CheckVersion(cctx)
		cancel()
		if last == nil {
			return nil
		}
		if errors.Is(last, opencode.ErrUnsupportedVersion) {
			return last
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return fmt.Errorf("server did not come up: %v", last)
}

// LogTail is the end of the OpenCode server's log, on one line, for an error the server answered without a
// body. The server's password and anything shaped like an OpenRouter key are masked.
func (j *Jail) LogTail(ctx context.Context) string {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(cctx, "podman", "logs", "--tail", "15", j.Name+"-server").CombinedOutput()
	pw, _ := os.ReadFile(passwordPath(j.Name))
	return cleanLog(string(out), strings.TrimSpace(string(pw)))
}

var orKey = regexp.MustCompile(`sk-or-[A-Za-z0-9_-]+`)

func cleanLog(s, pw string) string {
	if pw != "" {
		s = strings.ReplaceAll(s, pw, "[password]")
	}
	s = orKey.ReplaceAllString(s, "[key]")
	s = strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " | ")), " ")
	if len(s) > 600 {
		s = "…" + s[len(s)-600:]
	}
	return s
}

// Containers are the pod's containers.
func (j *Jail) Containers() []string {
	cs := []string{j.Name + "-infra", j.Name + "-proxy", j.Name + "-server"}
	if j.ShellInternet {
		cs = append(cs, j.Name+"-egress")
	}
	return cs
}

// Verify checks that the pod and its containers run and the egress filter is in place.
func (j *Jail) Verify(ctx context.Context) error {
	out, err := podman(ctx, nil, nil, append([]string{"inspect", "-f", "{{.Name}} {{.State.Status}}"}, j.Containers()...)...)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if !strings.HasSuffix(line, " running") {
			return fmt.Errorf("not running: %s", line)
		}
	}
	if err := j.checkNft(ctx); err != nil {
		return err
	}
	c, err := j.Client()
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err = c.CheckVersion(cctx)
	return err
}

// Remove deletes the pod, its containers and the jail's record, password and sockets.
func (j *Jail) Remove(ctx context.Context) error {
	_, err := podman(ctx, nil, nil, "pod", "rm", "-f", "-t", "0", "--ignore", j.Name)
	if rerr := os.RemoveAll(jailDir(j.Name)); err == nil {
		err = rerr
	}
	return err
}

type Status struct {
	Jail    *Jail
	State   string
	Egress  string
	Version string
}

func (j *Jail) Status(ctx context.Context) Status {
	s := Status{Jail: j}
	out, err := podman(ctx, nil, nil, "pod", "inspect", "-f", "{{.State}}", j.Name)
	if err != nil {
		s.State = "absent"
		return s
	}
	s.State = strings.ToLower(strings.TrimSpace(out))
	if err := j.checkNft(ctx); err != nil {
		s.Egress = "MISSING"
	} else {
		s.Egress = "filtered"
	}
	if c, err := j.Client(); err == nil {
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		s.Version, _ = c.CheckVersion(cctx)
		cancel()
	}
	return s
}
