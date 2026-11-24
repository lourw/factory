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

func TestEarlierTasks(t *testing.T) {
	s := newTestStore(t)
	it := addItem(s, "Handover")
	s.putDoc("spec", "## Acceptance criteria\n- **AC1**: one", it.ID, 0)
	s.exec("INSERT INTO tasks(item_id,n,title,covers,status,sha) VALUES(?,1,'first','AC1','verified','abcdef0123456789')", it.ID)
	s.exec("INSERT INTO tasks(item_id,n,title,covers,deps) VALUES(?,2,'second','AC1','T1')", it.ID)
	s.putDoc("task", "## Goal\nDo one.", it.ID, 1)
	s.putDoc("task", "## Goal\nDo two.", it.ID, 2)
	s.putDoc("report", "Status: done\nGate: PASS\n## Changed\n- `a.go`: added Foo\n- `b.go`: x\n- `c.go`: y\n\n## Tests\n- `TestFoo`: z\n"+
		"## Follow-ups\n- `path`: what\n- f1\n- f2\n- f3", it.ID, 1)
	t.Setenv("FACTORY_BACKEND", "claude")
	got := briefBuild(s, it, s.task(it, "T2"), false)
	for _, w := range []string{"## Earlier tasks", "T1 first (abcdef01)", "  - `a.go`: added Foo", "  - f3"} {
		if !strings.Contains(got, w) {
			t.Errorf("claude brief missing %q:\n%s", w, got)
		}
	}
	sec, _, _ := strings.Cut(got[strings.Index(got, "## Earlier tasks"):], "## Packet")
	if strings.Contains(sec, "TestFoo") || strings.Contains(sec, "`path`: what") || strings.Contains(got, "Dependencies") {
		t.Errorf("brief has Tests/placeholder/Dependencies lines:\n%s", got)
	}
	t.Setenv("FACTORY_BACKEND", "opencode")
	got = briefBuild(s, it, s.task(it, "T2"), false)
	if !strings.Contains(got, "added Foo") || strings.Contains(got, "f2") {
		t.Errorf("opencode cap wrong:\n%s", got)
	}
	if b := briefBuild(s, it, s.task(it, "T1"), false); strings.Contains(b, "Earlier tasks") {
		t.Error("T1 brief has Earlier tasks")
	}
	s.exec("UPDATE tasks SET status='todo' WHERE item_id=? AND n=1", it.ID)
	if got = briefBuild(s, it, s.task(it, "T2"), false); !strings.Contains(got, "T1: todo") {
		t.Errorf("unfinished dep not listed:\n%s", got)
	}
}
