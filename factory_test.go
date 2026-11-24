package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestParseArgs(t *testing.T) {
	a := parseArgs([]string{"Rate", "limit", "-p1", "--kind", "fix", "--scope=api", "API", "--force", "-s", "ready", "--", "--literal"}, "force")
	if got := a.rest(0); got != "Rate limit API --literal" {
		t.Fatalf("positionals: %q", got)
	}
	if a.Flags["prio"] != "1" || a.Flags["kind"] != "fix" || a.Flags["scope"] != "api" || a.Flags["status"] != "ready" {
		t.Fatalf("flags: %v", a.Flags)
	}
	if !a.Bools["force"] {
		t.Fatal("bool flag not set")
	}
	b := parseArgs([]string{"--force=false", "-a", "-5"}, "force", "all")
	if b.Bools["force"] || !b.Bools["all"] || !reflect.DeepEqual(b.Pos, []string{"-5"}) {
		t.Fatalf("bools/negative number: %+v", b)
	}
}

func TestE2EScriptsExitOnFailure(t *testing.T) {
	paths, err := filepath.Glob("test/e2e/*.sh")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, path := range paths {
		if filepath.Base(path) == "stub-agent.sh" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		line := ""
		lines := strings.Split(string(data), "\n")
		for i := len(lines) - 1; i >= 0; i-- {
			if line = strings.TrimSpace(lines[i]); line != "" {
				break
			}
		}
		if line != "exit $fail" {
			t.Errorf("%s: last line must be exit $fail, got %q", path, line)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no e2e scripts found in test/e2e")
	}
}

func TestParseReturn(t *testing.T) {
	out := "thinking...\nSTATUS: blocked\nmore\n**STATUS:** done\n`TASK: T2`\n> NOTE: added limiter\nDECISION: tasks-added\n"
	m := parseReturn(out)
	if m["STATUS"] != "done" || m["TASK"] != "T2" || m["NOTE"] != "added limiter" || firstWord(m["DECISION"]) != "tasks-added" {
		t.Fatalf("parseReturn: %v", m)
	}
}

func TestClaudeAllowedTools(t *testing.T) {
	tools := func(name string) []string {
		b, err := opencodeFS.ReadFile("agents/factory-" + name + ".md")
		if err != nil {
			t.Fatal(err)
		}
		front, _ := splitFrontmatter(string(b))
		return claudeAllowedTools(front)
	}
	has := func(l []string, s string) bool {
		for _, x := range l {
			if x == s {
				return true
			}
		}
		return false
	}
	b, v, o := tools("builder"), tools("verifier"), tools("overseer")
	for _, want := range []string{"Edit", "Write", "Read", "Grep", "Glob", "Bash(factory *)", "Bash(rg *)"} {
		if !has(b, want) {
			t.Fatalf("builder missing %q: %v", want, b)
		}
	}
	if !has(v, "Bash(git show*)") || has(v, "Edit") || has(v, "Write") {
		t.Fatalf("verifier: %v", v)
	}
	if !has(o, "Bash(git commit*)") || has(o, "Bash(git push*)") || has(o, "Bash(git reset*)") {
		t.Fatalf("overseer: %v", o)
	}
	for _, l := range [][]string{b, v, o} {
		if has(l, "Bash(*)") {
			t.Fatalf("wildcard bash allowed: %v", l)
		}
		seen := map[string]bool{}
		for _, x := range l {
			if seen[x] {
				t.Fatalf("duplicate %q: %v", x, l)
			}
			seen[x] = true
		}
	}
}

func TestSplitFrontmatter(t *testing.T) {
	src, err := opencodeFS.ReadFile("agents/factory-builder.md")
	if err != nil {
		t.Fatal(err)
	}
	front, body := splitFrontmatter(string(src))
	if !strings.HasPrefix(body, "You are a **factory builder**") || strings.Contains(body, "permission:") {
		t.Fatalf("body: %.80q", body)
	}
	if !strings.Contains(front, "permission:") {
		t.Fatalf("front: %q", front)
	}
	if f, b := splitFrontmatter("no frontmatter\n---\nhere"); f != "" || b != "no frontmatter\n---\nhere" {
		t.Fatalf("plain: %q %q", f, b)
	}
}

func TestNextRunnable(t *testing.T) {
	ts := []Task{
		{N: 1, Status: "built"},
		{N: 2, Status: "todo", Deps: "T1"},
		{N: 3, Status: "todo"},
		{N: 4, Status: "todo", Deps: "T2,T3"},
	}
	if got := nextRunnable(ts, false, nil); got == nil || got.N != 3 {
		t.Fatalf("want T3 (T2 waits on unverified T1), got %+v", got)
	}
	if got := nextRunnable(ts, true, nil); got == nil || got.N != 2 {
		t.Fatalf("pipeline: want T2, got %+v", got)
	}
	if got := nextRunnable(ts, false, set("T3")); got != nil {
		t.Fatalf("skip: want nil, got %+v", got)
	}
	if allVerified(ts) || !allVerified([]Task{{N: 1, Status: "verified"}}) || allVerified(nil) {
		t.Fatal("allVerified")
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return openStore(t.TempDir(), true)
}

func addItem(s *Store, title string) Item {
	r := s.exec("INSERT INTO items(slug,title,status,created,updated) VALUES(?,?,?,?,?)", slugify(title), title, "backlog", now(), now())
	id, _ := r.LastInsertId()
	return s.item(strconv.FormatInt(id, 10))
}

func TestReadyAndStatusTransitions(t *testing.T) {
	s := newTestStore(t)
	it := addItem(s, "Rate limit")
	if p := readyProblems(s, it); len(p) != 2 {
		t.Fatalf("want no-spec + no-tasks problems, got %v", p)
	}
	s.putDoc("spec", "## Acceptance criteria\n- **AC1**: one\n- **AC2**: two", it.ID, 0)
	s.exec("INSERT INTO tasks(item_id,n,title,covers) VALUES(?,1,'a','AC1')", it.ID)
	if p := readyProblems(s, it); len(p) != 1 || p[0] != "ACs not covered by any task: AC2" {
		t.Fatalf("uncovered: %v", p)
	}
	s.exec("INSERT INTO tasks(item_id,n,title,covers,deps) VALUES(?,2,'b','AC2','T1')", it.ID)
	if p := readyProblems(s, it); len(p) != 0 {
		t.Fatalf("should be ready: %v", p)
	}

	s.setStatus(it, "building", "")
	s.exec("UPDATE tasks SET status='verified' WHERE item_id=?", it.ID)
	s.refreshItemStatus(it.ID)
	if st := s.item("1").Status; st != "judging" {
		t.Fatalf("all verified → judging, got %s", st)
	}
	s.exec("INSERT INTO tasks(item_id,n,title) VALUES(?,3,'gap')", it.ID)
	s.refreshItemStatus(it.ID)
	if st := s.item("1").Status; st != "building" {
		t.Fatalf("task added during judging → building, got %s", st)
	}
	s.park(s.item("1"), "because")
	if it := s.item("1"); it.Status != "parked" || it.ParkReason != "because" {
		t.Fatalf("park: %+v", it)
	}
}

func TestMigrateV1(t *testing.T) {
	s := newTestStore(t)
	s.exec("INSERT INTO items(slug,title,kind,status) VALUES('a','A','feature','plan-approved'),('b','B','bug','done')")
	s.exec("UPDATE meta SET value='1' WHERE key='schema'")
	s.migrate()
	if a, b := s.item("1"), s.item("2"); a.Status != "ready" || a.Kind != "feat" || b.Status != "merged" || b.Kind != "fix" {
		t.Fatalf("migration: %+v %+v", a, b)
	}
}

func TestRemapRange(t *testing.T) {
	cases := []struct {
		name         string
		start, end   int
		hunks        []hunk
		wantS, wantE int
		wantOverlap  bool
	}{
		{"insert above shifts", 7, 9, []hunk{{0, 0, 1, 2}}, 9, 11, false},
		{"delete above shifts up", 7, 9, []hunk{{2, 3, 1, 0}}, 4, 6, false},
		{"replace above with more lines", 7, 9, []hunk{{3, 1, 3, 4}}, 10, 12, false},
		{"change inside overlaps", 4, 6, []hunk{{5, 1, 5, 1}}, 4, 6, true},
		{"insert inside overlaps", 4, 6, []hunk{{4, 0, 5, 1}}, 4, 6, true},
		{"insert right after end is fine", 4, 6, []hunk{{6, 0, 7, 2}}, 4, 6, false},
		{"change below ignored", 4, 6, []hunk{{10, 2, 10, 5}}, 4, 6, false},
		{"mixed", 10, 12, []hunk{{0, 0, 1, 2}, {5, 1, 7, 1}, {20, 1, 22, 0}}, 12, 14, false},
	}
	for _, c := range cases {
		s, e, o := remapRange(c.start, c.end, c.hunks)
		if s != c.wantS || e != c.wantE || o != c.wantOverlap {
			t.Errorf("%s: got %d-%d overlap=%v, want %d-%d overlap=%v", c.name, s, e, o, c.wantS, c.wantE, c.wantOverlap)
		}
	}
	h := parseHunks("diff --git a/x b/x\n@@ -0,0 +1,2 @@\n+a\n+b\n@@ -5 +7 @@\n-x\n+y\n")
	if len(h) != 2 || h[0] != (hunk{0, 0, 1, 2}) || h[1] != (hunk{5, 1, 7, 1}) {
		t.Fatalf("parseHunks: %+v", h)
	}
}

func TestParseRefs(t *testing.T) {
	body := "## Goal\nx `not/a/ref.go:1-2`\n\n## Read first (only these)\n" +
		"- `src/a.ts:40-120`: why\n- `src/b.ts` (`makeThing`): pattern\n- `src/c.ts (helper)`\n- `docs/plain.md`\n- run `factory test x`\n\n## Do\n- `src/d.ts:1-2`\n"
	refs := parseRefs(body)
	if len(refs) != 4 {
		t.Fatalf("want 4 refs, got %+v", refs)
	}
	if r := refs[0]; r.Path != "src/a.ts" || r.Start != 40 || r.End != 120 || r.Raw != "`src/a.ts:40-120`" {
		t.Errorf("range ref: %+v", r)
	}
	if r := refs[1]; r.Path != "src/b.ts" || r.Symbol != "makeThing" {
		t.Errorf("symbol ref: %+v", r)
	}
	if r := refs[2]; r.Path != "src/c.ts" || r.Symbol != "helper" {
		t.Errorf("inline symbol ref: %+v", r)
	}
	if r := refs[3]; r.Path != "docs/plain.md" || r.Start != 0 || r.Symbol != "" {
		t.Errorf("plain ref: %+v", r)
	}
	if got := newRaw(refs[0], 42, 122); got != "`src/a.ts:42-122`" {
		t.Errorf("newRaw: %s", got)
	}
}

func TestAgentsLabel(t *testing.T) {
	s := newTestStore(t)
	t.Setenv("FACTORY_AGENT_CMD", "")
	if got := agentsLabel(s); got != "opencode" {
		t.Fatalf("default: got %q", got)
	}
	s.setCfg("backend", "claude")
	if got := agentsLabel(s); got != "claude/opus" {
		t.Fatalf("claude default model: got %q", got)
	}
	s.setCfg("claude_model", "sonnet")
	if got := agentsLabel(s); got != "claude/sonnet" {
		t.Fatalf("claude model: got %q", got)
	}
	t.Setenv("FACTORY_AGENT_CMD", "x {message}")
	if got := agentsLabel(s); got != "custom (FACTORY_AGENT_CMD)" {
		t.Fatalf("custom: got %q", got)
	}
}

func TestAgentCmdTemplate(t *testing.T) {
	s := newTestStore(t)
	t.Setenv("OPENCODE_CONFIG_DIR", t.TempDir()) // use the embedded agent files
	r := &runner{s: s, root: s.root, backend: "claude"}
	t.Setenv("FACTORY_AGENT_CMD", "x {message}")
	if got, err := r.agentCmdTemplate("factory-builder"); err != nil || got != "x {message}" {
		t.Fatalf("FACTORY_AGENT_CMD not preferred: %q %v", got, err)
	}
	t.Setenv("FACTORY_AGENT_CMD", "")
	r.backend = "opencode"
	if got, err := r.agentCmdTemplate("factory-builder"); err != nil || got != defaultAgentCmd {
		t.Fatalf("opencode: %q %v", got, err)
	}
	r.backend = "claude"
	got, err := r.agentCmdTemplate("factory-builder")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"claude -p", "--permission-mode dontAsk", "--no-session-persistence",
		"--append-system-prompt-file", "--settings", "{message}"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
	if !strings.Contains(got, "--model 'opus'") && !strings.Contains(got, "--model opus") {
		t.Fatalf("model: %q", got)
	}
	dir := filepath.Join(s.root, ".factory", "agents")
	b, err := os.ReadFile(filepath.Join(dir, "factory-builder.settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Permissions struct {
			Allow []string `json:"allow"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("settings json: %v\n%s", err, b)
	}
	found := false
	for _, a := range cfg.Permissions.Allow {
		found = found || a == "Bash(factory *)"
	}
	if !found {
		t.Fatalf("allow: %v", cfg.Permissions.Allow)
	}
	body, err := os.ReadFile(filepath.Join(dir, "factory-builder.prompt.md"))
	if err != nil || strings.HasPrefix(string(body), "---") || len(body) == 0 {
		t.Fatalf("prompt body: %v\n%.200s", err, body)
	}
}
