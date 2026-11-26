package factory

import "os"

// budget is the context budget for agents, chosen by backend.
type budget struct {
	Backend     string
	TaskLines   int    // target changed lines per task
	Reading     string // reading rule injected into briefs
	ReportLines int    // per-task report lines in a brief's Earlier tasks
	InlineLines int    // Read-first code lines inlined into a build brief
}

// budgetFor returns the profile for the effective backend:
// env FACTORY_BACKEND, else config backend, else opencode.
func budgetFor(s *Store) budget {
	b := os.Getenv("FACTORY_BACKEND")
	if b == "" {
		b = s.cfg("backend", "opencode")
	}
	if b == "claude" {
		return budget{b, 400, "large: read whole files if useful; stay in scope", 8, 400}
	}
	return budget{"opencode", 150, "small: packet refs only; else `rg -n` + ~60-line windows; no whole files >300 lines", 4, 120}
}
