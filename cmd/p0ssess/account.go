package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/mail"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"p0ssess/internal/account"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/proc"
)

// Replaced in tests.
var (
	openBrowser = func(url string) error {
		switch runtime.GOOS {
		case "darwin":
			return exec.Command("open", url).Start()
		case "windows":
			return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
		default:
			return exec.Command("xdg-open", url).Start()
		}
	}
	startPoller = func(d paths.Dirs) error {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		logf, err := os.OpenFile(account.PollLog(d), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			os.MkdirAll(d.State, 0o700)
			if logf, err = os.OpenFile(account.PollLog(d), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600); err != nil {
				return err
			}
		}
		defer logf.Close()
		cmd := exec.Command(exe, "account", "poll")
		cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, logf, logf
		if err := proc.StartDetached(cmd); err != nil {
			return err
		}
		return cmd.Process.Release()
	}
)

func accountCmd(ctx context.Context, args []string) error {
	return runAccount(ctx, args, os.Stdout)
}

func runAccount(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: p0ssess account pro | link | status | notify --email E | signout")
	}
	d := paths.Default()
	c := account.NewClient(version)
	switch args[0] {
	case "pro":
		return accountPro(ctx, d, c, out)
	case "link":
		return accountLink(ctx, d, c, out)
	case "poll":
		return account.Poll(ctx, d, c)
	case "status":
		return accountStatus(ctx, d, c, out)
	case "notify":
		return accountNotify(ctx, c, args[1:], out)
	case "signout":
		return accountSignout(ctx, d, c, out)
	}
	return fmt.Errorf("account: unknown subcommand %q", args[0])
}

func planLine(p account.Plan) string {
	name := strings.ToUpper(p.Tier[:1]) + p.Tier[1:]
	if p.Interval != "" {
		name += ", " + map[string]string{"year": "yearly", "month": "monthly"}[p.Interval]
	}
	if !p.OK() {
		return strings.ToUpper(p.Tier[:1]) + p.Tier[1:] + ": coming in a later release"
	}
	line := fmt.Sprintf("%s: %s", name, p.Price.Display)
	if p.PerSeat {
		line += " per seat"
	}
	if p.Placeholder {
		line += " (placeholder)"
	}
	if p.Recommended {
		line += "  <- recommended"
	}
	return line
}

func accountPro(ctx context.Context, d paths.Dirs, c *account.Client, out io.Writer) error {
	if account.Token(d) != "" {
		rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		s, _ := account.Refresh(rctx, d, c)
		cancel()
		if account.Current(d).Pro() {
			fmt.Fprintf(out, "You are on p0ssess %s.\n", titleCase(s.Plan))
			if s.Manage != "" {
				fmt.Fprintf(out, "Manage: %s\n", s.Manage)
			}
			return nil
		}
	}
	offer, err := c.Offer(ctx)
	if err != nil {
		return fmt.Errorf("could not reach p0ssess cloud (%v); try again in a minute", err)
	}
	fmt.Fprintln(out, "p0ssess Pro")
	for _, p := range offer.Plans {
		if p.Tier == "" {
			continue
		}
		fmt.Fprintln(out, " ", planLine(p))
	}
	if offer.Checkout != "open" {
		fmt.Fprintln(out, "Opening soon. Tell Claude an email to get notified.")
		return nil
	}
	if err := startDeviceLink(ctx, d, c, out); err != nil {
		return err
	}
	fmt.Fprintln(out, "Finish in the browser; Pro turns on here by itself.")
	return nil
}

// startDeviceLink starts (or reuses) a device link and its background poller, and shows the page and code.
func startDeviceLink(ctx context.Context, d paths.Dirs, c *account.Client, out io.Writer) error {
	l, ok := account.PendingLink(d)
	if !ok || !account.PollerRunning(d) {
		var err error
		if l, err = account.StartLink(ctx, d, c); err != nil {
			return fmt.Errorf("could not start the link (%v); try again in a minute", err)
		}
		if err := startPoller(d); err != nil {
			return fmt.Errorf("could not start the background link check: %v", err)
		}
	}
	if err := openBrowser(l.URL); err != nil {
		fmt.Fprintf(out, "Open this page: %s\n", l.URL)
	}
	fmt.Fprintf(out, "Code: %s\n", l.UserCode)
	return nil
}

// accountLink links this install to an account whatever the checkout says, for an account that already
// has a plan, such as a seat in a team.
func accountLink(ctx context.Context, d paths.Dirs, c *account.Client, out io.Writer) error {
	if account.Token(d) != "" {
		fmt.Fprintln(out, "This install is already linked. p0ssess account signout unlinks it.")
		return nil
	}
	if err := startDeviceLink(ctx, d, c, out); err != nil {
		return err
	}
	fmt.Fprintln(out, "Sign in and confirm the code in the browser; this install links by itself.")
	return nil
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func accountStatus(ctx context.Context, d paths.Dirs, c *account.Client, out io.Writer) error {
	if account.Token(d) != "" {
		rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		account.Refresh(rctx, d, c)
		cancel()
	}
	s := account.Current(d)
	if !s.Pro() {
		if l, ok := account.PendingLink(d); ok {
			fmt.Fprintf(out, "linking: waiting for the browser, code %s\n", l.UserCode)
			return nil
		}
		line := "p0ssess Free"
		if s.Status == "lapsed" {
			line += " (Pro ended" + dateSuffix(" on ", s.EndsAt) + ")"
		}
		fmt.Fprintln(out, line)
		if s.Status == "lapsed" && s.Renew != "" {
			fmt.Fprintln(out, "Renew:", s.Renew)
		}
		return nil
	}
	plan := "p0ssess " + titleCase(s.Plan)
	if s.Interval != "" {
		plan += " (" + map[string]string{"year": "yearly", "month": "monthly"}[s.Interval] + ")"
	}
	switch s.Status {
	case "canceling":
		fmt.Fprintf(out, "%s, canceled; ends%s\n", plan, dateSuffix(" ", s.EndsAt))
	case "past_due":
		fmt.Fprintf(out, "%s, last payment failed; the provider is retrying\n", plan)
	default:
		fmt.Fprintf(out, "%s, %s%s\n", plan, s.Status, dateSuffix("; renews ", s.RenewsAt))
	}
	if s.Manage != "" {
		fmt.Fprintln(out, "Manage:", s.Manage)
	}
	return nil
}

func dateSuffix(prefix, rfc string) string {
	t, err := time.Parse(time.RFC3339, rfc)
	if err != nil {
		return ""
	}
	return prefix + t.UTC().Format("Jan 2, 2006")
}

func accountNotify(ctx context.Context, c *account.Client, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("account notify", flag.ContinueOnError)
	email := fs.String("email", "", "email address to notify")
	plan := fs.String("plan", "pro", "plan to wait for")
	if err := fs.Parse(args); err != nil {
		return err
	}
	addr, err := mail.ParseAddress(strings.TrimSpace(*email))
	if err != nil || strings.ContainsAny(*email, " \t\r\n<>") {
		return errors.New("that does not look like an email address")
	}
	if err := c.Waitlist(ctx, addr.Address, *plan); err != nil {
		return fmt.Errorf("could not reach p0ssess cloud (%v); try again in a minute", err)
	}
	fmt.Fprintf(out, "Noted: %s will get one email when p0ssess %s opens.\n", addr.Address, titleCase(*plan))
	return nil
}

func accountSignout(ctx context.Context, d paths.Dirs, c *account.Client, out io.Writer) error {
	tok := account.Token(d)
	if tok == "" {
		fmt.Fprintln(out, "This install is not linked to an account.")
		account.DeleteToken(d)
		return nil
	}
	err := c.RevokeDevice(ctx, tok)
	account.DeleteToken(d)
	if err != nil && !account.IsInvalidToken(err) {
		fmt.Fprintln(out, "Signed out here. The server could not be reached to revoke the token; revoke this device from your account page.")
		return nil
	}
	fmt.Fprintln(out, "Signed out. This install is on the free plan.")
	return nil
}
