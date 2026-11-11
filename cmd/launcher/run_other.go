//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

// run replaces the launcher with bin, as the shell launcher does.
func run(bin string, args []string) int {
	err := syscall.Exec(bin, append([]string{bin}, args...), os.Environ())
	fmt.Fprintln(os.Stderr, "p0ssess:", err)
	return 1
}
