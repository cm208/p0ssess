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

	"p0ssess/internal/account"
	"p0ssess/internal/budget"
	"p0ssess/internal/cloudsandbox"
	"p0ssess/internal/config"
	"p0ssess/internal/funnel"
	"p0ssess/internal/pme/paths"
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

// sandboxSetting is where the user asked for agent jobs to run: the environment (tests, development),
// then the plain-words setting in budget.toml, then config.json. Empty or "auto" lets p0ssess pick.
func sandboxSetting(cfg config.Config) string {
	if v := os.Getenv("P0SSESS_SANDBOX"); v != "" {
		return v
	}
	if b, err := budget.Load(paths.Default().BudgetFile()); err == nil && b.Sandbox.Where != "" {
		return b.Sandbox.Where
	}
	return cfg.Sandbox
}

var (
	localOnce sync.Once
	localErr  error
)

// LocalAvailable reports whether the local jail can run here: Linux with working rootless podman. It
// asks podman once per process. A missing jail image is not a reason to leave: the relay builds it.
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
	return localErr
}

// Where is the place an agent job runs.
type Where int

const (
	Local       Where = iota // the local jail
	Cloud                    // the hosted sandbox
	NeedPro                  // it would run in the cloud sandbox, which needs Pro or Team
	Unavailable              // nowhere: the setting asks for local and this machine cannot
)

// Placement is what Decide looks at; tests fill it in.
type Placement struct {
	Setting  string // "local", "cloud", "auto" or ""
	LocalErr error  // why the local jail cannot run here, nil when it can
	Pro      bool   // a linked Pro or Team install
	Env      bool   // Setting came from P0SSESS_SANDBOX, which forces the choice for tests and development
	GOOS     string
}

// NeedsPro is the plain line a Free install gets when an agent job needs a sandbox it does not have.
type NeedsPro struct{ GOOS string }

func (e *NeedsPro) Error() string {
	fix := "use Linux with rootless podman"
	if e.GOOS == "linux" {
		fix = "install rootless podman"
	}
	return "This job needs a sandbox, and it can run in the p0ssess cloud sandbox with Pro (/p0ssess:pro). " +
		"For now, read-only questions still run on the direct path, or " + fix + "."
}

var proOnce sync.Once

// noteUpgradeViewed counts the offer as seen, once per process; the count goes nowhere unless the user
// opted in to usage counts.
func noteUpgradeViewed() {
	proOnce.Do(func() { funnel.Note(paths.Default(), funnel.UpgradeViewed, time.Now()) })
}

func decide(p Placement) (Where, error) {
	switch p.Setting {
	case "cloud":
		if p.Pro || p.Env {
			return Cloud, nil
		}
		return NeedPro, &NeedsPro{p.GOOS}
	case "local":
		if p.LocalErr == nil {
			return Local, nil
		}
		return Unavailable, fmt.Errorf("agent jobs are set to run on this machine only, and %v", p.LocalErr)
	}
	if p.LocalErr == nil {
		return Local, nil
	}
	if p.Pro {
		return Cloud, nil
	}
	return NeedPro, &NeedsPro{p.GOOS}
}

// Decide says where an agent job runs (D27): the sandbox setting ("auto", the default, "local" or
// "cloud"), or P0SSESS_SANDBOX. Auto keeps the local jail where rootless podman works and uses the
// cloud sandbox everywhere else for a Pro or Team install. A Free install gets *NeedsPro.
func Decide(cfg config.Config) (Where, error) {
	p := Placement{Setting: sandboxSetting(cfg), GOOS: runtime.GOOS, Env: os.Getenv("P0SSESS_SANDBOX") != ""}
	if p.Setting != "cloud" {
		p.LocalErr = LocalAvailable(cfg)
	}
	p.Pro = account.Current(paths.Default()).Pro() && cloudsandbox.DeviceToken(DeviceTokenFile()) != ""
	w, err := decide(p)
	if w == NeedPro {
		noteUpgradeViewed()
	}
	return w, err
}

// UseCloud reports whether a session runs in the cloud sandbox.
func UseCloud(cfg config.Config) bool {
	w, _ := Decide(cfg)
	return w == Cloud
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
	token := cloudsandbox.DeviceToken(DeviceTokenFile())
	fetch := func(ctx context.Context) (*cloudsandbox.Ticket, error) {
		t, err := cloudsandbox.FetchTicket(ctx, plain, cloudsandbox.CloudURL(), token)
		if errors.Is(err, cloudsandbox.ErrNotPro) {
			noteUpgradeViewed()
			return nil, &NeedsPro{runtime.GOOS}
		}
		return t, err
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

	var decl []cloudsandbox.ModelDecl
	for _, m := range spec.Models {
		decl = append(decl, cloudsandbox.ModelDecl{ID: m.ID, Name: m.Name, Context: m.Context, Output: m.Output,
			InPerM: m.InPerM, OutPerM: m.OutPerM, CacheRead: m.CacheReadPerM})
	}
	s, err := createSession(ctx, hc, fetch, cloudsandbox.Spec{Workdir: j.RemoteDir,
		Internet: spec.ShellInternet, Client: "p0ssess " + clientVersion() + " " + runtime.GOOS + "/" + runtime.GOARCH, Model: model,
		Models: decl}, cfg, log)
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
func (j *Jail) StartRun(run string, capUSD float64, model string, rates ...proxy.Rates) error {
	var r proxy.Rates
	if len(rates) > 0 {
		r = rates[0]
	}
	if !j.Cloud {
		return proxy.WriteRunSpec(filepath.Join(ctlDir(j.Name), runFileName), proxy.RunSpec{Run: run, CapUSD: capUSD, Model: model, Rates: r})
	}
	live := liveCloud(j.Name)
	if live == nil {
		return errors.New("this cloud sandbox session belongs to another process; start a new run")
	}
	live.mu.Lock()
	live.run = run
	live.mu.Unlock()
	live.meter.StartRun(capUSD, model, r)
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

// createSession opens a session on the host, waiting in its queue when every slot is taken. A job that
// waits shows its place in the status line and gives up after the user's limit (sandbox.wait_minutes).
func createSession(ctx context.Context, hc *http.Client, fetch func(context.Context) (*cloudsandbox.Ticket, error), spec cloudsandbox.Spec, cfg config.Config, log io.Writer) (*cloudsandbox.Session, error) {
	d := paths.Default()
	limit := budget.Sandbox{}.QueueWait()
	if b, err := budget.Load(d.BudgetFile()); err == nil {
		limit = b.Sandbox.QueueWait()
	}
	queued := false
	since := time.Now()
	defer func() {
		if queued {
			cloudsandbox.ClearQueue(d)
		}
	}()
	used := func(m string) error {
		return errors.New(m + " They come back on the 1st; read-only questions still run on the direct path.")
	}
	for attempt := 0; ; attempt++ {
		t, err := fetch(ctx)
		if err != nil {
			return nil, err
		}
		host, err := cloudsandbox.HostURL(t)
		if err != nil {
			return nil, err
		}
		spec.URL, spec.Ticket = host, t.Ticket
		s, err := cloudsandbox.Create(ctx, hc, spec)
		var ae *cloudsandbox.APIError
		switch {
		case err == nil:
			cloudsandbox.ForgetUsage(d)
			return s, nil
		case !errors.As(err, &ae):
			return nil, err
		case ae.Code == "not_entitled":
			noteUpgradeViewed()
			return nil, &NeedsPro{runtime.GOOS}
		case ae.Code == "allowance_used":
			return nil, used(ae.Message)
		case ae.Code != "at_capacity" || attempt >= 5:
			return nil, err
		}
		if !queued {
			queued, since = true, time.Now()
		}
		werr := cloudsandbox.WaitForSlot(ctx, hc, host, cloudsandbox.WaitOptions{MaxWait: limit - time.Since(since),
			Ticket: func(ctx context.Context) (string, error) {
				t, err := fetch(ctx)
				if err != nil {
					return "", err
				}
				return t.Ticket, nil
			},
			OnUpdate: func(q cloudsandbox.QueueInfo) {
				cloudsandbox.SaveQueue(d, q, since, time.Now())
				if !q.Ready {
					fmt.Fprintln(log, "cloud sandbox busy:", q.QueuedText())
				}
			}})
		var ae2 *cloudsandbox.APIError
		switch {
		case werr == nil:
		case errors.As(werr, &ae2) && ae2.Code == "allowance_used":
			return nil, used(ae2.Message)
		default:
			return nil, werr
		}
	}
}
