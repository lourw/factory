// Package factory holds the files embedded into the factory binary. The CLI itself
// lives in internal/factory, with its entry point in cmd/factory.
package factory

import "embed"

// Templates holds the doc templates (templates/*.md).
//
//go:embed templates/*.md
var Templates embed.FS

// Opencode holds the opencode agents, commands and skills installed by `factory internal install`.
//
//go:embed agents commands skills
var Opencode embed.FS
