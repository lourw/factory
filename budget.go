package main

import "os"

// budget is the context budget for agents, chosen by backend.
type budget struct {
	Backend   string
	TaskLines int    // target changed lines per task
	Reading   string // reading rule injected into briefs
}

// budgetFor returns the profile for the effective backend:
// env FACTORY_BACKEND, else config backend, else opencode.
func budgetFor(s *Store) budget {
	b := os.Getenv("FACTORY_BACKEND")
	if b == "" {
		b = s.cfg("backend", "opencode")
	}
	if b == "claude" {
		return budget{b, 400, "Large context: read whole files when it helps; stay in the task's scope."}
	}
	return budget{"opencode", 150, "Small context: read only the packet's refs (line ranges); for anything else `rg -n` then ~60-line windows; never read a whole file over 300 lines."}
}
