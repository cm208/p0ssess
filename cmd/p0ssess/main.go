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
	"p0ssess/internal/proxy"
	"p0ssess/internal/relay"
)

var version = "0.1.0"

const usage = `p0ssess ` + "%s" + `

usage:
  p0ssess mcp                         run the relay MCP server on stdio
  p0ssess jail build [--tarball F]    verify the pinned OpenCode release and build the jail image
  p0ssess jail up --repo DIR          start (or verify) the jail for a repository
  p0ssess jail down --repo DIR        stop it (sessions are kept)
  p0ssess jail purge --repo DIR       stop it and delete its sessions and password
  p0ssess jail status [--repo DIR]    show jails
  p0ssess jail probe --repo DIR T...  connect to targets (host:port or https://URL) from inside the jail
  p0ssess jail unit                   print the systemd --user template unit
  p0ssess run --model M [--mode read|edit] [--cwd D] [--session S] [--attach F] TASK
  p0ssess wait|abort --session S      the relay tools, from the command line (JSON out)
  p0ssess answer --session S --request R --decision once|reject
  p0ssess proxy --listen ADDR         (inside the jail) OpenRouter forwarder; key from OPENROUTER_API_KEY
  p0ssess version
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, usage, version)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	switch os.Args[1] {
	case "mcp":
		err = relay.Serve(ctx, os.Stdin, os.Stdout, version)
	case "jail":
		err = jailCmd(ctx, os.Args[2:])
	case "proxy":
		err = proxyCmd(os.Args[2:])
	case "run", "wait", "abort", "answer":
		err = runCmd(ctx, os.Args[1], os.Args[2:])
	case "version", "--version":
		fmt.Println("p0ssess", version, "opencode", jail.OpenCodeVersion)
	default:
		fmt.Fprintf(os.Stderr, usage, version)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "p0ssess:", err)
		os.Exit(1)
	}
}

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
