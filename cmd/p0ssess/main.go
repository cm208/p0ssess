// Command p0ssess is the whole plugin in one binary: the MCP server Claude Code starts, the routing
// engine, the direct path, the jail, the status line and the prompt hook.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"p0ssess/internal/account"
	"p0ssess/internal/config"
	"p0ssess/internal/egress"
	"p0ssess/internal/jail"
	"p0ssess/internal/pme/cli"
	"p0ssess/internal/pme/local"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/pme/remote"
	"p0ssess/internal/proxy"
	"p0ssess/internal/relay"
	"p0ssess/internal/server"
)

var version = "0.6.0"

const usage = `p0ssess %s: Claude Code hands work to other models through OpenRouter

usage:
  p0ssess mcp                         the plugin's MCP server on stdio (relay, direct and engine tools)
  p0ssess me --brief                  a few lines: spend, limits, running jobs; starts the local page if none runs
  p0ssess me [--open]                 the local page: spend, Claude usage, rules, settings
  p0ssess account pro|status|notify|signout   p0ssess Pro: upgrade, check the plan, get notified, sign out
  p0ssess engine COMMAND ...          the routing engine (recommend, status, set, rules, outcome, ...)
  p0ssess direct COMMAND ...          one-shot OpenRouter calls with local files, and ffmpeg helpers
  p0ssess setup COMMAND ...           show, allow, statusline, summaries, undo (changes Claude Code settings)
  p0ssess statusline                  status line wrapper (reads Claude Code's JSON on stdin)
  p0ssess hook prompt|tool            UserPromptSubmit and PreToolUse hooks (read the hook JSON on stdin)
  p0ssess jail build|status|sweep|up|down|probe ...   the sandbox for the agent path (Linux)
  p0ssess run --model M [--mode read|edit] [--cwd D] [--session S] [--attach F] [--max-cost USD] [--detach] [--keep] TASK
  p0ssess wait|abort --session S      the relay tools, from the command line (JSON out)
  p0ssess answer --session S --request R --decision once|reject
      run, wait and answer stay until the run ends or asks for a permission decision, printing progress.
      --detach returns after --wait seconds instead, leaving the run unwatched (see docs/RELAY.md).
      A finished run's jail is removed on exit unless --keep keeps it for a follow-up (--session).
  p0ssess proxy --listen ADDR [--zdr] [--deadline-file F]   (inside the jail) OpenRouter forwarder
  p0ssess egress --socket PATH [--block CIDR]...             (inside the jail) the shell's internet proxy
  p0ssess version

The OpenRouter key is read from ~/.config/p0ssess/secrets/openrouter_key, or $P0SSESS_OPENROUTER_KEY_FILE.
`

func main() {
	cli.Version = version
	remote.Version = version
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, usage, version)
		os.Exit(2)
	}
	// Inside the jail, OpenCode invokes this binary as its shell: `p0ssess -c <command>`. Dispatch that
	// before flag parsing, because "-c" is not a subcommand. On success RunJailedShell never returns.
	if jail.IsJailedShellInvocation(os.Args[1:]) {
		if err := jail.RunJailedShell(os.Args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "p0ssess:", err)
			os.Exit(127)
		}
		return
	}
	// The shell wrapper starts the egress forwarder inside a command's own namespaces with these.
	if len(os.Args) == 3 && (os.Args[1] == jail.NetForwardArg || os.Args[1] == jail.NetServeArg) {
		var err error
		if os.Args[1] == jail.NetForwardArg {
			err = egress.StartForwarder(jail.ServerExePath, jail.NetServeArg, os.Args[2])
		} else {
			err = egress.ServeForwarder(os.Args[2])
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "p0ssess:", err)
			os.Exit(1)
		}
		return
	}
	cmd, args := os.Args[1], os.Args[2:]
	// The status line and the hook run on every refresh and prompt: nothing else loads for them.
	switch cmd {
	case "statusline":
		os.Exit(cli.Run([]string{"statusline"}, stdio()))
	case "hook":
		if len(args) == 0 || (args[0] != "prompt" && args[0] != "tool") {
			fmt.Fprintln(os.Stderr, "p0ssess: unknown hook")
			os.Exit(2)
		}
		os.Exit(cli.Run([]string{"hook", args[0]}, stdio()))
	case "engine":
		os.Exit(cli.Run(args, stdio()))
	case "me":
		if contains(args, "--brief") || contains(args, "-brief") {
			if err := meBrief(args); err != nil {
				fmt.Fprintln(os.Stderr, "p0ssess:", err)
				os.Exit(1)
			}
			return
		}
		os.Exit(cli.Run(append([]string{"me"}, args...), stdio()))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	switch cmd {
	case "mcp":
		refreshStableCopy()
		go account.Watch(ctx, paths.Default())
		err = server.Serve(ctx, os.Stdin, os.Stdout, version)
	case "account":
		err = accountCmd(ctx, args)
	case "direct":
		err = directCmd(ctx, args)
	case "setup":
		err = setupCmd(args)
	case "jail":
		err = jailCmd(ctx, args)
	case "proxy":
		err = proxyCmd(args)
	case "egress":
		err = egressCmd(args)
	case "run", "wait", "abort", "answer":
		if aerr := server.AgentAvailable(); aerr != nil {
			err = fmt.Errorf("the agent path is unavailable here: %w", aerr)
			break
		}
		err = runCmd(ctx, cmd, args)
	case "version", "--version", "-v":
		fmt.Println("p0ssess", version, "opencode", jail.OpenCodeVersion)
	case "help", "-h", "--help":
		fmt.Printf(usage, version)
	default:
		fmt.Fprintf(os.Stderr, usage, version)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "p0ssess:", err)
		os.Exit(1)
	}
}

func stdio() cli.IO { return cli.IO{In: os.Stdin, Out: os.Stdout, Err: os.Stderr} }

func proxyCmd(args []string) error {
	fs := flag.NewFlagSet("proxy", flag.ExitOnError)
	listen := fs.String("listen", jail.ProxyAddr, "listen address")
	zdr := fs.Bool("zdr", false, "require zero-retention endpoints")
	deadline := fs.String("deadline-file", "", "refuse requests after the Unix time in this file")
	fs.Parse(args)
	key := os.Getenv("OPENROUTER_API_KEY")
	os.Unsetenv("OPENROUTER_API_KEY")
	if key == "" {
		return errors.New("OPENROUTER_API_KEY is not set")
	}
	return proxy.Serve(*listen, key, *zdr, *deadline)
}

func egressCmd(args []string) error {
	fs := flag.NewFlagSet("egress", flag.ExitOnError)
	socket := fs.String("socket", "", "Unix socket to listen on")
	var block multi
	fs.Var(&block, "block", "another range to refuse (repeatable)")
	fs.Parse(args)
	if *socket == "" {
		return errors.New("--socket is required")
	}
	extra, err := egress.ParsePrefixes(block)
	if err != nil {
		return err
	}
	return egress.Serve(*socket, egress.Policy{Extra: extra})
}

func jailCmd(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("jail: missing subcommand (build, status, sweep, up, down, probe)")
	}
	fs := flag.NewFlagSet("jail "+args[0], flag.ExitOnError)
	repo := fs.String("repo", "", "repository directory (up)")
	name := fs.String("name", "", "jail name (down, probe)")
	tarball := fs.String("tarball", "", "local OpenCode release tarball (build)")
	image := fs.String("image", "", "image reference")
	internet := fs.Bool("shell-internet", false, "up: start the shell's egress proxy too")
	legacy := fs.Bool("legacy", false, "sweep: also remove p0ssess 0.3's shared per-repository jails")
	asJSON := fs.Bool("json", false, "JSON output")
	fs.Parse(args[1:])
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if *image != "" {
		cfg.Image = *image
	}
	if args[0] != "build" {
		if err := relay.Migrate(os.Stderr); err != nil {
			return err
		}
	}
	load := func() (*jail.Jail, error) {
		if *name == "" {
			return nil, errors.New("--name is required (see `p0ssess jail status`)")
		}
		return jail.Load(*name)
	}
	switch args[0] {
	case "build":
		img := cfg.Image
		if img == "" {
			img = jail.DefaultImage
		}
		return jail.Build(ctx, img, *tarball, os.Stdout)
	case "up":
		// A read-only jail with no session, for probing and timing. It stays up for session_idle_s.
		if *repo == "" {
			return errors.New("--repo is required")
		}
		r, err := jail.RepoRoot(*repo)
		if err != nil {
			return err
		}
		cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		j, err := jail.Start(cctx, jail.Spec{Repo: r, ShellInternet: *internet}, cfg, os.Stderr)
		if err != nil {
			return err
		}
		if err := j.Detach(time.Now().Add(time.Duration(cfg.SessionIdleSec) * time.Second)); err != nil {
			return err
		}
		fmt.Printf("%s %s startup_s=%.2f\n", j.Name, j.URL(), j.StartupS)
		return nil
	case "down":
		j, err := load()
		if err != nil {
			return err
		}
		return j.Remove(ctx)
	case "sweep":
		removed, err := jail.Sweep(ctx, os.Stderr)
		if err != nil {
			return err
		}
		if *legacy {
			old, err := jail.SweepLegacy(ctx)
			removed = append(removed, old...)
			if err != nil {
				return err
			}
		}
		if len(removed) == 0 {
			fmt.Println("nothing to remove")
		}
		for _, r := range removed {
			fmt.Println("removed", r)
		}
		return nil
	case "status":
		js, err := jail.List()
		if err != nil {
			return err
		}
		var all []jail.Status
		for _, j := range js {
			all = append(all, j.Status(ctx))
		}
		if *asJSON {
			return json.NewEncoder(os.Stdout).Encode(all)
		}
		if len(all) == 0 {
			fmt.Println("no jails")
		}
		for _, s := range all {
			net := ""
			if s.Jail.ShellInternet {
				net = " shell-internet"
			}
			fmt.Printf("%s  %-8s egress=%-8s opencode=%-6s session=%s%s  %s\n", s.Jail.Name, s.State, s.Egress, s.Version,
				s.Jail.Session, net, s.Jail.Repo)
		}
		return nil
	case "probe":
		j, err := load()
		if err != nil {
			return err
		}
		res, err := j.Probe(ctx, fs.Args())
		if err != nil {
			return err
		}
		for _, p := range res {
			fmt.Printf("%-45s %-5v %s\n", p.Target, p.OK, p.Detail)
		}
		return nil
	}
	return fmt.Errorf("jail: unknown subcommand %q", strings.Join(args, " "))
}

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func runCmd(ctx context.Context, name string, args []string) error {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	var req relay.RunRequest
	var attach multi
	var request, decision, message string
	var detach, keep bool
	fs.StringVar(&req.Model, "model", "", "OpenRouter model id")
	fs.StringVar(&req.Mode, "mode", "read", "read or edit")
	fs.StringVar(&req.Cwd, "cwd", "", "working directory")
	fs.StringVar(&req.Session, "session", "", "session to continue")
	fs.StringVar(&req.Kind, "kind", "", "task kind for the history")
	fs.StringVar(&req.UseCase, "use-case", "", "catalog use case id")
	fs.IntVar(&req.Stakes, "stakes", 0, "stakes 1-4")
	fs.Float64Var(&req.MaxCostUSD, "max-cost", 0, "cost cap for this run (USD); 0 takes max_run_cost_usd")
	fs.IntVar(&req.WaitS, "wait", 0, "with --detach, seconds before returning still_running")
	fs.BoolVar(&detach, "detach", false, "return still_running after --wait seconds instead of watching the run to its end")
	fs.BoolVar(&keep, "keep", false, "keep a finished run's jail for a follow-up (--session) until session_idle_s passes")
	fs.Var(&attach, "attach", "file to attach (repeatable)")
	fs.StringVar(&request, "request", "", "permission request id")
	fs.StringVar(&decision, "decision", "", "once or reject")
	fs.StringVar(&message, "message", "", "feedback for the model")
	fs.Parse(args)
	req.Attachments = attach
	req.Task = strings.Join(fs.Args(), " ")
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	// No Shutdown here: a run that is paused on a permission request, or detached, stays alive for a
	// later answer or wait. Ctrl-C still interrupts the run being waited on.
	if err := relay.Migrate(os.Stderr); err != nil {
		return err
	}
	e := relay.NewEngine(cfg, version, os.Stderr)
	e.Sweep(ctx)
	eng, err := local.Load(paths.Default())
	if err != nil {
		return err
	}
	e.SetObserver(server.NewObserver(eng))
	progress := func(v float64, msg string) { fmt.Fprintf(os.Stderr, "progress: %s\n", msg) }
	var res *relay.Result
	switch name {
	case "run":
		res, err = e.Run(ctx, req, progress)
	case "wait":
		res, err = e.Wait(ctx, req.Session, req.WaitS, progress)
	case "abort":
		res, err = e.Abort(ctx, req.Session)
	case "answer":
		res, err = e.Answer(ctx, req.Session, request, decision, message, req.WaitS, progress)
	}
	if name != "abort" {
		res, err = keepWatching(ctx, res, err, detach, func(ctx context.Context, sid string) (*relay.Result, error) {
			return e.Wait(ctx, sid, req.WaitS, progress)
		})
	}
	e.Release(res, keep)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", " ")
	return enc.Encode(res)
}

// detachNote explains what leaving a run unwatched costs. The watch that enforces the cap and the
// maximum runtime and records the cost lives in this process.
const detachNote = "detached: nothing watches this run now, so its cost cap and maximum runtime are not enforced and its " +
	"cost is not recorded until `p0ssess wait --session <id>` (or the MCP wait tool) picks it up. Until then its reservation " +
	"counts as spent at the full cap once the hold (max_runtime_s + 15 min) passes."

// keepWatching waits on a run that returned still_running until it ends or needs a decision, so the
// watch stays alive and the reservation settles at the measured cost. With detach it returns at once and
// says what that means.
func keepWatching(ctx context.Context, res *relay.Result, err error, detach bool,
	wait func(context.Context, string) (*relay.Result, error)) (*relay.Result, error) {
	for err == nil && !detach && res != nil && res.Status == "still_running" && ctx.Err() == nil {
		res, err = wait(ctx, res.SessionID)
	}
	if err == nil && res != nil && res.Status == "still_running" {
		res.Note = strings.TrimSpace(res.Note + " " + detachNote)
	}
	return res, err
}
