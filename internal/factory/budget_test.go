package factory

import (
	"strings"
	"testing"
)

func TestBudgetPlanBrief(t *testing.T) {
	s := newTestStore(t)
	it := addItem(s, "Budgets")
	s.putDoc("spec", "## Acceptance criteria\n- **AC1**: one", it.ID, 0)
	s.exec("INSERT INTO tasks(item_id,n,title,covers) VALUES(?,1,'a','AC1')", it.ID)
	for _, c := range []struct{ backend, size, reading string }{
		{"claude", "≤400", "large: "},
		{"opencode", "≤150", "small: "},
	} {
		t.Setenv("FACTORY_BACKEND", c.backend)
		got := briefPlan(s, it)
		if !strings.Contains(got, c.size) || !strings.Contains(got, "Budget: "+c.reading) {
			t.Fatalf("%s: plan brief missing %q or %q:\n%s", c.backend, c.size, c.reading, got)
		}
	}
	t.Setenv("FACTORY_BACKEND", "")
	if b := budgetFor(s); b.Backend != "opencode" || b.TaskLines != 150 {
		t.Fatalf("default: %+v", b)
	}
	s.setCfg("backend", "claude")
	if b := budgetFor(s); b.Backend != "claude" || b.TaskLines != 400 {
		t.Fatalf("config: %+v", b)
	}
}

func TestAgentPromptsNoBudget(t *testing.T) {
	ents, err := opencodeFS.ReadDir("agents")
	if err != nil || len(ents) == 0 {
		t.Fatalf("no embedded agents: %v", err)
	}
	for _, e := range ents {
		b, err := opencodeFS.ReadFile("agents/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		for _, bad := range []string{"131k", "300 lines"} {
			if strings.Contains(s, bad) {
				t.Errorf("%s contains %q", e.Name(), bad)
			}
		}
		if !strings.Contains(s, "Budget") {
			t.Errorf("%s does not point to the brief's Budget line", e.Name())
		}
	}
}
