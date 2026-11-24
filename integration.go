package main

import (
	"fmt"
	"strings"
)

const integrationTitle = "Integrate with factory: fix gate failures"

func isIntegrationTask(t Task) bool { return strings.HasPrefix(t.Title, "Integrate with factory") }

// integrationPacket is the packet for the task added when the gate fails after merging factory.
func integrationPacket(subjects, stat, summary string) string {
	return joinParts([]string{
		"## Goal\nThe gate fails after merging the latest factory into this feature. Make the feature work together with what came in.",
		"## What came in\n" + orDash(strings.TrimSpace(subjects)) + "\n\n" + strings.TrimSpace(stat),
		"## Gate failures\n" + strings.TrimSpace(summary),
		"## Do\n- Fix the feature so the gate passes with these changes.\n- Keep these changes: don't revert them; keep the feature's ACs working.",
		"## Verify\n- `factory gate`",
	})
}

// gateFailedAfterMerge turns a post-merge gate failure into one integration
// task; if one already exists, the item parks.
func (r *runner) gateFailedAfterMerge(it Item, wt, base, summary string) {
	n := 0
	for _, t := range r.s.tasks(it) {
		if isIntegrationTask(t) {
			r.s.park(it, fmt.Sprintf("gate still failing after integration task T%d", t.N))
			r.log("%s parked: gate still failing after integration task T%d\n%s", tag(it), t.N, summary)
			return
		}
		n = max(n, t.N)
	}
	n++
	var subjects, stat string
	if base != "" {
		subjects, _ = gitRun(wt, "log", "--format=%s", base+".."+integrationBranch)
		stat, _ = gitRun(wt, "diff", "--stat=120", base+".."+integrationBranch)
	}
	body := integrationPacket(capLines(strings.TrimSpace(subjects), 15), capLines(strings.TrimSpace(stat), 15), summary)
	r.s.exec("INSERT INTO tasks(item_id,n,title,covers,deps,updated) VALUES(?,?,?,?,?,?)",
		it.ID, n, integrationTitle, "", "", now())
	r.s.putDoc("task", body, it.ID, n)
	if len(parseRefs(body)) > 0 {
		r.s.storeRefs(it.ID, n, body)
	}
	r.s.event(it.ID, "task", fmt.Sprintf("integration task T%d added", n), n)
	r.s.setStatus(r.reload(it), "building", "integration task added")
	r.log("%s gate failed after merging factory: integration task T%d added\n%s", tag(it), n, summary)
}
