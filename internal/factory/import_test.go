package factory

import "testing"

func TestImportPlan(t *testing.T) {
	s := newTestStore(t)
	src := `title: Rate limit API
kind: fix
scope: api
prio: 1

=== spec
## Acceptance criteria
- **AC1**: a regression test reproduces the bug
- **AC2**: two

=== T1: Limiter core [covers AC1]
## Goal
core
=== T2: Wire it [covers AC2] [deps T1]
## Goal
wire
`
	p, err := parsePlan(src)
	if err != nil {
		t.Fatal(err)
	}
	id := importPlan(s, p)
	it := s.item(fid(id))
	if it.Title != "Rate limit API" || it.Kind != "fix" || it.Scope != "api" || it.Priority != 1 || it.Slug != "rate-limit-api" || it.Status != "backlog" {
		t.Fatalf("item: %+v", it)
	}
	if spec := s.getDoc("spec", id, 0); spec != "## Acceptance criteria\n- **AC1**: a regression test reproduces the bug\n- **AC2**: two" {
		t.Fatalf("spec: %q", spec)
	}
	ts := s.tasks(it)
	if len(ts) != 2 || ts[0].Title != "Limiter core" || ts[0].Covers != "AC1" || ts[0].Deps != "" ||
		ts[1].Title != "Wire it" || ts[1].Covers != "AC2" || ts[1].Deps != "T1" {
		t.Fatalf("tasks: %+v", ts)
	}
	if a, b := s.getDoc("task", id, 1), s.getDoc("task", id, 2); a != "## Goal\ncore" || b != "## Goal\nwire" {
		t.Fatalf("packets: %q %q", a, b)
	}
	if pr := readyProblems(s, it); len(pr) != 0 {
		t.Fatalf("should be ready: %v", pr)
	}

	for name, bad := range map[string]string{
		"no title":    "=== spec\nx\n=== T1: a\nb",
		"no spec":     "title: x\n=== T1: a\nb",
		"gap":         "title: x\n=== spec\ns\n=== T2: a\nb",
		"unknown key": "title: x\nfoo: y\n=== spec\ns",
		"bad prio":    "title: x\nprio: 9\n=== spec\ns",
		"bad kind":    "title: x\nkind: nope\n=== spec\ns",
		"bad dep":     "title: x\n=== spec\ns\n=== T1: a [deps T2]\nb",
		"bad block":   "title: x\n=== spec\ns\n=== notes\nb",
	} {
		if _, err := parsePlan(bad); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	var n int
	check(s.db.QueryRow("SELECT count(*) FROM items").Scan(&n))
	if n != 1 {
		t.Fatalf("malformed files created items: %d", n)
	}
}
