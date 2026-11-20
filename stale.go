package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Stale-packet detection: packets reference code ("Read first" refs). When other features
// merge, those refs can shift (auto-remapped), change, disappear, or lose their symbol.

const refsSchema = `
CREATE TABLE IF NOT EXISTS packet_refs(
  item_id INTEGER, task_n INTEGER, raw TEXT, path TEXT, start_line INTEGER DEFAULT 0, end_line INTEGER DEFAULT 0,
  symbol TEXT DEFAULT '', blob TEXT DEFAULT '', base_commit TEXT DEFAULT '');
`

type packetRef struct {
	ItemID, TaskN, Start, End     int
	Raw, Path, Symbol, Blob, Base string
}

type hunk struct{ OldStart, OldCount, NewStart, NewCount int }

type refResult struct {
	Ref                packetRef
	State              string // ok shifted changed missing symbol-gone
	Detail             string
	NewStart, NewEnd   int
	NewBlob, NewCommit string
}

var (
	readFirstRe = regexp.MustCompile(`(?s)## Read first[^\n]*\n(.*?)(?:\n## |\z)`)
	refTokRe    = regexp.MustCompile("`([^`\\s]+?)(?::(\\d+)(?:-(\\d+))?)?(?:\\s+\\(([A-Za-z_][\\w.:]*)\\))?`(?:\\s*\\(`?([A-Za-z_][\\w.:]*)`?\\))?")
	hunkRe      = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)
)

// parseRefs extracts file refs from a packet's "Read first" section.
func parseRefs(body string) []packetRef {
	m := readFirstRe.FindStringSubmatch(body)
	if m == nil {
		return nil
	}
	var out []packetRef
	for _, g := range refTokRe.FindAllStringSubmatch(m[1], -1) {
		path := g[1]
		if !strings.ContainsAny(path, "./") || strings.HasPrefix(path, "factory") {
			continue
		}
		r := packetRef{Raw: strings.SplitN(g[0], "`", 3)[1], Path: path}
		r.Raw = "`" + r.Raw + "`"
		if g[2] != "" {
			r.Start, _ = strconv.Atoi(g[2])
			r.End = r.Start
			if g[3] != "" {
				r.End, _ = strconv.Atoi(g[3])
			}
		}
		r.Symbol = g[4]
		if r.Symbol == "" {
			r.Symbol = g[5]
		}
		out = append(out, r)
	}
	return out
}

// remapRange moves [start,end] through diff hunks. overlap is true if any hunk touches the range.
func remapRange(start, end int, hunks []hunk) (int, int, bool) {
	delta := 0
	for _, h := range hunks {
		if h.OldCount == 0 { // pure insertion after line OldStart
			switch {
			case h.OldStart < start:
				delta += h.NewCount
			case h.OldStart < end:
				return start, end, true
			}
			continue
		}
		hEnd := h.OldStart + h.OldCount - 1
		switch {
		case hEnd < start:
			delta += h.NewCount - h.OldCount
		case h.OldStart > end:
		default:
			return start, end, true
		}
	}
	return start + delta, end + delta, false
}

func parseHunks(diff string) []hunk {
	var out []hunk
	for _, l := range strings.Split(diff, "\n") {
		g := hunkRe.FindStringSubmatch(l)
		if g == nil {
			continue
		}
		atoi := func(s string, def int) int {
			if s == "" {
				return def
			}
			n, _ := strconv.Atoi(s)
			return n
		}
		out = append(out, hunk{atoi(g[1], 0), atoi(g[2], 1), atoi(g[3], 0), atoi(g[4], 1)})
	}
	return out
}

// refBase is the commit packets are checked against: the factory tip, else main.
func refBase(s *Store) string {
	b := integrationBranch
	if !branchExists(s.root, b) {
		b = mainBranch(s)
	}
	sha, _ := gitRun(s.root, "rev-parse", b)
	return sha
}

func blobAt(root, commit, path string) string {
	b, ok := gitRun(root, "rev-parse", "-q", "--verify", commit+":"+path)
	if !ok {
		return ""
	}
	return b
}

// storeRefs replaces a task's tracked refs after its packet was (re)written.
func (s *Store) storeRefs(itemID, n int, body string) {
	s.exec("DELETE FROM packet_refs WHERE item_id=? AND task_n=?", itemID, n)
	base := refBase(s)
	for _, r := range parseRefs(body) {
		s.exec("INSERT INTO packet_refs(item_id,task_n,raw,path,start_line,end_line,symbol,blob,base_commit) VALUES(?,?,?,?,?,?,?,?,?)",
			itemID, n, r.Raw, r.Path, r.Start, r.End, r.Symbol, blobAt(s.root, base, r.Path), base)
	}
}

func (s *Store) refs(itemID, n int) []packetRef {
	rows, err := s.db.Query("SELECT item_id,task_n,raw,path,start_line,end_line,symbol,blob,base_commit FROM packet_refs WHERE item_id=? AND task_n=? ORDER BY rowid", itemID, n)
	check(err)
	defer rows.Close()
	var out []packetRef
	for rows.Next() {
		var r packetRef
		check(rows.Scan(&r.ItemID, &r.TaskN, &r.Raw, &r.Path, &r.Start, &r.End, &r.Symbol, &r.Blob, &r.Base))
		out = append(out, r)
	}
	return out
}

func classify(s *Store, r packetRef, head string) refResult {
	res := refResult{Ref: r, State: "ok", NewStart: r.Start, NewEnd: r.End, NewCommit: head}
	if r.Blob == "" || r.Base == "" { // file didn't exist when the packet was written (e.g. a file the task creates)
		return res
	}
	nb := blobAt(s.root, head, r.Path)
	res.NewBlob = nb
	if nb == r.Blob {
		return res
	}
	if nb == "" {
		res.State = "missing"
		out, _ := gitRun(s.root, "diff", "-M", "--name-status", r.Base, head)
		for _, l := range strings.Split(out, "\n") {
			f := strings.Split(l, "\t")
			if len(f) == 3 && strings.HasPrefix(f[0], "R") && f[1] == r.Path {
				res.Detail = "renamed to " + f[2]
			}
		}
		return res
	}
	if r.Symbol != "" {
		if _, found := gitRun(s.root, "grep", "-q", "-w", "-e", r.Symbol, head, "--", r.Path); !found {
			res.State = "symbol-gone"
			return res
		}
	}
	if r.Start > 0 {
		diff, _ := gitRun(s.root, "diff", "-U0", r.Base, head, "--", r.Path)
		ns, ne, overlap := remapRange(r.Start, r.End, parseHunks(diff))
		switch {
		case overlap:
			res.State = "changed"
		case ns != r.Start:
			res.State, res.NewStart, res.NewEnd = "shifted", ns, ne
		}
	}
	return res
}

func (r refResult) changedBy(s *Store) string {
	out, _ := gitRun(s.root, "log", "-1", "--format=%h %s", r.Ref.Base+".."+r.NewCommit, "--", r.Ref.Path)
	return out
}

func newRaw(r packetRef, ns, ne int) string {
	old := fmt.Sprintf("%s:%d", r.Path, r.Start)
	repl := fmt.Sprintf("%s:%d", r.Path, ns)
	if r.End != r.Start {
		old += fmt.Sprintf("-%d", r.End)
		repl += fmt.Sprintf("-%d", ne)
	}
	return strings.Replace(r.Raw, old, repl, 1)
}

// checkTask classifies a task's refs against the current base; with fix, shifted refs are remapped
// in the packet text and the DB. Returns results for every ref.
func (s *Store) checkTask(itemID, n int, fix bool) []refResult {
	refs := s.refs(itemID, n)
	if len(refs) == 0 {
		return nil
	}
	head := refBase(s)
	var out []refResult
	for _, r := range refs {
		res := classify(s, r, head)
		if fix && res.State == "shifted" {
			raw := newRaw(r, res.NewStart, res.NewEnd)
			body := strings.ReplaceAll(s.getDoc("task", itemID, n), r.Raw, raw)
			s.putDoc("task", body, itemID, n)
			s.exec("UPDATE packet_refs SET raw=?, start_line=?, end_line=?, blob=?, base_commit=? WHERE item_id=? AND task_n=? AND raw=?",
				raw, res.NewStart, res.NewEnd, res.NewBlob, head, itemID, n, r.Raw)
			s.event(itemID, "stale", fmt.Sprintf("auto-remapped %s → %s", r.Raw, raw), n)
			res.Detail = "fixed → " + raw
		}
		out = append(out, res)
	}
	return out
}

func needsModel(rs []refResult) []refResult {
	var out []refResult
	for _, r := range rs {
		if r.State == "changed" || r.State == "missing" || r.State == "symbol-gone" {
			out = append(out, r)
		}
	}
	return out
}

// staleCount is the number of refs in an item's todo tasks that need a model to refresh.
func (s *Store) staleCount(it Item) int {
	if it.Status == "merged" || it.Status == "dropped" {
		return 0
	}
	n := 0
	for _, t := range s.tasks(it) {
		if t.Status == "todo" {
			n += len(needsModel(s.checkTask(it.ID, t.N, false)))
		}
	}
	return n
}

// staleReport lists non-ok refs of todo tasks (fixing shifted ones). Returns lines and the count needing a model.
func staleReport(s *Store, items []Item) ([]string, int) {
	var lines []string
	need := 0
	for _, it := range items {
		for _, t := range s.tasks(it) {
			if t.Status != "todo" {
				continue
			}
			for _, r := range s.checkTask(it.ID, t.N, true) {
				switch r.State {
				case "ok":
					continue
				case "shifted":
					lines = append(lines, fmt.Sprintf("%s T%d fixed        %s  (%s)", fid(it.ID), t.N, r.Ref.Raw, r.Detail))
				default:
					need++
					detail := r.Detail
					if by := r.changedBy(s); by != "" {
						detail = strings.TrimSpace(detail + " by " + by)
					}
					lines = append(lines, fmt.Sprintf("%s T%d %-12s %s  (%s)", fid(it.ID), t.N, r.State, r.Ref.Raw, orDash(detail)))
				}
			}
		}
	}
	return lines, need
}

func cmdStale(a Args) {
	for _, p := range projectsFor(a.Bools["all"]) {
		var items []Item
		if ref := a.pos(0); ref != "" && !a.Bools["all"] {
			items = []Item{p.s.item(ref)}
		} else {
			items = p.s.itemsWithStatus([]string{"backlog", "ready", "building", "parked"})
		}
		lines, need := staleReport(p.s, items)
		if a.Bools["all"] {
			fmt.Printf("== %s\n", p.root)
		}
		for _, l := range lines {
			fmt.Println(l)
		}
		if need > 0 {
			fmt.Printf("%d ref(s) need a model: factory brief refresh <item> T<n>\n", need)
		} else if len(lines) == 0 {
			fmt.Println("no stale packets")
		}
	}
}

func capLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		return strings.Join(lines[:n], "\n") + fmt.Sprintf("\n... (%d more lines)", len(lines)-n)
	}
	return s
}

// relevantHunks returns the -U3 diff hunks for path that touch [start-3, end+3] (all hunks if start==0).
func relevantHunks(s *Store, r packetRef, head string) string {
	diff, _ := gitRun(s.root, "diff", "-U3", r.Base, head, "--", r.Path)
	if r.Start == 0 {
		return capLines(diff, 80)
	}
	var out []string
	keep := false
	for _, l := range strings.Split(diff, "\n") {
		if g := hunkRe.FindStringSubmatch(l); g != nil {
			a, _ := strconv.Atoi(g[1])
			b := 1
			if g[2] != "" {
				b, _ = strconv.Atoi(g[2])
			}
			keep = a <= r.End+3 && a+b-1 >= r.Start-3
		}
		if keep {
			out = append(out, l)
		}
	}
	return capLines(strings.Join(out, "\n"), 80)
}

func briefRefresh(s *Store, it Item, t Task) string {
	results := needsModel(s.checkTask(it.ID, t.N, true))
	head := refBase(s)
	parts := []string{
		fmt.Sprintf("# Refresh brief: %s T%d: %s", fid(it.ID), t.N, t.Title),
		"Other features merged since this packet was written; some code it points at changed.",
		section("Current packet", s.getDoc("task", it.ID, t.N)),
	}
	if len(results) == 0 {
		parts = append(parts, "## Broken refs\n(none: the packet is current)")
	}
	for _, r := range results {
		head := fmt.Sprintf("## %s: %s", r.State, r.Ref.Raw)
		if by := r.changedBy(s); by != "" {
			head += "  (changed by " + by + ")"
		}
		body := r.Detail
		if r.State != "missing" {
			body = strings.TrimSpace(body + "\n```diff\n" + relevantHunks(s, r.Ref, r.NewCommit) + "\n```")
		}
		parts = append(parts, head+"\n"+orDash(body))
	}
	parts = append(parts,
		fmt.Sprintf("## Your job\nRewrite the packet so every ref matches the code at `%s` (the factory tip). Keep the task's scope, goal and test identical: only update refs, line ranges and instructions the change invalidated. Read the new code narrowly (`git show %s:<path> | sed -n 'A,Bp'`).", short(head), short(head)),
		fmt.Sprintf("## Commands\nfactory write task %d T%d <<'EOF'\n...full packet...\nEOF\n\n## Return (end your reply with exactly)\nDECISION: refreshed | park\nREASON: <one line>", it.ID, t.N))
	return strings.Join(parts, "\n\n")
}
