package main

import (
	"strings"
	"testing"
)

// oldBuildBriefLen is len(briefBuild) for the fixture below, minus the
// temp-dir-dependent worktree line, measured on main before the trim.
const oldBuildBriefLen = 1446

func TestBriefTrim(t *testing.T) {
	t.Setenv("FACTORY_BACKEND", "opencode")
	s := newTestStore(t)
	it := addItem(s, "Trim")
	s.putDoc("spec", "## Acceptance criteria\n- **AC1**: one", it.ID, 0)
	s.exec("INSERT INTO tasks(item_id,n,title,covers) VALUES(?,1,'a','AC1')", it.ID)
	s.putDoc("task", "## Goal\nDo a thing.", it.ID, 1)
	got := briefBuild(s, it, s.task(it, "T1"), false)
	n := len(got) - len(worktreeLine(s, it))
	t.Logf("normalized len: %d", n)
	for _, bad := range []string{"(none)", "Don't commit", "Never run raw"} {
		if strings.Contains(got, bad) {
			t.Errorf("brief contains %q", bad)
		}
	}
	if float64(n) > 0.85*float64(oldBuildBriefLen) {
		t.Errorf("brief %d bytes, want ≤85%% of %d", n, oldBuildBriefLen)
	}
	if lines := strings.Split(strings.TrimSpace(tpl("report")), "\n"); len(lines) > 12 {
		t.Errorf("report template has %d lines, want ≤12", len(lines))
	}
	for _, w := range []string{"Status", "Gate", "Changed", "Tests", "Follow-ups", "Blocker"} {
		if !strings.Contains(tpl("report"), w) {
			t.Errorf("report template missing %q", w)
		}
	}
	if section("X", "") != "## X\n(none)" {
		t.Error("section() behavior changed")
	}
}
