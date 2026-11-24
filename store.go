package main

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS meta(key TEXT PRIMARY KEY, value TEXT);
CREATE TABLE IF NOT EXISTS items(
  id INTEGER PRIMARY KEY, slug TEXT NOT NULL, title TEXT NOT NULL, kind TEXT DEFAULT 'feat',
  priority INTEGER DEFAULT 2, status TEXT DEFAULT 'backlog', base TEXT,
  created REAL, updated REAL, scope TEXT DEFAULT '', park_reason TEXT DEFAULT '', merged_sha TEXT DEFAULT '');
CREATE TABLE IF NOT EXISTS tasks(
  item_id INTEGER, n INTEGER, title TEXT, covers TEXT DEFAULT '', deps TEXT DEFAULT '',
  status TEXT DEFAULT 'todo', sha TEXT, fixes INTEGER DEFAULT 0, note TEXT, updated REAL,
  PRIMARY KEY(item_id, n));
CREATE TABLE IF NOT EXISTS docs(
  item_id INTEGER DEFAULT 0, task_n INTEGER DEFAULT 0, kind TEXT, body TEXT, updated REAL,
  PRIMARY KEY(item_id, task_n, kind));
CREATE TABLE IF NOT EXISTS events(
  id INTEGER PRIMARY KEY, ts REAL, item_id INTEGER, task_n INTEGER DEFAULT 0, kind TEXT, msg TEXT);
CREATE TABLE IF NOT EXISTS gate(name TEXT PRIMARY KEY, cmd TEXT, ord INTEGER, known_failing INTEGER DEFAULT 0);
CREATE TABLE IF NOT EXISTS factory_tags(tag TEXT PRIMARY KEY, sha TEXT, item_id INTEGER DEFAULT 0);
CREATE TABLE IF NOT EXISTS gate_runs(
  id INTEGER PRIMARY KEY, ts REAL, item_id INTEGER DEFAULT 0, task_n INTEGER DEFAULT 0,
  name TEXT, ok INTEGER, secs REAL, log TEXT);
`

var (
	statuses     = []string{"backlog", "ready", "building", "judging", "merging", "merged", "parked", "dropped"}
	inFlight     = []string{"building", "judging", "merging"}
	openStatuses = []string{"backlog", "ready", "building", "judging", "merging", "parked"}
	kinds        = []string{"feat", "fix", "chore", "refactor", "perf", "test", "docs", "build", "ci"}
	nextStep     = map[string]string{
		"backlog":  "finish it with /factory-feature, then `factory ready %d`",
		"ready":    "`factory run`",
		"building": "`factory run` (in progress)",
		"judging":  "`factory run` (in progress)",
		"merging":  "`factory run` (in progress)",
		"merged":   "`factory land` to fast-forward main",
		"parked":   "inspect the worktree (`factory show %d`), fix (or `/factory-feature refresh`), then `factory set %[1]d --status ready`",
		"dropped":  "-",
	}
	taskStatuses = []string{"todo", "built", "failed", "verified", "blocked"}
	mark         = map[string]string{"todo": "[ ]", "built": "[~]", "failed": "[-]", "verified": "[x]", "blocked": "[!]"}
	projectDocs  = set("map", "lessons")
	itemDocs     = set("spec", "notes", "merge-msg")
	taskDocs     = set("task", "report", "verify")
	templateFor  = map[string]string{"spec": "spec", "task": "task", "report": "report", "verify": "verify",
		"map": "map", "lessons": "lessons"}
	acRe   = regexp.MustCompile(`\*\*(AC\d+)\*\*\s*:?\s*(.+)`)
	taskRe = regexp.MustCompile(`^[Tt]?(\d+)$`)
)

type Item struct {
	ID, Priority, Slot                                            int
	Slug, Title, Kind, Status, Base, Scope, ParkReason, MergedSha string
	After                                                         string // comma-separated item ids that must merge first
	Filler                                                        bool
	Created, Updated                                              float64
}

type Task struct {
	ItemID, N, Fixes                       int
	Partial                                bool
	Title, Covers, Deps, Status, Sha, Note string
	Updated                                float64
}

type Store struct {
	db   *sql.DB
	root string
}

func set(xs ...string) map[string]bool {
	m := map[string]bool{}
	for _, x := range xs {
		m[x] = true
	}
	return m
}

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "factory: "+format+"\n", a...)
	os.Exit(1)
}

func check(err error) {
	if err != nil {
		die("%v", err)
	}
}

func now() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func fid(n int) string { return fmt.Sprintf("%03d", n) }

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func gitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func git(dir string, args ...string) string {
	out, err := gitOut(dir, args...)
	check(err)
	return out
}

// findRoot returns the main repo root (shared by worktrees), else the cwd.
func findRoot() string {
	wd, _ := os.Getwd()
	if c, err := gitOut(wd, "rev-parse", "--path-format=absolute", "--git-common-dir"); err == nil {
		if filepath.Base(c) == ".git" {
			return filepath.Dir(c)
		}
		if top, err := gitOut(wd, "rev-parse", "--show-toplevel"); err == nil {
			return top
		}
	}
	return wd
}

func dbPath(root string) string { return filepath.Join(root, ".factory", "factory.db") }

func openStore(root string, create bool) *Store {
	p := dbPath(root)
	if _, err := os.Stat(p); err != nil && !create {
		die("no factory db in %s (run `factory init`)", root)
	}
	check(os.MkdirAll(filepath.Dir(p), 0o755))
	db, err := sql.Open("sqlite", p+"?_pragma=busy_timeout(15000)&_pragma=journal_mode(WAL)")
	check(err)
	db.SetMaxOpenConns(1)
	_, err = db.Exec(schema + refsSchema)
	check(err)
	st := &Store{db, root}
	st.migrate()
	return st
}

// migrate upgrades databases created by older versions of factory.
func (s *Store) migrate() {
	for _, c := range [][3]string{
		{"items", "scope", "TEXT DEFAULT ''"}, {"items", "park_reason", "TEXT DEFAULT ''"}, {"items", "merged_sha", "TEXT DEFAULT ''"},
		{"items", "slot", "INTEGER DEFAULT 0"}, {"items", "after_ids", "TEXT DEFAULT ''"}, {"items", "filler", "INTEGER DEFAULT 0"},
		{"tasks", "partial", "INTEGER DEFAULT 0"},
	} {
		if !s.hasColumn(c[0], c[1]) {
			s.exec("ALTER TABLE " + c[0] + " ADD COLUMN " + c[1] + " " + c[2])
		}
	}
	var v string
	if s.db.QueryRow("SELECT value FROM meta WHERE key='schema'").Scan(&v) != nil || v < "2" {
		// v1 statuses/kinds → v2
		s.exec(`UPDATE items SET status = CASE status
			WHEN 'spec-draft' THEN 'backlog' WHEN 'spec-approved' THEN 'backlog' WHEN 'plan-draft' THEN 'backlog'
			WHEN 'plan-approved' THEN 'ready' WHEN 'built' THEN 'judging' WHEN 'reviewed' THEN 'judging'
			WHEN 'done' THEN 'merged' ELSE status END`)
		s.exec(`UPDATE items SET kind = CASE kind WHEN 'feature' THEN 'feat' WHEN 'bug' THEN 'fix' WHEN 'spike' THEN 'chore' ELSE kind END`)
		s.exec("INSERT INTO meta(key,value) VALUES('schema','2') ON CONFLICT(key) DO UPDATE SET value='2'")
	}
}

func (s *Store) hasColumn(table, col string) bool {
	rows, err := s.db.Query("PRAGMA table_info(" + table + ")")
	check(err)
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var def any
		check(rows.Scan(&cid, &name, &typ, &notnull, &def, &pk))
		if name == col {
			return true
		}
	}
	return false
}

// configKeys are the per-project settings for `factory internal config`.
var configKeys = map[string]string{
	"worktree_root":         "where feature worktrees live (default $XDG_DATA_HOME/factory/worktrees/<project>-<hash>; relative paths are from the repo root)",
	"cache_dir":             "persistent per-project cache, exposed as {cache} / FACTORY_CACHE (default <repo>/.factory/cache)",
	"tagging":               "off | semver: annotated vX.Y.Z tags on factory squash commits (default off)",
	"post_land":             "command run in the main checkout after `factory land` (FACTORY_LANDED_RANGE is set)",
	"remote":                "remote for `factory land --push` (default origin)",
	"main_branch":           "the branch factory integrates from and lands to (default main, master, or the current branch)",
	"backend":               "opencode | claude: which CLI runs agent jobs (default opencode)",
	"claude_model":          "model for the claude backend (default opus)",
	"claude_model_overseer": "model for the overseer on the claude backend (default: claude_model)",
	"claude_model_builder":  "model for the builder on the claude backend (default: claude_model)",
	"claude_model_verifier": "model for the verifier on the claude backend (default: claude_model)",
}

func (s *Store) cfg(key, def string) string {
	k := "cfg." + key
	if key == "main_branch" {
		k = key
	}
	if v := s.meta(k); v != "" {
		return v
	}
	return def
}

func (s *Store) setCfg(key, val string) {
	k := "cfg." + key
	if key == "main_branch" {
		k = key
	}
	if val == "" || val == "-" {
		s.exec("DELETE FROM meta WHERE key=?", k)
		return
	}
	s.setMeta(k, val)
}

func (s *Store) meta(key string) string {
	var v string
	s.db.QueryRow("SELECT value FROM meta WHERE key=?", key).Scan(&v)
	return v
}

func (s *Store) setMeta(key, val string) {
	s.exec("INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, val)
}

func cur() *Store { return openStore(findRoot(), false) }

func (s *Store) exec(q string, args ...any) sql.Result {
	r, err := s.db.Exec(q, args...)
	check(err)
	return r
}

// ---------------------------------------------------------------- items

const itemCols = "id,slug,title,kind,priority,status,COALESCE(base,''),COALESCE(created,0),COALESCE(updated,0)," +
	"COALESCE(scope,''),COALESCE(park_reason,''),COALESCE(merged_sha,''),COALESCE(slot,0),COALESCE(after_ids,''),COALESCE(filler,0)"

type scanner interface{ Scan(...any) error }

func scanItem(r scanner) (Item, error) {
	var it Item
	var filler int
	err := r.Scan(&it.ID, &it.Slug, &it.Title, &it.Kind, &it.Priority, &it.Status, &it.Base, &it.Created, &it.Updated,
		&it.Scope, &it.ParkReason, &it.MergedSha, &it.Slot, &it.After, &filler)
	it.Filler = filler != 0
	return it, err
}

func (s *Store) item(ref string) Item {
	ref = strings.TrimSpace(ref)
	if n, err := strconv.Atoi(ref); err == nil {
		if it, err := scanItem(s.db.QueryRow("SELECT "+itemCols+" FROM items WHERE id=?", n)); err == nil {
			return it
		}
	}
	if it, err := scanItem(s.db.QueryRow("SELECT "+itemCols+" FROM items WHERE slug=?", ref)); err == nil {
		return it
	}
	die("no item '%s'", ref)
	return Item{}
}

func (s *Store) items(where string, args ...any) []Item {
	rows, err := s.db.Query("SELECT "+itemCols+" FROM items "+where, args...)
	check(err)
	defer rows.Close()
	var out []Item
	for rows.Next() {
		it, err := scanItem(rows)
		check(err)
		out = append(out, it)
	}
	return out
}

func inClause(xs []string) (string, []any) {
	args := make([]any, len(xs))
	for i, x := range xs {
		args[i] = x
	}
	return "(" + strings.TrimSuffix(strings.Repeat("?,", len(xs)), ",") + ")", args
}

func (s *Store) itemsWithStatus(sts []string) []Item {
	in, args := inClause(sts)
	return s.items("WHERE status IN "+in+" ORDER BY priority, id", args...)
}

func (s *Store) event(itemID int, kind, msg string, n int) {
	s.exec("INSERT INTO events(ts,item_id,task_n,kind,msg) VALUES(?,?,?,?,?)", now(), itemID, n, kind, msg)
}

func (s *Store) setStatus(it Item, status, why string) {
	if !contains(statuses, status) {
		die("bad status '%s' (one of: %s)", status, strings.Join(statuses, ", "))
	}
	if it.Status == status {
		return
	}
	s.exec("UPDATE items SET status=?, updated=? WHERE id=?", status, now(), it.ID)
	msg := status
	if why != "" {
		msg += " (" + why + ")"
	}
	s.event(it.ID, "status", msg, 0)
}

func branchName(it Item) string { return fmt.Sprintf("factory-wip/%s-%s", fid(it.ID), it.Slug) }

func next(it Item) string {
	f := nextStep[it.Status]
	if !strings.Contains(f, "%") {
		return f
	}
	return fmt.Sprintf(f, it.ID)
}

// ---------------------------------------------------------------- tasks

const taskCols = "item_id,n,title,covers,deps,status,COALESCE(sha,''),fixes,COALESCE(note,''),COALESCE(updated,0),COALESCE(partial,0)"

func scanTask(r scanner) (Task, error) {
	var t Task
	var partial int
	err := r.Scan(&t.ItemID, &t.N, &t.Title, &t.Covers, &t.Deps, &t.Status, &t.Sha, &t.Fixes, &t.Note, &t.Updated, &partial)
	t.Partial = partial != 0
	return t, err
}

func taskN(ref string) int {
	m := taskRe.FindStringSubmatch(strings.TrimSpace(ref))
	if m == nil {
		die("bad task ref '%s' (use T3 or 3)", ref)
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func (s *Store) task(it Item, ref string) Task {
	n := taskN(ref)
	t, err := scanTask(s.db.QueryRow("SELECT "+taskCols+" FROM tasks WHERE item_id=? AND n=?", it.ID, n))
	if err != nil {
		die("no task T%d in %s", n, fid(it.ID))
	}
	return t
}

func (s *Store) tasks(it Item) []Task {
	rows, err := s.db.Query("SELECT "+taskCols+" FROM tasks WHERE item_id=? ORDER BY n", it.ID)
	check(err)
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		check(err)
		out = append(out, t)
	}
	return out
}

func progress(ts []Task) string {
	if len(ts) == 0 {
		return "-"
	}
	v, b := 0, 0
	for _, t := range ts {
		switch t.Status {
		case "verified":
			v++
		case "blocked":
			b++
		}
	}
	out := fmt.Sprintf("%d/%d", v, len(ts))
	if b > 0 {
		out += fmt.Sprintf(" !%d", b)
	}
	return out
}

// refreshItemStatus keeps the item status consistent with its tasks:
// building with every task verified → judging; judging/merging with an unverified task → building.
func (s *Store) refreshItemStatus(itemID int) {
	it := s.item(strconv.Itoa(itemID))
	ts := s.tasks(it)
	all := len(ts) > 0
	for _, t := range ts {
		if t.Status != "verified" {
			all = false
		}
	}
	switch {
	case it.Status == "building" && all:
		s.setStatus(it, "judging", "all tasks verified")
	case (it.Status == "judging" || it.Status == "merging") && !all:
		s.setStatus(it, "building", "tasks reopened")
	}
}

// park parks an item and frees its slot: the worktree moves to parked-NNN-slug (see parkWorktree).
func (s *Store) park(it Item, reason string) {
	parkWorktree(s, it)
	s.exec("UPDATE items SET park_reason=?, slot=0 WHERE id=?", reason, it.ID)
	s.setStatus(it, "parked", reason)
}

// ---------------------------------------------------------------- docs

func (s *Store) getDoc(kind string, itemID, n int) string {
	var body string
	err := s.db.QueryRow("SELECT body FROM docs WHERE item_id=? AND task_n=? AND kind=?", itemID, n, kind).Scan(&body)
	if err != nil {
		return ""
	}
	return body
}

func (s *Store) putDoc(kind, body string, itemID, n int) {
	s.exec(`INSERT INTO docs(item_id,task_n,kind,body,updated) VALUES(?,?,?,?,?)
	        ON CONFLICT(item_id,task_n,kind) DO UPDATE SET body=excluded.body, updated=excluded.updated`,
		itemID, n, kind, body, now())
}

type AC struct{ ID, Text string }

func parseACs(spec string) []AC {
	var out []AC
	seen := map[string]bool{}
	for _, line := range strings.Split(spec, "\n") {
		if m := acRe.FindStringSubmatch(line); m != nil && !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, AC{m[1], strings.TrimSpace(m[2])})
		}
	}
	return out
}

func splitList(s string) []string {
	var out []string
	for _, x := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

func normTaskList(s string) string {
	var out []string
	for _, d := range splitList(s) {
		out = append(out, fmt.Sprintf("T%d", taskN(d)))
	}
	return strings.Join(out, ",")
}

func ago(ts float64) string {
	if ts == 0 {
		return "-"
	}
	d := now() - ts
	for _, u := range []struct {
		s   string
		sec float64
	}{{"d", 86400}, {"h", 3600}, {"m", 60}} {
		if d >= u.sec {
			return fmt.Sprintf("%d%s", int(d/u.sec), u.s)
		}
	}
	return "now"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// ---------------------------------------------------------------- registry

func dataDir() string {
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "factory")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".local", "share", "factory")
}

func registryPath() string { return filepath.Join(dataDir(), "projects.txt") }

func registry() []string {
	b, err := os.ReadFile(registryPath())
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func writeRegistry(paths []string) {
	check(os.MkdirAll(dataDir(), 0o755))
	body := strings.Join(paths, "\n")
	if body != "" {
		body += "\n"
	}
	check(os.WriteFile(registryPath(), []byte(body), 0o644))
}

func register(root string) {
	paths := registry()
	if !contains(paths, root) {
		writeRegistry(append(paths, root))
	}
}
