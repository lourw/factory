package factory

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------- board

func installOK() string {
	owned := installedList()
	if len(owned) == 0 {
		return "not installed (./install.sh)"
	}
	for _, p := range owned {
		if _, err := os.Stat(p); err != nil {
			return "broken (" + p + " missing; rerun ./install.sh)"
		}
	}
	return "ok"
}

func lastGate(s *Store) string {
	rows := s.gateRows()
	var names []string
	for _, g := range rows {
		if !gateExcluded(g.Name) {
			names = append(names, g.Name)
		}
	}
	if len(names) == 0 {
		return "no checks (set up: factory-feature init, or /factory-init)"
	}
	var ts float64
	var failed []string
	for _, n := range names {
		var ok int
		var t float64
		if s.db.QueryRow("SELECT ok, ts FROM gate_runs WHERE name=? ORDER BY id DESC LIMIT 1", n).Scan(&ok, &t) == nil {
			if ok == 0 {
				failed = append(failed, n)
			}
			ts = max(ts, t)
		}
	}
	res := "never run"
	if ts > 0 {
		res = "last PASS"
		if len(failed) > 0 {
			res = "last FAIL (" + strings.Join(failed, ", ") + ")"
		}
		if a := ago(ts); a == "now" {
			res += " just now"
		} else {
			res += " " + a + " ago"
		}
	}
	return fmt.Sprintf("%d checks, %s", len(names), res)
}

func worktreeCount(s *Store) int {
	entries, _ := os.ReadDir(worktreesBase(s))
	n := 0
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), "_") {
			n++
		}
	}
	return n
}

func integrationLine(s *Store) string {
	if !branchExists(s.root, integrationBranch) {
		return "factory: not created yet (first `factory run`)"
	}
	mb := mainBranch(s)
	head, _ := gitRun(s.root, "log", "-1", "--format=%h %s", integrationBranch)
	ahead, _ := gitRun(s.root, "rev-list", "--count", mb+".."+integrationBranch)
	behind, _ := gitRun(s.root, "rev-list", "--count", integrationBranch+".."+mb)
	line := fmt.Sprintf("factory: %s | +%s ahead of %s", head, ahead, mb)
	if behind != "0" {
		line += fmt.Sprintf(", %s behind", behind)
	}
	if ahead != "0" {
		line += " → factory review, then land"
	}
	return line
}

func boardHeader(s *Store, items []Item) {
	stale := 0
	for _, it := range items {
		stale += s.staleCount(it)
	}
	run := ""
	if pid := activeRun(s.root); pid != 0 {
		run = fmt.Sprintf(" | run active (pid %d)", pid)
	}
	fmt.Printf("%s%s\n", integrationLine(s), run)
	mapStale := ""
	if ts := s.docUpdated("map", 0, 0); ts > 0 {
		if n := len(s.mergedSince(ts)); n > 0 {
			mapStale = fmt.Sprintf(" | map stale (%d merged since)", n)
		}
	}
	fmt.Printf("install %s | agents %s | gate %s | worktrees %d | stale %d | projects %d%s\n",
		installOK(), agentsLabel(s), lastGate(s), worktreeCount(s), stale, len(registry()), mapStale)
}

// agentsLabel names the effective agent backend for the board header.
func agentsLabel(s *Store) string {
	if os.Getenv("FACTORY_AGENT_CMD") != "" {
		return "custom (FACTORY_AGENT_CMD)"
	}
	if s.cfg("backend", "opencode") == "claude" {
		base := s.cfg("claude_model", "opus")
		var over []string
		for _, role := range []string{"overseer", "builder", "verifier"} {
			if m := claudeModel(s, role); m != base {
				over = append(over, role+" "+m)
			}
		}
		if len(over) == 0 {
			return "claude/" + base
		}
		return "claude/" + base + " (" + strings.Join(over, ", ") + ")"
	}
	return "opencode"
}

func boardLine(s *Store, it Item, withStatus bool) string {
	st := ""
	if withStatus {
		st = fmt.Sprintf("%-9s ", it.Status)
	}
	line := fmt.Sprintf("  %s p%d %s%-6s %-16s %s", fid(it.ID), it.Priority, st, progress(s.tasks(it)), kindScope(it), it.Title)
	if n := s.staleCount(it); n > 0 {
		line += fmt.Sprintf("  [stale:%d]", n)
	}
	if it.Status == "parked" && it.ParkReason != "" {
		line += "  (" + it.ParkReason + ")"
	}
	if nx := nextFor(s, it); nx != "-" {
		line += "\n      → " + nx
	}
	return line
}

func cmdBoard(a Args) {
	sts := openStatuses
	if a.Bools["done"] {
		sts = statuses
	}
	kind, byKind := a.flag("kind")
	if byKind {
		validKind(kind)
	}
	all := a.Bools["all"]
	for _, p := range projectsFor(all) {
		if all {
			fmt.Printf("== %s  (%s)\n", filepath.Base(p.root), p.root)
		}
		var items []Item
		for _, it := range p.s.itemsWithStatus(sts) {
			if !byKind || it.Kind == kind {
				items = append(items, it)
			}
		}
		boardHeader(p.s, items)
		if len(items) == 0 {
			fmt.Println("  (nothing queued: write features with /factory-feature)")
		} else if a.Bools["flat"] {
			for _, it := range items {
				fmt.Println(boardLine(p.s, it, true))
			}
		} else {
			for _, st := range sts {
				first := true
				for _, it := range items {
					if it.Status != st {
						continue
					}
					if first {
						fmt.Printf("[%s]\n", st)
						first = false
					}
					fmt.Println(boardLine(p.s, it, false))
				}
			}
		}
		if all {
			fmt.Println()
		}
	}
}

// ---------------------------------------------------------------- show

func cmdShow(a Args) {
	s := cur()
	it := s.item(a.need(0, "item"))
	if a.Bools["md"] {
		exportItem(s, it)
		return
	}
	showItem(s, it, a.intFlag("events", 8))
}

// ---------------------------------------------------------------- review

// cmdReview summarizes each unlanded factory commit (main..factory) that carries a Factory-Item trailer.
func cmdReview(a Args) {
	s := cur()
	if !branchExists(s.root, integrationBranch) {
		fmt.Println("no unlanded commits (no `factory` branch yet)")
		return
	}
	out := git(s.root, "log", "--format=%H%x00%s%x00%b%x01", mainBranch(s)+".."+integrationBranch)
	n, total := 0, 0.0
	for _, rec := range strings.Split(out, "\x01") {
		parts := strings.SplitN(strings.TrimSpace(rec), "\x00", 3)
		if len(parts) < 3 {
			continue
		}
		id := ""
		for _, l := range strings.Split(parts[2], "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(l), "Factory-Item:"); ok {
				id = strings.TrimSpace(v)
			}
		}
		if id == "" {
			continue
		}
		it := s.item(id)
		fmt.Println(reviewLine(s, it, parts[0]))
		if st, err := gitOut(s.root, "show", "--shortstat", "--format=", parts[0]); err == nil && st != "" {
			fmt.Println("  " + st)
		}
		_, c, _ := itemUsage(s, it.ID, 0)
		n, total = n+1, total+c
	}
	if n == 0 {
		fmt.Println("no unlanded commits")
		return
	}
	fmt.Printf("total: %d item(s), $%.2f\n", n, total)
}

// reviewLine formats one unlanded item: commit, subject, id, ACs, tasks/fix rounds, agent runs + cost.
func reviewLine(s *Store, it Item, sha string) string {
	ts := s.tasks(it)
	fixes := 0
	for _, t := range ts {
		fixes += t.Fixes
	}
	runs, cost, _ := itemUsage(s, it.ID, 0)
	return fmt.Sprintf("%s %s: %s [%s] | %d ACs | %d tasks, %d fix rounds | %d runs $%.2f",
		sha[:min(8, len(sha))], kindScope(it), it.Title, fid(it.ID), len(parseACs(s.getDoc("spec", it.ID, 0))),
		len(ts), fixes, runs, cost)
}

// ---------------------------------------------------------------- set

func cmdSet(a Args) {
	s := cur()
	it := s.item(a.need(0, "item"))
	if st, ok := a.flag("status"); ok {
		if !contains(statuses, st) {
			die("bad status '%s' (one of: %s)", st, strings.Join(statuses, ", "))
		}
		if it.Status == "parked" && (st == "ready" || st == "building") {
			// unpark: retry blocked/failed tasks from scratch; the next run gives it a slot and
			// moves its parked worktree (or branch) back into that slot's path
			s.exec("UPDATE tasks SET status='todo', fixes=0, note='' WHERE item_id=? AND status IN ('blocked','failed')", it.ID)
			s.exec("UPDATE items SET park_reason='' WHERE id=?", it.ID)
			s.setStatus(it, "ready", "unparked")
		} else if st == "parked" {
			s.park(it, "manual")
		} else {
			if st == "dropped" {
				parkWorktree(s, it) // free the slot; `factory clean` removes the worktree
				s.exec("UPDATE items SET slot=0 WHERE id=?", it.ID)
			}
			s.setStatus(it, st, "manual")
		}
	}
	if _, ok := a.flag("prio"); ok {
		s.exec("UPDATE items SET priority=?, updated=? WHERE id=?", a.intFlag("prio", 2), now(), it.ID)
	}
	if t, ok := a.flag("title"); ok {
		s.exec("UPDATE items SET title=?, updated=? WHERE id=?", t, now(), it.ID)
	}
	if k, ok := a.flag("kind"); ok {
		s.exec("UPDATE items SET kind=? WHERE id=?", validKind(k), it.ID)
	}
	if sc, ok := a.flag("scope"); ok {
		s.exec("UPDATE items SET scope=? WHERE id=?", sc, it.ID)
	}
	it = s.item(strconv.Itoa(it.ID))
	fmt.Println(strings.TrimSpace(boardLine(s, it, true)))
}

// ---------------------------------------------------------------- clean

func isTTY() bool {
	st, err := os.Stdin.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func cmdClean(a Args) {
	s := cur()
	if pid := activeRun(s.root); pid != 0 {
		die("a factory run is active (pid %d); it cleans up on its own", pid)
	}
	removed := gc(s, false, false)
	// parked worktrees: ask on a TTY (or remove all with --parked), keep otherwise
	in := bufio.NewReader(os.Stdin)
	for _, it := range s.itemsWithStatus([]string{"parked"}) {
		wt := featureWorktree(s, it)
		if !dirExists(wt) {
			continue
		}
		remove := a.Bools["parked"]
		if !remove && isTTY() {
			fmt.Printf("remove parked worktree %s %s (%s)? [y/N] ", fid(it.ID), it.Title, orDash(it.ParkReason))
			ans, _ := in.ReadString('\n')
			remove = strings.HasPrefix(strings.ToLower(strings.TrimSpace(ans)), "y")
		}
		if remove {
			removeWorktree(s.root, wt)
			removed++
			fmt.Printf("removed %s (parked; branch %s kept)\n", wt, branchName(it))
		} else {
			fmt.Printf("kept parked worktree %s\n", wt)
		}
	}
	gitRun(s.root, "worktree", "prune")
	lines, need := staleReport(s, s.itemsWithStatus([]string{"backlog", "ready", "building", "parked"}))
	for _, l := range lines {
		if strings.Contains(l, " fixed ") {
			fmt.Println("packet " + l)
		}
	}
	if need > 0 {
		fmt.Println("needs a model → /factory-feature refresh:")
		for _, l := range lines {
			if !strings.Contains(l, " fixed ") {
				fmt.Println("  " + l)
			}
		}
	}
	fmt.Printf("clean: %d worktree(s) removed\n", removed)
}

// ---------------------------------------------------------------- config (internal)

func cmdConfig(a Args) {
	s := cur()
	keys := make([]string, 0, len(configKeys))
	for k := range configKeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	switch len(a.Pos) {
	case 0:
		for _, k := range keys {
			fmt.Printf("%-14s %-30s %s\n", k, orDash(s.cfg(k, "")), configKeys[k])
		}
		b := budgetFor(s)
		fmt.Printf("\neffective: worktrees %s | cache %s | budget %s (tasks ≤%d lines)\n", worktreesBase(s), s.cacheDir(), b.Backend, b.TaskLines)
		return
	case 1:
		if _, ok := configKeys[a.Pos[0]]; !ok {
			die("unknown key %q (keys: %s)", a.Pos[0], strings.Join(keys, ", "))
		}
		fmt.Println(s.cfg(a.Pos[0], ""))
		return
	}
	key, val := a.Pos[0], a.rest(1)
	if _, ok := configKeys[key]; !ok {
		die("unknown key %q (keys: %s)", key, strings.Join(keys, ", "))
	}
	if key == "tagging" && val != "off" && val != "semver" && val != "-" {
		die("tagging must be off or semver")
	}
	if key == "gate_cache" && val != "on" && val != "off" && val != "-" {
		die("gate_cache must be on or off")
	}
	if key == "backend" && val != "opencode" && val != "claude" && val != "-" {
		die("backend must be opencode or claude")
	}
	if strings.HasPrefix(key, "claude_effort") && val != "-" &&
		val != "low" && val != "medium" && val != "high" && val != "xhigh" && val != "max" {
		die("%s must be low, medium, high, xhigh or max", key)
	}
	if key == "claude_max_usd" && val != "-" {
		if f, err := strconv.ParseFloat(val, 64); err != nil || !(f > 0) {
			die("claude_max_usd must be a positive number")
		}
	}
	if key == "worktree_root" && activeRun(s.root) != 0 {
		die("a run is active; change worktree_root between runs")
	}
	s.setCfg(key, val)
	fmt.Printf("%s = %s\n", key, orDash(s.cfg(key, "")))
}
