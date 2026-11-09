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

	"p0ssess/internal/config"
	"p0ssess/internal/jail"
	"p0ssess/internal/pme/cli"
	"p0ssess/internal/proxy"
	"p0ssess/internal/relay"
	"p0ssess/internal/server"
)

var version = "0.2.0"

const usage = `p0ssess %s: Claude Code hands work to other models through OpenRouter

usage:
  p0ssess mcp                         the plugin's MCP server on stdio (relay, direct and engine tools)
  p0ssess me [--open]                 the local page: spend, Claude usage, rules, settings
  p0ssess engine COMMAND ...          the routing engine (recommend, status, set, rules, outcome, ...)
  p0ssess direct COMMAND ...          one-shot OpenRouter calls with local files, and ffmpeg helpers
  p0ssess setup COMMAND ...           show, allow, statusline, summaries, undo (changes Claude Code settings)
  p0ssess statusline                  status line wrapper (reads Claude Code's JSON on stdin)
  p0ssess hook prompt                 UserPromptSubmit hook (reads the hook JSON on stdin)
  p0ssess jail build|up|down|purge|status|probe|unit ...   the sandbox for the agent path (Linux)
  p0ssess run --model M [--mode read|edit] [--cwd D] [--session S] [--attach F] TASK
  p0ssess wait|abort --session S      the relay tools, from the command line (JSON out)
  p0ssess answer --session S --request R --decision once|reject
  p0ssess proxy --listen ADDR         (inside the jail) OpenRouter forwarder; key from OPENROUTER_API_KEY
  p0ssess version

The OpenRouter key is read from ~/.config/p0ssess/secrets/openrouter_key, or $P0SSESS_OPENROUTER_KEY_FILE.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, usage, version)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	// The status line and the hook run on every refresh and prompt: nothing else loads for them.
	switch cmd {
	case "statusline":
		os.Exit(cli.Run([]string{"statusline"}, stdio()))
	case "hook":
		if len(args) > 0 && args[0] != "prompt" {
			fmt.Fprintln(os.Stderr, "p0ssess: unknown hook", args[0])
			os.Exit(2)
		}
		os.Exit(cli.Run([]string{"hook"}, stdio()))
	case "engine":
		os.Exit(cli.Run(args, stdio()))
	case "me":
		os.Exit(cli.Run(append([]string{"me"}, args...), stdio()))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	switch cmd {
	case "mcp":
		refreshStableCopy()
		err = server.Serve(ctx, os.Stdin, os.Stdout, version)
	case "direct":
		err = directCmd(ctx, args)
	case "setup":
		err = setupCmd(args)
	case "jail":
		err = jailCmd(ctx, args)
	case "proxy":
		err = proxyCmd(args)
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
	fs.Parse(args)
	key := os.Getenv("OPENROUTER_API_KEY")
	os.Unsetenv("OPENROUTER_API_KEY")
	if key == "" {
		return errors.New("OPENROUTER_API_KEY is not set")
	}
	return proxy.Serve(*listen, key)
}

func jailCmd(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("jail: missing subcommand")
	}
	fs := flag.NewFlagSet("jail "+args[0], flag.ExitOnError)
	repo := fs.String("repo", "", "repository directory")
	tarball := fs.String("tarball", "", "local OpenCode release tarball")
	image := fs.String("image", "", "image reference")
	asJSON := fs.Bool("json", false, "JSON output")
	fs.Parse(args[1:])
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if *image != "" {
		cfg.Image = *image
	}
	needRepo := func() (string, error) {
		if *repo == "" {
			return "", errors.New("--repo is required")
		}
		return jail.Canonical(*repo)
	}
	switch args[0] {
	case "build":
		img := cfg.Image
		if img == "" {
			img = jail.DefaultImage
		}
		return jail.Build(ctx, img, *tarball, os.Stdout)
	case "up":
		r, err := needRepo()
		if err != nil {
			return err
		}
		cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		j, err := jail.Up(cctx, r, cfg, os.Stdout)
		if err != nil {
			return err
		}
		fmt.Println(j.Name, j.URL())
		return nil
	case "down":
		r, err := needRepo()
		if err != nil {
			return err
		}
		return jail.Down(ctx, r)
	case "purge":
		r, err := needRepo()
		if err != nil {
			return err
		}
		return jail.Purge(ctx, r)
	case "status":
		var js []*jail.Jail
		if *repo != "" {
			r, err := needRepo()
			if err != nil {
				return err
			}
			j, err := jail.Load(r)
			if err != nil {
				return fmt.Errorf("no jail recorded for %s", r)
			}
			js = []*jail.Jail{j}
		} else {
			js, _ = jail.List()
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
			fmt.Printf("%s  %-8s egress=%-8s opencode=%-6s %s  %s\n", s.Jail.Name, s.State, s.Egress, s.Version, s.Jail.URL(), s.Jail.Repo)
		}
		return nil
	case "probe":
		r, err := needRepo()
		if err != nil {
			return err
		}
		j, err := jail.Load(r)
		if err != nil {
			return fmt.Errorf("no jail recorded for %s", r)
		}
		res, err := j.Probe(ctx, fs.Args())
		if err != nil {
			return err
		}
		for _, p := range res {
			fmt.Printf("%-45s %-5v %s\n", p.Target, p.OK, p.Detail)
		}
		return nil
	case "unit":
		fmt.Print(jail.SystemdUnit())
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
	fs.StringVar(&req.Model, "model", "", "OpenRouter model id")
	fs.StringVar(&req.Mode, "mode", "read", "read or edit")
	fs.StringVar(&req.Cwd, "cwd", "", "working directory")
	fs.StringVar(&req.Session, "session", "", "session to continue")
	fs.StringVar(&req.Kind, "kind", "", "task kind for the history")
	fs.IntVar(&req.WaitS, "wait", 0, "seconds before returning still_running")
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
	// No Shutdown here: a run that is paused on a permission request or still running stays alive for a
	// later answer or wait. Ctrl-C still interrupts the run being waited on.
	e := relay.NewEngine(cfg, version, os.Stderr)
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
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", " ")
	return enc.Encode(res)
}
