package main

import (
	"flag"
	"fmt"
	"regexp"
	"time"

	"p0ssess/internal/notices"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/savings"
)

var monthRe = regexp.MustCompile(`^[0-9]{4}-(0[1-9]|1[0-2])$`)

// savingsCmd prints the savings card for a month, from the receipts on this machine.
func savingsCmd(args []string) error {
	fs := flag.NewFlagSet("savings", flag.ContinueOnError)
	month := fs.String("month", "", "month as YYYY-MM (default: this month)")
	all := fs.Bool("all", false, "all time")
	if err := fs.Parse(args); err != nil {
		return err
	}
	now := time.Now()
	m := *month
	switch {
	case *all:
		m = ""
	case m == "":
		m = savings.Month(now, now.Location())
	case !monthRe.MatchString(m):
		return fmt.Errorf("--month wants YYYY-MM, such as %s", savings.Month(now, now.Location()))
	}
	recs := notices.Receipts(paths.Default().NoticesFile())
	fmt.Print(savings.Card(savings.Roll(recs, m, now.Location())))
	return nil
}
