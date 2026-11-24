package main

import (
	"strings"
	"testing"
)

func TestIntegrationPacket(t *testing.T) {
	p := integrationPacket("feat: a\nfix: b", " x.go | 3 ++-", "GATE: FAIL (test)\nFAIL test")
	for _, want := range []string{"## Goal", "## What came in", "feat: a", "fix: b", "x.go | 3", "## Gate failures", "GATE: FAIL (test)", "## Do", "don't revert", "## Verify", "factory gate"} {
		if !strings.Contains(p, want) {
			t.Errorf("packet missing %q:\n%s", want, p)
		}
	}
	if !isIntegrationTask(Task{Title: integrationTitle}) || isIntegrationTask(Task{Title: "other"}) {
		t.Error("isIntegrationTask")
	}
}
