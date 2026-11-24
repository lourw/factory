package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

func stdinPiped() bool {
	st, err := os.Stdin.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice == 0
}

func readStdin() string {
	b, err := io.ReadAll(os.Stdin)
	check(err)
	return strings.TrimSpace(string(b))
}

func cmdTaskAdd(a Args) {
	s := cur()
	it := s.item(a.need(0, "item"))
	title := a.rest(1)
	if title == "" {
		die("usage: factory task-add <item> <title...> [--covers AC1] [--deps T1] [--body]")
	}
	if !contains([]string{"backlog", "ready", "building", "judging"}, it.Status) && !a.Bools["force"] {
		die("%s is %s; tasks can't be added now (use --force)", fid(it.ID), it.Status)
	}
	var max int
	check(s.db.QueryRow("SELECT COALESCE(max(n),0) FROM tasks WHERE item_id=?", it.ID).Scan(&max))
	n := max + 1
	covers, _ := a.flag("covers")
	deps, _ := a.flag("deps")
	s.exec("INSERT INTO tasks(item_id,n,title,covers,deps,updated) VALUES(?,?,?,?,?,?)",
		it.ID, n, title, strings.ToUpper(strings.Join(splitList(covers), ",")), normTaskList(deps), now())
	s.event(it.ID, "task", "added: "+title, n)
	s.refreshItemStatus(it.ID)
	if a.Bools["body"] {
		if !stdinPiped() {
			die("--body needs the packet on stdin")
		}
		body := readStdin()
		s.putDoc("task", body, it.ID, n)
		s.storeRefs(it.ID, n, body)
	}
	fmt.Printf("T%d\n", n)
}

func cmdTaskSet(a Args) {
	s := cur()
	it := s.item(a.need(0, "item"))
	t := s.task(it, a.need(1, "task"))
	var fields, msgs []string
	var vals []any
	if st, ok := a.flag("status"); ok {
		if !contains(taskStatuses, st) {
			die("bad task status (one of %s)", strings.Join(taskStatuses, ", "))
		}
		fields, vals, msgs = append(fields, "status=?"), append(vals, st), append(msgs, st)
	}
	if v, ok := a.flag("sha"); ok {
		fields, vals = append(fields, "sha=?"), append(vals, v)
	}
	note, hasNote := a.flag("note")
	if hasNote {
		fields, vals = append(fields, "note=?"), append(vals, note)
	}
	if v, ok := a.flag("title"); ok {
		fields, vals = append(fields, "title=?"), append(vals, v)
	}
	if v, ok := a.flag("covers"); ok {
		fields, vals = append(fields, "covers=?"), append(vals, strings.ToUpper(strings.Join(splitList(v), ",")))
	}
	if v, ok := a.flag("deps"); ok {
		fields, vals = append(fields, "deps=?"), append(vals, normTaskList(v))
	}
	if len(fields) == 0 {
		die("nothing to set")
	}
	vals = append(vals, now(), it.ID, t.N)
	s.exec("UPDATE tasks SET "+strings.Join(fields, ", ")+", updated=? WHERE item_id=? AND n=?", vals...)
	if len(msgs) > 0 || note != "" {
		msg := strings.Join(msgs, " ")
		if note != "" {
			msg += ": " + note
		}
		s.event(it.ID, "task", strings.TrimPrefix(msg, ": "), t.N)
	}
	s.refreshItemStatus(it.ID)
	st := t.Status
	if v, ok := a.flag("status"); ok {
		st = v
	}
	fmt.Printf("T%d %s\n", t.N, st)
}

// nextRunnable returns the first todo task whose deps are all satisfied
// (verified or dropped), skipping ids in skip; nil if none.
func nextRunnable(ts []Task, skip map[string]bool) *Task {
	st := map[string]string{}
	for _, t := range ts {
		st[fmt.Sprintf("T%d", t.N)] = t.Status
	}
	ok := set("verified", "dropped")
	for i, t := range ts {
		if t.Status != "todo" || skip[fmt.Sprintf("T%d", t.N)] {
			continue
		}
		ready := true
		for _, d := range splitList(t.Deps) {
			if !ok[st[d]] {
				ready = false
				break
			}
		}
		if ready {
			return &ts[i]
		}
	}
	return nil
}

func allVerified(ts []Task) bool {
	if len(ts) == 0 {
		return false
	}
	for _, t := range ts {
		if t.Status != "verified" && t.Status != "dropped" {
			return false
		}
	}
	return true
}
