package main

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// resolveDoc maps (kind, item, task) to DB keys, validating which args each kind needs.
func resolveDoc(s *Store, kind, itemRef, taskRef string) (int, int) {
	switch {
	case projectDocs[kind]:
		return 0, 0
	case itemDocs[kind] || kind == "plan":
		if itemRef == "" {
			die("`%s` needs an item", kind)
		}
		return s.item(itemRef).ID, 0
	case taskDocs[kind]:
		if itemRef == "" || taskRef == "" {
			die("`%s` needs an item and a task", kind)
		}
		it := s.item(itemRef)
		return it.ID, s.task(it, taskRef).N
	}
	die("unknown doc kind '%s' (project: map lessons | item: spec plan notes merge-msg | task: task report verify)", kind)
	return 0, 0
}

func header(s *Store, kind string, itemID, n int) string {
	if projectDocs[kind] {
		return "# " + kind + "\n\n"
	}
	it := s.item(strconv.Itoa(itemID))
	if taskDocs[kind] {
		t := s.task(it, strconv.Itoa(n))
		meta := fmt.Sprintf("status: %s | covers: %s | deps: %s", t.Status, orDash(t.Covers), orDash(t.Deps))
		if t.Sha != "" {
			meta += " | sha: " + t.Sha
		}
		title := map[string]string{"task": "", "report": "Report ", "verify": "Verify "}[kind]
		return fmt.Sprintf("# %s%s T%d: %s\n%s\n\n", title, fid(it.ID), t.N, t.Title, meta)
	}
	return fmt.Sprintf("# %s %s: %s\nstatus: %s | %s | p%d\n\n", kind, fid(it.ID), it.Title, it.Status, it.Kind, it.Priority)
}

// templateName picks the body template for a doc; fix items use the bug template for their spec.
func templateName(s *Store, kind string, itemID int) string {
	if kind == "spec" && itemID != 0 && s.item(strconv.Itoa(itemID)).Kind == "fix" {
		return "bug"
	}
	return templateFor[kind]
}

func renderDoc(s *Store, kind string, itemID, n int, raw bool) string {
	body := s.getDoc(kind, itemID, n)
	if body == "" {
		if t := tpl(templateName(s, kind, itemID)); t != "" {
			body = "(empty; template follows)\n\n" + t
		} else {
			body = "(empty)"
		}
	}
	if raw {
		return body
	}
	return header(s, kind, itemID, n) + body
}

func renderPlan(s *Store, it Item) string {
	out := []string{fmt.Sprintf("# plan %s: %s", fid(it.ID), it.Title),
		fmt.Sprintf("status: %s | branch: %s", it.Status, branchName(it)), ""}
	ts := s.tasks(it)
	covered := map[string]bool{}
	for _, t := range ts {
		for _, c := range splitList(t.Covers) {
			covered[c] = true
		}
		out = append(out, fmt.Sprintf("- %s T%d: %s | covers %s | deps %s", mark[t.Status], t.N, t.Title, orDash(t.Covers), orDash(t.Deps)))
	}
	var missing []string
	for _, ac := range parseACs(s.getDoc("spec", it.ID, 0)) {
		if !covered[ac.ID] {
			missing = append(missing, ac.ID)
		}
	}
	cov := "complete"
	if len(missing) > 0 {
		cov = "MISSING " + strings.Join(missing, ", ")
	}
	out = append(out, "", "AC coverage: "+cov, "")
	for _, t := range ts {
		packet := s.getDoc("task", it.ID, t.N)
		if packet == "" {
			packet = "(no packet)"
		}
		out = append(out, "---", strings.TrimRight(header(s, "task", it.ID, t.N), "\n"), "", packet, "")
	}
	return strings.Join(out, "\n")
}

func cmdCat(a Args) {
	s := cur()
	kind := a.need(0, "kind")
	if kind == "plan" {
		fmt.Println(renderPlan(s, s.item(a.need(1, "item"))))
		return
	}
	itemID, n := resolveDoc(s, kind, a.pos(1), a.pos(2))
	fmt.Println(renderDoc(s, kind, itemID, n, a.Bools["raw"]))
}

func cmdWrite(a Args) {
	s := cur()
	kind := a.need(0, "kind")
	if kind == "plan" {
		die("the plan is the task list: use task-add / task-set, and `write task <item> <task>` for packets")
	}
	itemID, n := resolveDoc(s, kind, a.pos(1), a.pos(2))
	if !stdinPiped() {
		die("pipe the body on stdin, e.g. factory write spec 3 <<'EOF' ... EOF")
	}
	body := readStdin()
	if body == "" {
		die("empty body")
	}
	if a.Bools["append"] {
		if old := s.getDoc(kind, itemID, n); old != "" {
			body = strings.TrimSpace(old + "\n" + body)
		}
	}
	s.putDoc(kind, body, itemID, n)
	if kind == "task" {
		s.storeRefs(itemID, n, body)
	}
	if itemID != 0 {
		msg := "wrote " + kind
		if a.Bools["append"] {
			msg += " (append)"
		}
		s.event(itemID, "doc", msg, n)
	}
	fmt.Printf("saved %s (%d lines)\n", kind, len(strings.Split(body, "\n")))
}

func cmdEdit(a Args) {
	s := cur()
	kind := a.need(0, "kind")
	itemID, n := resolveDoc(s, kind, a.pos(1), a.pos(2))
	body := s.getDoc(kind, itemID, n)
	if body == "" {
		body = tpl(templateName(s, kind, itemID))
	}
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}
	f, err := os.CreateTemp("", "factory-*-"+kind+".md")
	check(err)
	path := f.Name()
	defer os.Remove(path)
	_, err = f.WriteString(body)
	check(err)
	f.Close()
	parts := strings.Fields(editor)
	cmd := exec.Command(parts[0], append(parts[1:], path)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	check(cmd.Run())
	b, err := os.ReadFile(path)
	check(err)
	updated := strings.TrimSpace(string(b))
	if updated == "" || updated == strings.TrimSpace(body) {
		fmt.Println("unchanged")
		return
	}
	s.putDoc(kind, updated, itemID, n)
	if kind == "task" {
		s.storeRefs(itemID, n, updated)
	}
	if itemID != 0 {
		s.event(itemID, "doc", "edited "+kind+" (human)", n)
	}
	fmt.Println("saved")
}

func cmdTemplate(a Args) {
	name := a.need(0, "name")
	t := tpl(name)
	if t == "" {
		entries, _ := templatesFS.ReadDir("templates")
		var have []string
		for _, e := range entries {
			have = append(have, strings.TrimSuffix(e.Name(), ".md"))
		}
		sort.Strings(have)
		die("no template '%s' (have: %s)", name, strings.Join(have, " "))
	}
	fmt.Println(t)
}

// ---------------------------------------------------------------- briefs

type gateRow struct {
	Name, Cmd    string
	KnownFailing bool
}

func (s *Store) gateRows() []gateRow {
	rows, err := s.db.Query("SELECT name, cmd, known_failing FROM gate ORDER BY ord, name")
	check(err)
	defer rows.Close()
	var out []gateRow
	for rows.Next() {
		var g gateRow
		var kf int
		check(rows.Scan(&g.Name, &g.Cmd, &kf))
		g.KnownFailing = kf != 0
		out = append(out, g)
	}
	return out
}

func verificationBlock(s *Store, it Item, n int) string {
	var names []string
	focused := false
	for _, g := range s.gateRows() {
		if g.Name == "test_focused" {
			focused = true
		}
		if !gateExcluded(g.Name) {
			names = append(names, g.Name)
		}
	}
	checks := strings.Join(names, ", ")
	if checks == "" {
		checks = "NONE CONFIGURED"
	}
	lines := []string{"## Verification (run from the worktree)"}
	if focused {
		lines = append(lines, "- focused tests: `factory test <test file or pattern>`")
	}
	lines = append(lines,
		fmt.Sprintf("- full gate: `factory gate --item %d --task %d`  (checks: %s)", it.ID, n, checks),
		"- Output is a compact summary; full logs are in .factory/logs/. Never run raw test/build commands.")
	return strings.Join(lines, "\n")
}

func acBlock(s *Store, it Item, covers string) string {
	acs := parseACs(s.getDoc("spec", it.ID, 0))
	text := map[string]string{}
	var wanted []string
	for _, ac := range acs {
		text[ac.ID] = ac.Text
		wanted = append(wanted, ac.ID)
	}
	if c := splitList(covers); len(c) > 0 {
		wanted = c
	}
	lines := []string{"## Acceptance criteria"}
	for _, id := range wanted {
		t, ok := text[id]
		if !ok {
			t = "(not found in spec!)"
		}
		lines = append(lines, fmt.Sprintf("- **%s**: %s", id, t))
	}
	return strings.Join(lines, "\n")
}

func section(title, body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		body = "(none)"
	}
	return "## " + title + "\n" + body
}

var commentRe = regexp.MustCompile(`(?s)<!--.*?-->\n?`)

func stripComments(s string) string { return strings.TrimSpace(commentRe.ReplaceAllString(s, "")) }

func indent(s, pre string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = pre + l
	}
	return strings.Join(lines, "\n")
}

func short(sha string) string { return sha[:min(12, len(sha))] }

func depLines(s *Store, it Item, t Task) string {
	var deps []string
	for _, d := range splitList(t.Deps) {
		dt, err := scanTask(s.db.QueryRow("SELECT "+taskCols+" FROM tasks WHERE item_id=? AND n=?", it.ID, taskN(d)))
		if err != nil {
			deps = append(deps, "- "+d+": missing")
			continue
		}
		line := fmt.Sprintf("- %s %s: %s", d, dt.Title, dt.Status)
		if dt.Sha != "" {
			line += " (" + dt.Sha[:8] + ")"
		}
		deps = append(deps, line)
	}
	return strings.Join(deps, "\n")
}

func worktreeLine(s *Store, it Item) string {
	return fmt.Sprintf("worktree: %s (branch %s). Work only inside it.", featureWorktree(s, it), branchName(it))
}

func briefBuild(s *Store, it Item, t Task, fix bool) string {
	packet := s.getDoc("task", it.ID, t.N)
	if packet == "" {
		die("T%d has no packet", t.N)
	}
	mode := "build"
	if fix {
		mode = fmt.Sprintf("FIX (round %d of 2)", max(t.Fixes, 1))
	}
	parts := []string{
		fmt.Sprintf("# Build brief: %s T%d: %s", fid(it.ID), t.N, t.Title),
		fmt.Sprintf("item: %s: %s | mode: %s\n%s\nIf `git status` shows uncommitted changes, they are from an interrupted attempt at this task: keep what's useful, discard the rest.",
			kindScope(it), it.Title, mode, worktreeLine(s, it)),
		acBlock(s, it, t.Covers),
		section("Dependencies (already committed in this worktree)", depLines(s, it, t)),
		section("Packet", packet),
	}
	if fix {
		parts = append(parts, section("Findings to fix (address every [blocking] item; add or strengthen a test for each)",
			s.getDoc("verify", it.ID, t.N)))
	}
	parts = append(parts,
		section("Project lessons", stripComments(s.getDoc("lessons", 0, 0))),
		verificationBlock(s, it, t.N),
		fmt.Sprintf("## When done\n1. Save your report (body only; the header is added for you):\n"+
			"   factory write report %d T%d <<'EOF'\n   ...\n   EOF\n   Report sections:\n%s\n"+
			"2. Don't commit; the scheduler re-runs the gate and commits.\n"+
			"3. End your reply with exactly:\n   STATUS: done | blocked | failed\n   TASK: T%d\n   NOTE: <one line>",
			it.ID, t.N, indent(stripComments(tpl("report")), "   "), t.N))
	return strings.Join(parts, "\n\n")
}

func briefVerify(s *Store, it Item, t Task, tests bool) string {
	if t.Sha == "" {
		die("T%d has no commit yet", t.N)
	}
	mode := "no: read from the commit only"
	verify := "## Verification\n- Don't run tests in this mode."
	if tests {
		mode = "yes (the builder is idle; the worktree is at this commit)"
		verify = verificationBlock(s, it, t.N)
	}
	sh := short(t.Sha)
	return strings.Join([]string{
		fmt.Sprintf("# Verify brief: %s T%d: %s", fid(it.ID), t.N, t.Title),
		fmt.Sprintf("commit: %s | tests: %s\n%s", t.Sha, mode, worktreeLine(s, it)),
		acBlock(s, it, t.Covers),
		section("Packet (what was asked)", s.getDoc("task", it.ID, t.N)),
		fmt.Sprintf("## How to inspect\n- `git show --stat %s`, then per file `git show %[1]s -- <path>`\n"+
			"- surrounding code: `git show %[1]s:<path> | sed -n 'A,Bp'` or a narrow `rg -n`", sh),
		verify,
		fmt.Sprintf("## When done\n1. factory write verify %d T%d <<'EOF'\n   ...\n   EOF\n   Sections:\n%s\n"+
			"2. End your reply with exactly:\n   VERDICT: PASS | FAIL\n   TASK: T%d\n   BLOCKING: <count>\n   TOP: <most important finding, or none>",
			it.ID, t.N, indent(stripComments(tpl("verify")), "   "), t.N),
	}, "\n\n")
}

func briefPlan(s *Store, it Item) string {
	var lines, missing []string
	for _, t := range s.tasks(it) {
		has := "packet ok"
		if s.getDoc("task", it.ID, t.N) == "" {
			has = "NEEDS PACKET"
			missing = append(missing, fmt.Sprintf("T%d", t.N))
		}
		lines = append(lines, fmt.Sprintf("- T%d %s | covers %s | deps %s | %s | %s", t.N, t.Title, orDash(t.Covers), orDash(t.Deps), t.Status, has))
	}
	return strings.Join([]string{
		fmt.Sprintf("# Plan brief: %s %s: %s", fid(it.ID), kindScope(it), it.Title),
		worktreeLine(s, it),
		section("Spec", s.getDoc("spec", it.ID, 0)),
		section("Tasks", strings.Join(lines, "\n")),
		section("Code map", stripComments(s.getDoc("map", 0, 0))),
		section("Project lessons", stripComments(s.getDoc("lessons", 0, 0))),
		"## Your job\n" +
			"Write a packet for every task marked NEEDS PACKET (" + strings.Join(missing, ", ") + "). The builder is a small model that reads ONLY its packet, so make it self-contained:\n" +
			"- Research narrowly: `rg -n` for symbols, read ~60-line windows. Don't survey the whole repo.\n" +
			"- Read-first files with line ranges, the pattern to copy, the exact test to add, constraints, out of scope. ≤120 lines.\n" +
			"- Size: one builder session, roughly ≤150 changed lines. If a task is bigger, split it:\n" +
			fmt.Sprintf("  `factory task-add %d \"<title>\" --covers AC2 --deps T1` and narrow the original with `factory internal task-set %d T<n> --title ... --covers ... --deps ...`.\n", it.ID, it.ID) +
			"- Every AC must stay covered by some task.",
		fmt.Sprintf("## Commands\n- packet: factory write task %d T<n> <<'EOF'\n  ...\n  EOF   (sections: `factory internal template task`)\n- check: factory cat plan %d\n\n"+
			"## Return (end your reply with exactly)\nDECISION: planned | park\nREASON: <one line>", it.ID, it.ID),
	}, "\n\n")
}

func verdictLine(doc string) string {
	for _, l := range strings.Split(doc, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "Verdict:") {
			return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "Verdict:"))
		}
	}
	return "-"
}

func briefJudge(s *Store, it Item) string {
	stat, _ := gitRun(s.root, "diff", "--stat=120", integrationBranch+"..."+branchName(it))
	if lines := strings.Split(stat, "\n"); len(lines) > 40 {
		stat = strings.Join(append(append(lines[:35:35], "  ..."), lines[len(lines)-1]), "\n")
	}
	var ts []string
	for _, t := range s.tasks(it) {
		line := fmt.Sprintf("- T%d %s | covers %s | %s | verify: %s", t.N, t.Title, orDash(t.Covers), t.Status, verdictLine(s.getDoc("verify", it.ID, t.N)))
		if t.Fixes > 0 {
			line += fmt.Sprintf(" | fix rounds: %d", t.Fixes)
		}
		if t.Note != "" {
			line += " | " + t.Note
		}
		ts = append(ts, line)
	}
	return strings.Join([]string{
		fmt.Sprintf("# Judge brief: %s %s: %s", fid(it.ID), kindScope(it), it.Title),
		worktreeLine(s, it),
		acBlock(s, it, ""),
		section("Tasks", strings.Join(ts, "\n")),
		section("Diffstat vs factory", stat),
		"## Your job\nDecide whether this feature is complete and safe to merge. Every AC must be satisfied by verified tasks. " +
			fmt.Sprintf("Spot-check with `git diff %s...%s -- <file>` only where the summary leaves doubt; don't read the whole diff.", integrationBranch, branchName(it)),
		fmt.Sprintf("## Commands\n- merge: write the squash-commit body (2-6 lines: what and why; the subject `%s: %s` is added for you):\n"+
			"  factory write merge-msg %d <<'EOF'\n  ...\n  EOF\n"+
			"- tasks-added: for each gap, `factory task-add %[3]d \"<title>\" --covers ACn` and write its packet with `factory write task %[3]d T<n>`.\n"+
			"- park: when a human is needed.\n\n"+
			"## Return (end your reply with exactly)\nDECISION: merge | tasks-added | park\nREASON: <one line>", kindScope(it), it.Title, it.ID),
	}, "\n\n")
}

func briefResolve(s *Store, it Item, phase string) string {
	dir, how := featureWorktree(s, it), ""
	switch phase {
	case "rebase":
		dir = integrationWorktree(s)
		how = "A `git rebase` of the factory integration branch onto main stopped on conflicts.\n" +
			"After resolving each file: `git add <file>`, then `git rebase --continue`. Repeat if it stops again."
	case "merge":
		how = fmt.Sprintf("`git merge %s` into this feature's branch stopped on conflicts.\n", integrationBranch) +
			fmt.Sprintf("After resolving: `git add <files>`, `git commit --no-edit`, then `factory gate --item %d` must pass (fix and amend nothing; add a new commit if needed).", it.ID)
	default:
		die("resolve phase must be rebase or merge")
	}
	files := conflictedFiles(dir)
	intent := "(rebasing the integration branch; keep both main's and factory's changes)"
	title := "integration branch"
	if it.ID != 0 {
		intent = strings.TrimPrefix(acBlock(s, it, ""), "## Acceptance criteria\n")
		title = fmt.Sprintf("%s %s: %s", fid(it.ID), kindScope(it), it.Title)
	}
	return strings.Join([]string{
		fmt.Sprintf("# Resolve brief (%s): %s", phase, title),
		"directory: " + dir,
		section("Conflicted files", "- "+strings.Join(files, "\n- ")),
		section("Intent to preserve", intent),
		"## How\n" + how + "\n" +
			"- Keep the intent of BOTH sides; never drop the other side's changes wholesale. For the other side's intent, see `git log --oneline -5 " + integrationBranch + "`.\n" +
			"- Never use push, `reset --hard`, or `rebase --abort`/`merge --abort`. If you can't resolve it safely, stop and say so.",
		"## Return (end your reply with exactly)\nDECISION: resolved | unresolved\nREASON: <one line>",
	}, "\n\n")
}

func cmdBrief(a Args) {
	s := cur()
	kind := a.need(0, "build|verify|plan|judge|resolve")
	ref := a.need(1, "item")
	var it Item
	if kind == "resolve" && ref == "-" {
		it = Item{Slug: "factory", Title: "integration branch", Kind: "chore"}
	} else {
		it = s.item(ref)
	}
	switch kind {
	case "build":
		fmt.Println(briefBuild(s, it, s.task(it, a.need(2, "task")), a.Bools["fix"]))
	case "verify":
		fmt.Println(briefVerify(s, it, s.task(it, a.need(2, "task")), a.Bools["tests"]))
	case "plan":
		fmt.Println(briefPlan(s, it))
	case "judge":
		fmt.Println(briefJudge(s, it))
	case "refresh":
		fmt.Println(briefRefresh(s, it, s.task(it, a.need(2, "task"))))
	case "resolve":
		phase, _ := a.flag("phase")
		if phase == "" {
			phase = a.pos(2)
		}
		fmt.Println(briefResolve(s, it, phase))
	default:
		die("brief kind must be build, verify, plan, judge, refresh or resolve")
	}
}
