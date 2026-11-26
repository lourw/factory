package factory

import (
	"strings"
	"testing"
)

// Golden lengths for the fixture below with FACTORY_BACKEND=opencode, minus the
// temp-dir-dependent worktree line, measured on main before prompt pass 2.
const (
	pass2OldBuildLen  = 1170
	pass2OldVerifyLen = 1077
)

func TestBriefPass2(t *testing.T) {
	t.Setenv("FACTORY_BACKEND", "opencode")
	s := newTestStore(t)
	it := addItem(s, "Trim")
	s.putDoc("spec", "## Acceptance criteria\n- **AC1**: one", it.ID, 0)
	s.exec("INSERT INTO tasks(item_id,n,title,covers,sha) VALUES(?,1,'a','AC1',?)", it.ID, strings.Repeat("a", 40))
	s.putDoc("task", "## Goal\nDo a thing.", it.ID, 1)
	wl := len(worktreeLine(s, it))
	build := briefBuild(s, it, s.task(it, "T1"), false)
	verify := briefVerify(s, it, s.task(it, "T1"), false)
	nb, nv := len(build)-wl, len(verify)-wl
	t.Logf("build %d verify %d", nb, nv)
	if strings.Contains(build, "interrupted attempt") {
		t.Error("clean worktree: build brief mentions interrupted attempt")
	}
	if strings.Contains(verify, strings.Repeat("a", 13)) {
		t.Error("verify brief uses the full SHA")
	}
	if float64(nb) > 0.92*pass2OldBuildLen {
		t.Errorf("build brief %d bytes, want ≤92%% of %d", nb, pass2OldBuildLen)
	}
	if float64(nv) > 0.92*pass2OldVerifyLen {
		t.Errorf("verify brief %d bytes, want ≤92%% of %d", nv, pass2OldVerifyLen)
	}
	for _, b := range []string{"opencode", "claude"} {
		t.Setenv("FACTORY_BACKEND", b)
		if r := budgetFor(s).Reading; len(r) > 90 {
			t.Errorf("%s reading text %d chars, want ≤90", b, len(r))
		}
	}
	src, err := opencodeFS.ReadFile("agents/factory-overseer.md")
	if err != nil || !strings.Contains(string(src), "\n\n## Jobs") {
		t.Error("overseer prompt lacks blank line before ## Jobs")
	}
}
