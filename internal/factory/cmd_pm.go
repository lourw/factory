package factory

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var (
	slugRe       = regexp.MustCompile(`[^a-z0-9]+`)
	regressionRe = regexp.MustCompile(`(?i)regression[- ]test`)
)

func slugify(s string) string {
	out := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(out) > 40 {
		out = strings.Trim(out[:40], "-")
	}
	if out == "" {
		return "item"
	}
	return out
}

func kindScope(it Item) string {
	if it.Scope != "" {
		return it.Kind + "(" + it.Scope + ")"
	}
	return it.Kind
}

func itemLine(s *Store, it Item) string {
	return fmt.Sprintf("%s  p%d  %-9s %-7s %4s  %s: %s", fid(it.ID), it.Priority, it.Status, progress(s.tasks(it)), ago(it.Updated), kindScope(it), it.Title)
}

func cmdInit(a Args) {
	root := findRoot()
	s := openStore(root, true)
	s.exec("INSERT OR IGNORE INTO meta VALUES('created', ?)", fmt.Sprint(now()))
	exclude := filepath.Join(root, ".git", "info", "exclude")
	if st, err := os.Stat(filepath.Dir(exclude)); err == nil && st.IsDir() {
		b, _ := os.ReadFile(exclude)
		text := string(b)
		if !contains(strings.Fields(text), ".factory/") {
			if text != "" && !strings.HasSuffix(text, "\n") {
				text += "\n"
			}
			check(os.WriteFile(exclude, []byte(text+".factory/\n"), 0o644))
		}
	}
	register(root)
	fmt.Printf("initialized %s/.factory (excluded from git, registered)\n", root)
	fmt.Println("next: set up gate commands + code map: \"init factory\" with the factory-feature skill in Claude Code, or /factory-init in opencode,")
	fmt.Println("      then write features with /factory-feature and run `factory run`")
}

func validKind(k string) string {
	if !contains(kinds, k) {
		die("--kind must be one of: %s", strings.Join(kinds, ", "))
	}
	return k
}

func cmdAdd(a Args) {
	s := cur()
	title := a.rest(0)
	if title == "" {
		die("usage: factory add <title...> [--kind feat] [--scope s] [-p 1-4]")
	}
	slug, ok := a.flag("slug")
	if !ok {
		slug = slugify(title)
	}
	kind, ok := a.flag("kind")
	if !ok {
		kind = "feat"
	}
	scope, _ := a.flag("scope")
	r := s.exec("INSERT INTO items(slug,title,kind,scope,priority,status,created,updated) VALUES(?,?,?,?,?,?,?,?)",
		slug, title, validKind(kind), scope, a.intFlag("prio", 2), "backlog", now(), now())
	id, _ := r.LastInsertId()
	s.event(int(id), "status", "backlog", 0)
	fmt.Printf("%s backlog  %s\n", fid(int(id)), title)
}

type project struct {
	root string
	s    *Store
}

func projectsFor(all bool) []project {
	if !all {
		root := findRoot()
		return []project{{root, openStore(root, false)}}
	}
	var out []project
	for _, p := range registry() {
		if _, err := os.Stat(dbPath(p)); err == nil {
			out = append(out, project{p, openStore(p, false)})
		}
	}
	return out
}

func showItem(s *Store, it Item, events int) {
	ts := s.tasks(it)
	fmt.Printf("%s %s: %s\n", fid(it.ID), kindScope(it), it.Title)
	base := ""
	if it.Base != "" {
		base = " | base: " + it.Base[:min(10, len(it.Base))]
	}
	fmt.Printf("status: %s | p%d | branch: %s%s\n", it.Status, it.Priority, branchName(it), base)
	if it.Status == "parked" && it.ParkReason != "" {
		fmt.Printf("parked: %s\n", it.ParkReason)
	}
	if wt := featureWorktree(s, it); dirExists(wt) {
		fmt.Printf("worktree: %s\n", wt)
	}
	var runs int
	var cost, secs float64
	if s.db.QueryRow("SELECT count(*), COALESCE(sum(cost_usd),0), COALESCE(sum(secs),0) FROM agent_runs WHERE item_id=?", it.ID).Scan(&runs, &cost, &secs) == nil && runs > 0 {
		fmt.Printf("agents: %d runs, $%.2f, %.0fs\n", runs, cost, secs)
	}
	fmt.Printf("next: %s\n", nextFor(s, it))
	if n := s.staleCount(it); n > 0 {
		fmt.Printf("stale: %d packet ref(s) need a refresh (factory stale %d)\n", n, it.ID)
	}
	if len(ts) > 0 {
		fmt.Println("tasks:")
		for _, t := range ts {
			var extra []string
			if t.Sha != "" {
				extra = append(extra, t.Sha[:min(8, len(t.Sha))])
			}
			if t.Fixes > 0 {
				extra = append(extra, fmt.Sprintf("fixes=%d", t.Fixes))
			}
			if s.getDoc("task", it.ID, t.N) == "" {
				extra = append(extra, "(no packet)")
			}
			if t.Note != "" {
				extra = append(extra, "— "+t.Note)
			}
			fmt.Println(strings.TrimRight(fmt.Sprintf("  %s T%d: %s  %s", mark[t.Status], t.N, t.Title, strings.Join(extra, " ")), " "))
		}
	}
	rows, err := s.db.Query("SELECT kind, task_n FROM docs WHERE item_id=? ORDER BY task_n, kind", it.ID)
	check(err)
	var docs []string
	for rows.Next() {
		var kind string
		var n int
		check(rows.Scan(&kind, &n))
		if n > 0 {
			kind += fmt.Sprintf(":T%d", n)
		}
		docs = append(docs, kind)
	}
	rows.Close()
	if len(docs) > 0 {
		fmt.Println("docs: " + strings.Join(docs, ", "))
	}
	type ev struct {
		ts        float64
		n         int
		kind, msg string
	}
	evRows, err := s.db.Query("SELECT ts, task_n, kind, msg FROM events WHERE item_id=? ORDER BY id DESC LIMIT ?", it.ID, events)
	check(err)
	var evs []ev
	for evRows.Next() {
		var e ev
		check(evRows.Scan(&e.ts, &e.n, &e.kind, &e.msg))
		evs = append(evs, e)
	}
	evRows.Close()
	if len(evs) > 0 {
		fmt.Println("recent:")
		for i := len(evs) - 1; i >= 0; i-- {
			e := evs[i]
			t := ""
			if e.n > 0 {
				t = fmt.Sprintf(" T%d", e.n)
			}
			fmt.Printf("  %s%s %s: %s\n", time.Unix(int64(e.ts), 0).Format("01-02 15:04"), t, e.kind, e.msg)
		}
	}
}

// specWarnings re-runs the ready validation for items past backlog; spec edits
// never block, so problems come back as warnings.
func specWarnings(s *Store, it Item) []string {
	if it.Status == "backlog" {
		return nil
	}
	var out []string
	for _, p := range readyProblems(s, it) {
		if strings.HasPrefix(p, "ACs not covered") {
			p += fmt.Sprintf(" (factory task-add %d <title> --covers ACn)", it.ID)
		}
		out = append(out, p)
	}
	return out
}

// readyProblems lists what keeps an item from being picked up by `factory run`.
func readyProblems(s *Store, it Item) []string {
	var problems []string
	spec := s.getDoc("spec", it.ID, 0)
	acs := parseACs(spec)
	if spec == "" {
		problems = append(problems, "no spec (factory write spec <item>)")
	} else if len(acs) == 0 {
		problems = append(problems, "spec has no **ACn** acceptance criteria")
	}
	ts := s.tasks(it)
	if len(ts) == 0 {
		problems = append(problems, "no tasks (factory task-add <item> <title>)")
	}
	covered := map[string]bool{}
	for _, t := range ts {
		if t.Status == "dropped" {
			continue
		}
		for _, c := range splitList(t.Covers) {
			covered[c] = true
		}
	}
	var missing []string
	for _, ac := range acs {
		if !covered[ac.ID] {
			missing = append(missing, ac.ID)
		}
	}
	if len(missing) > 0 {
		problems = append(problems, "ACs not covered by any task: "+strings.Join(missing, ", "))
	}
	if it.Kind == "fix" {
		found := false
		for _, ac := range acs {
			if regressionRe.MatchString(ac.Text) {
				found = true
			}
		}
		if !found {
			problems = append(problems, "bug fixes need a regression-test AC (e.g. **AC1**: a regression test reproduces the bug and passes after the fix)")
		}
	}
	return problems
}

func cmdReady(a Args) {
	s := cur()
	it := s.item(a.need(0, "item"))
	if it.Status != "backlog" {
		die("%s is %s; only backlog items can be made ready", fid(it.ID), it.Status)
	}
	if p := readyProblems(s, it); len(p) > 0 && !a.Bools["force"] {
		die("not ready:\n  - %s\n(use --force to override)", strings.Join(p, "\n  - "))
	}
	s.setStatus(it, "ready", "")
	fmt.Printf("%s ready. `factory run` will pick it up.\n", fid(it.ID))
}

func cmdLog(a Args) {
	s := cur()
	it := s.item(a.need(0, "item"))
	msg := a.rest(1)
	if msg == "" {
		die("usage: factory internal log <item> <msg...>")
	}
	n := 0
	if t, ok := a.flag("task"); ok {
		n = taskN(t)
	}
	kind, ok := a.flag("kind")
	if !ok {
		kind = "note"
	}
	s.event(it.ID, kind, msg, n)
}
