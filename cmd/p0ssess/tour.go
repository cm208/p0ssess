package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"p0ssess/internal/account"
	"p0ssess/internal/config"
	"p0ssess/internal/direct"
	"p0ssess/internal/pme/local"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/server"
	"p0ssess/internal/tour"
)

// tourCmd is `p0ssess tour [show|envelope|demo|skip|done]`, what /p0ssess:tour runs.
func tourCmd(ctx context.Context, args []string) error {
	d := paths.Default()
	sub := "show"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "show":
		f, err := tourFacts(d)
		if err != nil {
			return err
		}
		fmt.Print(tour.Show(f))
		return nil
	case "envelope":
		return tourEnvelope(d, args)
	case "demo":
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		out, err := tour.Demo(ctx, demoRunner(cfg, d), keyReady(cfg), cfg.OpenRouterKeyFile)
		if err != nil {
			return errors.New("the demo did not run: " + err.Error())
		}
		fmt.Print(out)
		return nil
	case "skip":
		return tour.Set(d, tour.Skipped, time.Now())
	case "done":
		return tour.Set(d, tour.Done, time.Now())
	}
	return fmt.Errorf("tour: unknown step %q (show, envelope, demo, skip, done)", sub)
}

func tourFacts(d paths.Dirs) (tour.Facts, error) {
	b, err := local.LoadBudget(d)
	if err != nil {
		return tour.Facts{}, err
	}
	f := tour.Facts{Envelope: tour.Envelope{Daily: b.DailyUSD, Monthly: b.MonthlyUSD, Task: b.PerTaskUSD}, New: !tour.Existing(d)}
	if cfg, err := config.Load(); err == nil {
		f.KeyReady, f.KeyFile = keyReady(cfg), cfg.OpenRouterKeyFile
	}
	if o, err := readObject(settingsPath()); err == nil {
		sl, _ := o.get("statusLine")
		f.StatusLine = strings.Contains(string(sl), "p0ssess")
	}
	a := account.Current(d)
	f.Team = a.OrgID != "" || a.Plan == "team"
	return f, nil
}

// keyReady reports whether the OpenRouter key file is there; it is never read here.
func keyReady(cfg config.Config) bool {
	st, err := os.Stat(cfg.OpenRouterKeyFile)
	return err == nil && st.Size() > 0
}

func tourEnvelope(d paths.Dirs, args []string) error {
	fs := flag.NewFlagSet("tour envelope", flag.ContinueOnError)
	daily := fs.String("daily", "", "daily limit, US$")
	monthly := fs.String("monthly", "", "monthly cap, US$")
	task := fs.String("task", "", "auto-approve per task, US$")
	if err := fs.Parse(args); err != nil {
		return err
	}
	eng, err := local.Load(d)
	if err != nil {
		return err
	}
	for _, kv := range [][2]string{{"monthly_usd", *monthly}, {"daily_usd", *daily}, {"per_task_usd", *task}} {
		if kv[1] == "" {
			continue
		}
		if _, err := eng.SetSetting(kv[0], kv[1]); err != nil {
			return err
		}
	}
	b, err := eng.Budget()
	if err != nil {
		return err
	}
	fmt.Println("saved    " + tour.Envelope{Daily: b.DailyUSD, Monthly: b.MonthlyUSD, Task: b.PerTaskUSD}.Line())
	return nil
}

// demoRunner runs the tour's sample task on the direct path, under every limit, capped at maxUSD.
func demoRunner(cfg config.Config, d paths.Dirs) tour.Runner {
	return func(ctx context.Context, task, useCase string, maxUSD float64) (tour.DemoResult, error) {
		eng, err := local.Load(d)
		if err != nil {
			return tour.DemoResult{}, err
		}
		ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		res, runID, err := server.RunDirect(ctx, cfg, eng, direct.Request{Prompt: task, MaxCostUSD: maxUSD, MaxGiven: true},
			server.DirectJob{UseCase: useCase, Stakes: 1})
		if err != nil {
			return tour.DemoResult{}, err
		}
		return tour.DemoResult{Answer: res.Text, Receipt: server.DirectReceipt(eng, res, runID), CostUSD: server.DirectCost(res)}, nil
	}
}
