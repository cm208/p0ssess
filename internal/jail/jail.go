// Package jail runs the pinned OpenCode server in a rootless podman pod with LAN and VPN egress blocked.
//
// Layout of one jail (one per repository):
//
//	pod p0ssess-<hash>        userns keep-id, port 127.0.0.1:<port> -> 4096, nft egress filter in its netns
//	  <pod>-proxy             holds the OpenRouter key, forwards to openrouter.ai, forces data_collection=deny
//	  <pod>-server            opencode serve; repo mounted read-only at its host path, worktrees read-write
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
	"strconv"
	"strings"
	"time"

	"p0ssess/internal/config"
	"p0ssess/internal/opencode"
)

const (
	containerPort = 4096
	portBase      = 47400
	portSpan      = 500
	WorktreeDir   = ".p0ssess/wt"
)

type Jail struct {
	Name    string    `json:"name"`
	Repo    string    `json:"repo"`
	Port    int       `json:"port"`
	Image   string    `json:"image"`
	Git     bool      `json:"git"`
	Created time.Time `json:"created"`
	// ZDR is set when the key proxy requires zero-retention endpoints.
	ZDR bool `json:"zdr,omitempty"`
}

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

// RepoRoot returns the git top level of dir, or dir itself outside git.
func RepoRoot(dir string) (string, error) {
	c, err := Canonical(dir)
	if err != nil {
		return "", err
	}
	out, err := exec.Command("git", "-C", c, "rev-parse", "--show-toplevel").Output()
	if err == nil {
		if top := strings.TrimSpace(string(out)); top != "" {
			return Canonical(top)
		}
	}
	return c, nil
}

func NameFor(repo string) string {
	h := sha256.Sum256([]byte(repo))
	return "p0ssess-" + hex.EncodeToString(h[:])[:10]
}

func recordPath(name string) string   { return config.Path("jails", name+".json") }
func passwordPath(name string) string { return config.Path("jails", name+".pw") }

func Load(repo string) (*Jail, error) {
	b, err := os.ReadFile(recordPath(NameFor(repo)))
	if err != nil {
		return nil, err
	}
	var j Jail
	return &j, json.Unmarshal(b, &j)
}

func List() ([]*Jail, error) {
	files, _ := filepath.Glob(config.Path("jails", "*.json"))
	var out []*Jail
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var j Jail
		if json.Unmarshal(b, &j) == nil {
			out = append(out, &j)
		}
	}
	return out, nil
}

func (j *Jail) save() error {
	if err := os.MkdirAll(filepath.Dir(recordPath(j.Name)), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(j, "", "  ")
	return os.WriteFile(recordPath(j.Name), b, 0o600)
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

func freePort(name string) int {
	h := sha256.Sum256([]byte(name))
	start := int(h[0])<<8 | int(h[1])
	for i := 0; i < portSpan; i++ {
		p := portBase + (start+i)%portSpan
		l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
		if err == nil {
			l.Close()
			return p
		}
	}
	return 0
}

func ensurePassword(name string) (string, error) {
	p := passwordPath(name)
	if b, err := os.ReadFile(p); err == nil && len(bytes.TrimSpace(b)) > 0 {
		return strings.TrimSpace(string(b)), nil
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	pw := base64.RawURLEncoding.EncodeToString(buf)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", err
	}
	return pw, os.WriteFile(p, []byte(pw+"\n"), 0o600)
}

// prepareGit creates the worktree mount points and keeps .p0ssess/ out of git status.
func prepareGit(repo string) (bool, error) {
	gitDir := filepath.Join(repo, ".git")
	st, err := os.Stat(gitDir)
	if err != nil || !st.IsDir() {
		return false, nil
	}
	for _, d := range []string{filepath.Join(repo, WorktreeDir), filepath.Join(gitDir, "worktrees"), filepath.Join(gitDir, "info")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return true, err
		}
	}
	ex := filepath.Join(gitDir, "info", "exclude")
	b, _ := os.ReadFile(ex)
	if !bytes.Contains(b, []byte("/.p0ssess/")) {
		f, err := os.OpenFile(ex, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return true, err
		}
		if len(b) > 0 && b[len(b)-1] != '\n' {
			f.WriteString("\n")
		}
		f.WriteString("/.p0ssess/\n")
		f.Close()
	}
	return true, nil
}

func (j *Jail) infraPID(ctx context.Context) (string, error) {
	out, err := podman(ctx, nil, nil, "inspect", "-f", "{{.State.Pid}}", j.Name+"-infra")
	pid := strings.TrimSpace(out)
	if err != nil || pid == "" || pid == "0" {
		return "", fmt.Errorf("pod infra container is not running: %v", err)
	}
	return pid, nil
}

func (j *Jail) applyNft(ctx context.Context) error {
	pid, err := j.infraPID(ctx)
	if err != nil {
		return err
	}
	v4, v6 := HostPrefixes()
	rules := NftRules(v4, v6)
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

func (j *Jail) mountOpt(cfg config.Config, mode string) string {
	if cfg.SELinux == "disable" {
		return mode
	}
	return mode + ",z"
}

func (j *Jail) securityOpts(cfg config.Config) []string {
	o := []string{"--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "2048"}
	if cfg.SELinux == "disable" {
		o = append(o, "--security-opt", "label=disable")
	}
	return o
}

// Up starts the jail for repo, replacing a stale one. It is safe to call when the jail already runs.
func Up(ctx context.Context, repo string, cfg config.Config, log io.Writer) (*Jail, error) {
	repo, err := Canonical(repo)
	if err != nil {
		return nil, err
	}
	name := NameFor(repo)
	j, _ := Load(repo)
	// A jail whose proxy was started for another privacy mode is restarted, so the mode always holds.
	if j != nil && j.ZDR == cfg.ZDR && j.Verify(ctx) == nil {
		return j, nil
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
	exe := os.Getenv("P0SSESS_BIN") // the binary the proxy container runs; tests point it at a fresh build
	if exe == "" {
		if exe, err = os.Executable(); err != nil {
			return nil, err
		}
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return nil, err
	}
	_ = Down(ctx, repo)
	if j == nil {
		j = &Jail{Name: name, Repo: repo}
	}
	j.Image, j.Created, j.ZDR = image, time.Now().UTC(), cfg.ZDR
	if j.Port == 0 || !portFree(j.Port) {
		j.Port = freePort(name)
		if j.Port == 0 {
			return nil, errors.New("no free port for the jail")
		}
	}
	pw, err := ensurePassword(name)
	if err != nil {
		return nil, err
	}
	if j.Git, err = prepareGit(repo); err != nil {
		return nil, err
	}
	ocCfg, err := OpenCodeConfig()
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(log, "jail %s for %s on %s\n", name, repo, j.URL())

	vol := name + "-state"
	if _, err := podman(ctx, nil, nil, "volume", "exists", vol); err != nil {
		if _, err := podman(ctx, nil, nil, "volume", "create", "--label", "p0ssess=1", vol); err != nil {
			return nil, err
		}
	}
	if _, err := podman(ctx, nil, nil, "pod", "create", "--name", name, "--infra-name", name+"-infra",
		"--userns=keep-id", "--network", "pasta", "-p", "127.0.0.1:"+strconv.Itoa(j.Port)+":"+strconv.Itoa(containerPort),
		"--label", "p0ssess=1", "--label", "p0ssess.repo="+repo); err != nil {
		return nil, err
	}
	fail := func(err error) (*Jail, error) {
		_ = Down(context.Background(), repo)
		return nil, err
	}
	if _, err := podman(ctx, nil, nil, "pod", "start", name); err != nil {
		return fail(err)
	}
	if err := j.applyNft(ctx); err != nil {
		return fail(fmt.Errorf("applying the egress filter failed, jail not started: %w", err))
	}
	fmt.Fprintln(log, "egress filter loaded")

	args := append([]string{"run", "-d", "--pod", name, "--name", name + "-proxy", "--label", "p0ssess=1"}, j.securityOpts(cfg)...)
	args = append(args, "-e", "OPENROUTER_API_KEY", "-v", exe+":/opt/p0ssess:"+j.mountOpt(cfg, "ro"),
		"--entrypoint", "/opt/p0ssess", image, "proxy", "--listen", ProxyAddr)
	if cfg.ZDR {
		args = append(args, "--zdr")
	}
	if _, err := podman(ctx, []string{"OPENROUTER_API_KEY=" + key}, nil, args...); err != nil {
		return fail(err)
	}

	args = append([]string{"run", "-d", "--pod", name, "--name", name + "-server", "--label", "p0ssess=1"}, j.securityOpts(cfg)...)
	args = append(args, "--tmpfs", "/tmp:rw,size=1g,mode=1777",
		"-v", vol+":/state:U",
		"-v", exe+":"+ServerExePath+":"+j.mountOpt(cfg, "ro"),
		"-v", repo+":"+repo+":"+j.mountOpt(cfg, "ro"))
	if j.Git {
		wt := filepath.Join(repo, WorktreeDir)
		gw := filepath.Join(repo, ".git", "worktrees")
		args = append(args, "-v", wt+":"+wt+":"+j.mountOpt(cfg, "rw"), "-v", gw+":"+gw+":"+j.mountOpt(cfg, "rw"))
	}
	env := []string{
		"OPENCODE_PASSWORD=" + pw,
		"OPENCODE_CONFIG_CONTENT=" + string(ocCfg),
		"OPENROUTER_API_KEY=" + ProxyKeyPlaceholder,
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
	if err := j.save(); err != nil {
		return fail(err)
	}
	fmt.Fprintln(log, "server ready")
	return j, nil
}

func portFree(p int) bool {
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
	if err != nil {
		return false
	}
	l.Close()
	return true
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
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("server did not come up: %v", last)
}

// Verify checks that the pod and both containers run and the egress filter is in place.
func (j *Jail) Verify(ctx context.Context) error {
	out, err := podman(ctx, nil, nil, "inspect", "-f", "{{.Name}} {{.State.Status}}", j.Name+"-infra", j.Name+"-proxy", j.Name+"-server")
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

// Down removes the pod and its containers. The state volume (sessions) is kept unless purge is set.
func Down(ctx context.Context, repo string) error {
	name := NameFor(repo)
	_, err := podman(ctx, nil, nil, "pod", "rm", "-f", "-t", "5", "--ignore", name)
	return err
}

func Purge(ctx context.Context, repo string) error {
	name := NameFor(repo)
	_ = Down(ctx, repo)
	_, err := podman(ctx, nil, nil, "volume", "rm", "-f", name+"-state")
	os.Remove(recordPath(name))
	os.Remove(passwordPath(name))
	return err
}

// Ensure returns a verified, running jail and a version-checked client for repo.
func Ensure(ctx context.Context, repo string, cfg config.Config, log io.Writer) (*Jail, *opencode.Client, error) {
	j, err := Up(ctx, repo, cfg, log)
	if err != nil {
		return nil, nil, err
	}
	c, err := j.Client()
	if err != nil {
		return nil, nil, err
	}
	return j, c, nil
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
