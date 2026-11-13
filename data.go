// Package p0ssess holds the files the binary ships with.
package p0ssess

import "embed"

// Data holds the logo.
//
//go:embed assets/ghost.svg assets/ghost-amber.svg
var Data embed.FS
