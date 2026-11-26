package factory

import (
	"strings"
	"testing"
)

func TestReviewLine(t *testing.T) {
	s := newTestStore(t)
	it := addItem(s, "Review cmd")
	s.putDoc("spec", "## Acceptance criteria\n- **AC1**: one\n- **AC2**: two", it.ID, 0)
	s.exec("INSERT INTO tasks(item_id,n,title,covers,fixes) VALUES(?,1,'a','AC1',2)", it.ID)
	s.exec("INSERT INTO tasks(item_id,n,title,covers,fixes) VALUES(?,2,'b','AC2',0)", it.ID)
	s.exec("INSERT INTO agent_runs(ts,item_id,label,agent,secs,cost_usd) VALUES(1000,?,'build','b',60,1.25)", it.ID)
	s.exec("INSERT INTO agent_runs(ts,item_id,label,agent,secs,cost_usd) VALUES(1600,?,'judge','j',30,0.50)", it.ID)
	got := reviewLine(s, it, "abcdef1234567890")
	for _, want := range []string{"abcdef12", "Review cmd", fid(it.ID), "2 ACs", "2 tasks", "2 fix rounds", "2 runs", "$1.75"} {
		if !strings.Contains(got, want) {
			t.Errorf("reviewLine missing %q: %s", want, got)
		}
	}
	// wall time runs from the first run's start (1000-60) to the given end
	runs, cost, mins := itemUsage(s, it.ID, 1660)
	if runs != 2 || cost != 1.75 || mins != 12 {
		t.Errorf("itemUsage = %d %.2f %.0f", runs, cost, mins)
	}
}
