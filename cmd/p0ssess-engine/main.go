// Command p0ssess-engine is the Predictive Model Engine: routing advice for
// Claude, the status line wrapper, the prompt hook, an MCP server and the
// local settings page.
package main

import (
	"os"

	"p0ssess/internal/pme/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], cli.IO{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}))
}
