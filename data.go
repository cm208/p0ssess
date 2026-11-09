// Package p0ssess holds the data files the engine ships with.
package p0ssess

import "embed"

// Data holds the catalog, the registry snapshot, the benchmark files and the logo.
//
//go:embed catalog/usecases.yaml registry/models.json registry/benchmarks/*.json assets/ghost.svg assets/ghost-amber.svg
var Data embed.FS
