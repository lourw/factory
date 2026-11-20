package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	tailLines = envInt("FACTORY_TAIL", 30)
	hitRe     = regexp.MustCompile(`(?i)(error|fail|panic|exception|assert|expected|✗|×)`)
	negRe     = regexp.MustCompile(`(?i)\b0 (failed|errors?|failures)\b`)
	gateOrder = []string{"setup", "format", "lint", "typecheck", "test", "test_focused", "build"}
)

func envInt(name string, def int) int {
	var n int
	if _, err := fmt.Sscan(os.Getenv(name), &n); err == nil && n > 0 {
		return n
	}
	return def
}

// execCtx identifies where a command runs: its slot ("1", "int", "main", ...) and worktree.
type execCtx struct{ Slot, Worktree string }

func (s *Store) ctxFor(dir string) execCtx { return execCtx{slotOf(s, dir), dir} }

func (s *Store) cacheDir() string {
	p := s.cfg("cache_dir", filepath.Join(".factory", "cache"))
	if !filepath.IsAbs(p) {
		p = filepath.Join(s.root, p)
	}
	return filepath.Clean(p)
}

// expand fills {slot}, {cache} and {worktree} in a command or env template.
func (s *Store) expand(tmpl string, c execCtx) string {
	return strings.NewReplacer("{slot}", c.Slot, "{cache}", s.cacheDir(), "{worktree}", c.Worktree).Replace(tmpl)
}

// envFor is the environment added to setup, gate, test and agent processes.
func (s *Store) envFor(c execCtx) []string {
	os.MkdirAll(s.cacheDir(), 0o755)
	env := []string{"FACTORY_SLOT=" + c.Slot, "FACTORY_CACHE=" + s.cacheDir(), "FACTORY_WORKTREE=" + c.Worktree}
	var kv [][2]string // read everything first: one DB connection, and expand() queries too
	rows, err := s.db.Query("SELECT name, cmd FROM gate WHERE name LIKE 'env:%' ORDER BY name")
	check(err)
	for rows.Next() {
		var name, val string
		check(rows.Scan(&name, &val))
		kv = append(kv, [2]string{strings.TrimPrefix(name, "env:"), val})
	}
	rows.Close()
	for _, e := range kv {
		env = append(env, e[0]+"="+s.expand(e[1], c))
	}
	return env
}

func runCheck(root, name, cmd, logdir string, env []string) (bool, float64, string) {
	log := filepath.Join(logdir, name+".log")
	f, err := os.Create(log)
	check(err)
	defer f.Close()
	start := time.Now()
	c := exec.Command("bash", "-c", cmd)
	c.Dir, c.Stdout, c.Stderr = root, f, f
	c.Env = append(os.Environ(), env...)
	err = c.Run()
	return err == nil, time.Since(start).Seconds(), log
}

func excerpt(log string) string {
	b, _ := os.ReadFile(log)
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	var hits []string
	for i, l := range lines {
		if hitRe.MatchString(l) && !negRe.MatchString(l) {
			hits = append(hits, fmt.Sprintf("%d: %s", i+1, l))
		}
	}
	chosen := hits
	if len(chosen) == 0 {
		chosen = lines
	}
	if len(chosen) > tailLines {
		chosen = chosen[len(chosen)-tailLines:]
	}
	out := make([]string, len(chosen))
	for i, l := range chosen {
		if r := []rune(l); len(r) > 240 {
			l = string(r[:240])
		}
		out[i] = "  | " + l
	}
	return strings.Join(out, "\n")
}

func newLogdir(root string) string {
	d := filepath.Join(root, ".factory", "logs", fmt.Sprintf("%s-%d", time.Now().Format("20060102-150405"), os.Getpid()))
	check(os.MkdirAll(d, 0o755))
	return d
}

func (s *Store) recordRun(itemID, n int, name string, ok bool, secs float64, log string) {
	okInt := 0
	if ok {
		okInt = 1
	}
	s.exec("INSERT INTO gate_runs(ts,item_id,task_n,name,ok,secs,log) VALUES(?,?,?,?,?,?,?)", now(), itemID, n, name, okInt, secs, log)
}

func rel(root, p string) string {
	if r, err := filepath.Rel(root, p); err == nil {
		return r
	}
	return p
}

// gateExcluded reports checks that are not part of the gate (run separately).
func gateExcluded(name string) bool {
	return name == "setup" || strings.HasSuffix(name, "_focused") || strings.HasPrefix(name, "env:")
}

// runGate runs gate checks in dir, records them, and returns success plus a compact summary.
func runGate(s *Store, dir string, itemID, n int, names []string) (bool, string) {
	var rows []gateRow
	for _, g := range s.gateRows() {
		if !gateExcluded(g.Name) && (len(names) == 0 || contains(names, g.Name)) {
			rows = append(rows, g)
		}
	}
	if len(rows) == 0 {
		return true, "GATE: PASS (no checks configured)"
	}
	logdir := newLogdir(s.root)
	var out, failed []string
	for _, g := range rows {
		ctx := s.ctxFor(dir)
		ok, secs, log := runCheck(dir, g.Name, s.expand(g.Cmd, ctx), logdir, s.envFor(ctx))
		s.recordRun(itemID, n, g.Name, ok, secs, log)
		switch {
		case ok:
			out = append(out, fmt.Sprintf("PASS %s (%.0fs)", g.Name, secs))
		case g.KnownFailing:
			out = append(out, fmt.Sprintf("KNOWN-FAIL %s (%.0fs, pre-existing; ignored) log: %s", g.Name, secs, rel(s.root, log)))
		default:
			out = append(out, fmt.Sprintf("FAIL %s (%.0fs) log: %s\n%s", g.Name, secs, rel(s.root, log), excerpt(log)))
			failed = append(failed, g.Name)
		}
	}
	if len(failed) > 0 {
		return false, strings.Join(out, "\n") + fmt.Sprintf("\nGATE: FAIL (%s)", strings.Join(failed, ", "))
	}
	return true, strings.Join(out, "\n") + "\nGATE: PASS"
}

// runSetup runs the `setup` gate entry (if any) in dir.
func runSetup(s *Store, dir string, itemID int) (bool, string) {
	var cmd string
	if s.db.QueryRow("SELECT cmd FROM gate WHERE name='setup'").Scan(&cmd) != nil {
		return true, ""
	}
	ctx := s.ctxFor(dir)
	ok, secs, log := runCheck(dir, "setup", s.expand(cmd, ctx), newLogdir(s.root), s.envFor(ctx))
	s.recordRun(itemID, 0, "setup", ok, secs, log)
	if ok {
		return true, fmt.Sprintf("PASS setup (%.0fs)", secs)
	}
	return false, fmt.Sprintf("FAIL setup (%.0fs) log: %s\n%s", secs, rel(s.root, log), excerpt(log))
}

func gateList(s *Store) {
	rows := s.gateRows()
	if len(rows) == 0 {
		fmt.Println("(no gate checks; factory gate config <name> '<cmd>')")
		return
	}
	for _, g := range rows {
		suffix := ""
		if g.KnownFailing {
			suffix = "  [known-failing]"
		}
		if strings.HasPrefix(g.Name, "env:") {
			fmt.Printf("env %-9s %s\n", strings.TrimPrefix(g.Name, "env:"), g.Cmd)
			continue
		}
		fmt.Printf("%-13s %s%s\n", g.Name, g.Cmd, suffix)
	}
}

func cmdGate(a Args) {
	if a.pos(0) == "config" {
		a.Pos = a.Pos[1:]
		gateConfig(a)
		return
	}
	s := cur()
	itemID, n := 0, 0
	if ref, ok := a.flag("item"); ok {
		itemID = s.item(ref).ID
	}
	if t, ok := a.flag("task"); ok {
		n = taskN(t)
	}
	if len(s.gateRows()) == 0 {
		die("no gate checks configured (factory gate config <name> '<cmd>')")
	}
	ok, summary := runGate(s, worktreeTop(), itemID, n, a.Pos)
	fmt.Println(summary)
	if !ok {
		os.Exit(1)
	}
}

func shellQuote(s string) string {
	if s != "" && regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`).MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func cmdTest(a Args) {
	s := cur()
	if len(a.Pos) == 0 {
		die("usage: factory test <path or pattern>")
	}
	var cmd string
	if err := s.db.QueryRow("SELECT cmd FROM gate WHERE name='test_focused'").Scan(&cmd); err != nil {
		die("no test_focused configured (factory gate config test_focused '<cmd> {}')")
	}
	q := make([]string, len(a.Pos))
	for i, p := range a.Pos {
		q[i] = shellQuote(p)
	}
	args := strings.Join(q, " ")
	if strings.Contains(cmd, "{}") {
		cmd = strings.ReplaceAll(cmd, "{}", args)
	} else {
		cmd += " " + args
	}
	ctx := s.ctxFor(worktreeTop())
	ok, secs, log := runCheck(worktreeTop(), "test_focused", s.expand(cmd, ctx), newLogdir(s.root), s.envFor(ctx))
	s.recordRun(0, 0, "test_focused", ok, secs, log)
	if ok {
		fmt.Printf("PASS test_focused (%.0fs)\n", secs)
		return
	}
	fmt.Printf("FAIL test_focused (%.0fs) log: %s\n%s\n", secs, rel(s.root, log), excerpt(log))
	os.Exit(1)
}

func gateConfig(a Args) {
	s := cur()
	if _, ok := a.flag("env"); len(a.Pos) == 0 && !ok {
		gateList(s)
		return
	}
	name, cmd := a.Pos[0], a.rest(1)
	if env, ok := a.flag("env"); ok {
		name, cmd = "env:"+env, a.rest(0)
	}
	if cmd == "" {
		var c string
		if s.db.QueryRow("SELECT cmd FROM gate WHERE name=?", name).Scan(&c) != nil {
			die("no gate check %s", name)
		}
		fmt.Println(c)
		return
	}
	if cmd == "n/a" || cmd == "-" {
		s.exec("DELETE FROM gate WHERE name=?", name)
		fmt.Printf("removed %s\n", name)
		return
	}
	ord := 50
	for i, n := range gateOrder {
		if n == name {
			ord = i
		}
	}
	kf := 0
	if a.Bools["known-failing"] {
		kf = 1
	}
	s.exec(`INSERT INTO gate(name,cmd,ord,known_failing) VALUES(?,?,?,?)
	        ON CONFLICT(name) DO UPDATE SET cmd=excluded.cmd, known_failing=excluded.known_failing`, name, cmd, ord, kf)
	suffix := ""
	if kf == 1 {
		suffix = "  [known-failing]"
	}
	fmt.Printf("%s: %s%s\n", name, cmd, suffix)
}

// ---------------------------------------------------------------- reporting

func cmdStats(a Args) {
	for _, p := range projectsFor(a.Bools["all"]) {
		s := p.s
		fmt.Printf("== %s\n", filepath.Base(p.root))
		items := s.items("")
		by := map[string]int{}
		for _, it := range items {
			by[it.Status]++
		}
		var parts []string
		for _, st := range statuses {
			if by[st] > 0 {
				parts = append(parts, fmt.Sprintf("%s=%d", st, by[st]))
			}
		}
		if len(parts) == 0 {
			parts = []string{"none"}
		}
		fmt.Println("items: " + strings.Join(parts, ", "))
		var leads []float64
		for _, it := range items {
			if it.Status != "merged" {
				continue
			}
			var d float64
			if err := s.db.QueryRow("SELECT COALESCE(max(ts),0) FROM events WHERE item_id=? AND kind='status' AND msg LIKE 'merged%'", it.ID).Scan(&d); err == nil && d > 0 {
				leads = append(leads, (d-it.Created)/3600)
			}
		}
		if len(leads) > 0 {
			sort.Float64s(leads)
			fmt.Printf("lead time (created→merged): median %.1fh, n=%d\n", leads[len(leads)/2], len(leads))
		}
		var c, f, b int
		check(s.db.QueryRow("SELECT count(*), COALESCE(sum(fixes),0), COALESCE(sum(status='blocked'),0) FROM tasks").Scan(&c, &f, &b))
		if c > 0 {
			fmt.Printf("tasks: %d, fix rounds: %d (%.2f/task), blocked now: %d\n", c, f, float64(f)/float64(c), b)
		}
		rows, err := s.db.Query("SELECT name, count(*), avg(ok), avg(secs) FROM (SELECT * FROM gate_runs ORDER BY id DESC LIMIT 200) GROUP BY name")
		check(err)
		for rows.Next() {
			var name string
			var cnt int
			var pass, secs float64
			check(rows.Scan(&name, &cnt, &pass, &secs))
			fmt.Printf("gate %-13s runs=%-4d pass=%.0f%%  avg=%.0fs\n", name, cnt, pass*100, secs)
		}
		rows.Close()
	}
}

var (
	problemRe = regexp.MustCompile(`(?s)## Problem\n(.*?)(?:\n## |\z)`)
)

func exportItem(s *Store, it Item) {
	spec := s.getDoc("spec", it.ID, 0)
	problem := ""
	if m := problemRe.FindStringSubmatch(spec); m != nil {
		problem = strings.TrimSpace(m[1])
	}
	out := []string{"## " + kindScope(it) + ": " + it.Title, "", problem, "", "### Acceptance criteria"}
	for _, ac := range parseACs(spec) {
		out = append(out, fmt.Sprintf("- **%s**: %s", ac.ID, ac.Text))
	}
	out = append(out, "", "### Tasks")
	for _, t := range s.tasks(it) {
		line := fmt.Sprintf("- %s T%d %s", mark[t.Status], t.N, t.Title)
		if t.Sha != "" {
			line += " (" + t.Sha[:8] + ")"
		}
		out = append(out, line)
	}
	if msg := s.getDoc("merge-msg", it.ID, 0); msg != "" {
		out = append(out, "", "### Summary", msg)
	}
	out = append(out, "", "Status: "+it.Status)
	fmt.Println(strings.TrimSpace(strings.Join(out, "\n")))
}
