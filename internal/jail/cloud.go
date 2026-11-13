package jail

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"p0ssess/internal/cloudsandbox"
	"p0ssess/internal/config"
	"p0ssess/internal/proxy"
)

// A cloud jail runs the session on a p0ssess cloud sandbox host instead of a local pod. Its record looks
// like a local one (name, port, password file, deadline file), so the relay drives both the same way:
// the port is a loopback forwarder to the host's OpenCode API, and the deadline file feeds the same key
// proxy, which here runs in this process behind the cost meter. The session lives as long as the process
// that started it, because the tunnel does.

// CloudUpstream is where the cloud tunnel sends model calls; tests point it at a fake OpenRouter.
var CloudUpstream = proxy.Upstream

type cloudLive struct {
	s     *cloudsandbox.Session
	meter *cloudsandbox.Meter
	mu    sync.Mutex
	run   string // the run the meter counts now
}

var (
	cloudMu   sync.Mutex
	cloudJail = map[string]*cloudLive{}
)

func liveCloud(name string) *cloudLive {
	cloudMu.Lock()
	defer cloudMu.Unlock()
	return cloudJail[name]
}

// DeviceTokenFile is where the account link keeps its token (auth.md).
func DeviceTokenFile() string { return config.Path("secrets", "p0ssess_token") }

func sandboxSetting(cfg config.Config) string {
	if v := os.Getenv("P0SSESS_SANDBOX"); v != "" {
		return v
	}
	return cfg.Sandbox
}

var (
	localOnce sync.Once
	localErr  error
)

// LocalAvailable reports whether the local jail can run here: Linux with working rootless podman and the
// jail image built. It asks podman once per process.
func LocalAvailable(cfg config.Config) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("it needs Linux with rootless podman (this is %s)", runtime.GOOS)
	}
	localOnce.Do(func() {
		if _, err := exec.LookPath("podman"); err != nil {
			localErr = errors.New("podman is not installed")
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "podman", "info", "--format", "{{.Host.Security.Rootless}}").Output()
		if err != nil || strings.TrimSpace(string(out)) != "true" {
			localErr = errors.New("rootless podman is not working")
		}
	})
	if localErr != nil {
		return localErr
	}
	image := cfg.Image
	if image == "" {
		image = DefaultImage
	}
	if exec.Command("podman", "image", "exists", image).Run() != nil {
		return fmt.Errorf("jail image %s is missing; run `p0ssess jail build`", image)
	}
	return nil
}

// UseCloud decides where a session runs (D27): the `sandbox` setting ("local", "cloud", or "auto", the
// default) or P0SSESS_SANDBOX. Auto keeps the local jail where it works and uses the cloud sandbox
// everywhere else, for an install linked to an account.
func UseCloud(cfg config.Config) bool {
	switch sandboxSetting(cfg) {
	case "cloud":
		return true
	case "local":
		return false
	}
	if LocalAvailable(cfg) == nil {
		return false
	}
	return cloudsandbox.DeviceToken(DeviceTokenFile()) != ""
}

// CloudReady reports whether the cloud sandbox could take a session from this install.
func CloudReady(cfg config.Config) bool {
	return sandboxSetting(cfg) != "local" && cloudsandbox.DeviceToken(DeviceTokenFile()) != ""
}

func sandboxURL(t *cloudsandbox.Ticket) (string, error) {
	if v := os.Getenv("P0SSESS_SANDBOX_URL"); v != "" {
		return v, nil
	}
	if t.SandboxURL != "" {
		return t.SandboxURL, nil
	}
	return "", errors.New("the cloud sandbox is not available yet (no sandbox host was assigned)")
}

func startCloud(ctx context.Context, spec Spec, cfg config.Config, log io.Writer) (*Jail, error) {
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
	plain := &http.Client{Timeout: 30 * time.Second}
	ticket, err := cloudsandbox.FetchTicket(ctx, plain, cloudsandbox.CloudURL(), cloudsandbox.DeviceToken(DeviceTokenFile()))
	if err != nil {
		return nil, err
	}
	host, err := sandboxURL(ticket)
	if err != nil {
		return nil, err
	}
	hc, err := cloudsandbox.Transport(os.Getenv("P0SSESS_SANDBOX_CA"))
	if err != nil {
		return nil, err
	}

	src := repo
	if spec.Worktree != "" {
		src = spec.Worktree
	}
	j := &Jail{Name: newName(), Repo: repo, Worktree: spec.Worktree, Image: "cloud", Created: t0.UTC(), ZDR: spec.ZDR,
		ShellInternet: spec.ShellInternet, Owner: Self(), KeepUntil: t0.Add(startGrace).UTC(), Cloud: true,
		LocalRoot: src, RemoteDir: cloudsandbox.RemoteWorkdir(filepath.Base(repo))}
	if err := os.MkdirAll(ctlDir(j.Name), 0o700); err != nil {
		return nil, err
	}
	if err := j.SetDeadline(t0.Add(startGrace)); err != nil {
		return nil, err
	}
	if err := j.save(); err != nil {
		return nil, err
	}
	meter := cloudsandbox.NewMeter()
	meter.Lookup = cloudsandbox.GenerationLookup(plain, CloudUpstream, key)
	model := proxy.WithDeadline(meter.Guard(proxy.Handler(CloudUpstream, key, spec.ZDR)), filepath.Join(ctlDir(j.Name), "deadline"))

	s, err := cloudsandbox.Create(ctx, hc, cloudsandbox.Spec{URL: host, Ticket: ticket.Ticket, Workdir: j.RemoteDir,
		Internet: spec.ShellInternet, Client: "p0ssess " + clientVersion() + " " + runtime.GOOS + "/" + runtime.GOARCH, Model: model})
	if err != nil {
		os.RemoveAll(jailDir(j.Name))
		return nil, err
	}
	live := &cloudLive{s: s, meter: meter}
	cloudMu.Lock()
	cloudJail[j.Name] = live
	cloudMu.Unlock()
	fail := func(err error) (*Jail, error) {
		_ = j.Remove(context.Background())
		return nil, err
	}
	man, res, err := s.Upload(ctx, src)
	if err != nil {
		return fail(err)
	}
	u, err := url.Parse(s.APIURL())
	if err != nil || u.Host == "" {
		return fail(errors.New("cloud sandbox: no local API port"))
	}
	_, port, _ := net.SplitHostPort(u.Host)
	if j.Port, err = strconv.Atoi(port); err != nil {
		return fail(err)
	}
	if err := os.WriteFile(passwordPath(j.Name), []byte(s.Password()+"\n"), 0o600); err != nil {
		return fail(err)
	}
	j.UploadNote = man.Note()
	if err := j.waitReady(ctx, 90*time.Second); err != nil {
		return fail(err)
	}
	j.StartupS = time.Since(t0).Round(time.Millisecond).Seconds()
	if err := j.save(); err != nil {
		return fail(err)
	}
	fmt.Fprintf(log, "cloud sandbox %s ready in %.2fs (%d files, %d KiB; sandbox start %.2fs)\n", j.Name, j.StartupS, res.Files, res.Bytes>>10, res.StartupS)
	return j, nil
}

// RemotePath maps a directory on this machine to the path the session's OpenCode server sees.
func (j *Jail) RemotePath(local string) string {
	if !j.Cloud {
		return local
	}
	rel, err := filepath.Rel(j.LocalRoot, local)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return j.RemoteDir
	}
	return path.Join(j.RemoteDir, filepath.ToSlash(rel))
}

const runFileName = "run.json"

// StartRun names the next run and its cost cap and model for the jail's cost meter: the local key proxy
// reads them from the run file, a cloud session's meter is told directly.
func (j *Jail) StartRun(run string, capUSD float64, model string) error {
	if !j.Cloud {
		return proxy.WriteRunSpec(filepath.Join(ctlDir(j.Name), runFileName), proxy.RunSpec{Run: run, CapUSD: capUSD, Model: model})
	}
	live := liveCloud(j.Name)
	if live == nil {
		return errors.New("this cloud sandbox session belongs to another process; start a new run")
	}
	live.mu.Lock()
	live.run = run
	live.mu.Unlock()
	live.meter.StartRun(capUSD, model)
	return nil
}

// RunCost is what the jail's cost meter charged run. ok is false when the meter has no record of it:
// a jail started before metering, a cloud session owned by another process, or a run that is not the
// cloud session's current one.
func RunCost(name, run string) (proxy.RunCost, bool) {
	if live := liveCloud(name); live != nil {
		live.mu.Lock()
		cur := live.run
		live.mu.Unlock()
		if cur != run {
			return proxy.RunCost{}, false
		}
		return live.meter.RunSpend(), true
	}
	return proxy.ReadRunCost(MeterFile(name), run)
}

// MeterFile is where a local jail's key proxy records its calls.
func MeterFile(name string) string { return filepath.Join(meterDir(name), proxy.MeterFile) }

// Sync brings an edit session's cloud changes into its local worktree, so the relay's own diff of the
// worktree reports them. Local jails write the worktree directly.
func (j *Jail) Sync(ctx context.Context) error {
	if !j.Cloud || j.Worktree == "" {
		return nil
	}
	live := liveCloud(j.Name)
	if live == nil {
		return errors.New("the cloud sandbox session is gone; its changes could not be fetched")
	}
	patch, err := live.s.Diff(ctx)
	if err != nil {
		return err
	}
	gd := filepath.Join(j.Repo, ".git", "worktrees", filepath.Base(j.Worktree))
	env := []string{"GIT_DIR=" + gd, "GIT_COMMON_DIR=" + filepath.Join(j.Repo, ".git"), "GIT_WORK_TREE=" + j.Worktree}
	base, err := exec.Command("git", "--git-dir", gd, "rev-parse", "HEAD").Output()
	if err != nil {
		return fmt.Errorf("worktree base: %w", err)
	}
	return cloudsandbox.ApplyToWorktree(j.Worktree, env, strings.TrimSpace(string(base)), patch)
}

func (j *Jail) verifyCloud(ctx context.Context) error {
	live := liveCloud(j.Name)
	if live == nil {
		return errors.New("the cloud sandbox session belongs to a process that is gone")
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if _, err := live.s.Status(cctx); err != nil {
		return err
	}
	c, err := j.Client()
	if err != nil {
		return err
	}
	_, err = c.CheckVersion(cctx)
	return err
}

func (j *Jail) removeCloud(ctx context.Context) error {
	cloudMu.Lock()
	live := cloudJail[j.Name]
	delete(cloudJail, j.Name)
	cloudMu.Unlock()
	var err error
	if live != nil {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err = live.s.Close(cctx)
		cancel()
	}
	if rerr := os.RemoveAll(jailDir(j.Name)); err == nil {
		err = rerr
	}
	return err
}

func (j *Jail) cloudStatus(ctx context.Context) Status {
	s := Status{Jail: j, Egress: "cloud sandbox"}
	live := liveCloud(j.Name)
	if live == nil {
		s.State = "elsewhere"
		return s
	}
	st, err := live.s.Status(ctx)
	if err != nil {
		st = "ended"
	}
	s.State = st
	if c, err := j.Client(); err == nil {
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		s.Version, _ = c.CheckVersion(cctx)
		cancel()
	}
	return s
}

func clientVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" {
		return bi.Main.Version
	}
	return "dev"
}
