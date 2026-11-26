package factory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
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

func TestInitInstructionsSingleSourced(t *testing.T) {
	steps := tpl("init")
	for _, want := range []string{"factory gate config", "factory gate", "factory write map"} {
		if !strings.Contains(steps, want) {
			t.Fatalf("templates/init.md missing %q", want)
		}
	}
	for _, path := range []string{"commands/factory-init.md", "skills/factory-feature/SKILL.md"} {
		b, err := opencodeFS.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "factory internal template init") {
			t.Fatalf("%s does not reference `factory internal template init`", path)
		}
	}
	cmd, _ := opencodeFS.ReadFile("commands/factory-init.md")
	if strings.Contains(string(cmd), "factory write map") {
		t.Fatal("commands/factory-init.md duplicates the setup steps")
	}
}

func TestSkillPlansWithImport(t *testing.T) {
	if commands["import"].group != "skill" {
		t.Fatal("`factory help skill` does not list import")
	}
	b, err := opencodeFS.ReadFile("skills/factory-feature/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{"factory import - --ready", "=== spec", "=== T1:", "factory cat plan", "**claude**: may be lighter"} {
		if !strings.Contains(s, want) {
			t.Fatalf("SKILL.md missing %q", want)
		}
	}
	if strings.Contains(s, "factory task-add 4") {
		t.Fatal("SKILL.md full-plan flow still uses separate task-add calls")
	}
}

func TestSetupHintsMentionBothRoutes(t *testing.T) {
	for _, want := range []string{"factory-feature skill", "/factory-init"} {
		if !strings.Contains(commands["init"].usage, want) {
			t.Fatalf("init usage missing %q: %s", want, commands["init"].usage)
		}
	}
	if !strings.Contains(commands["template"].usage, " init") {
		t.Fatalf("template usage does not list init: %s", commands["template"].usage)
	}
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(readme), "skill init mode (Claude Code) or /factory-init (opencode)") {
		t.Fatal("README setup reference section does not describe both routes")
	}
}

func TestE2EScriptsExitOnFailure(t *testing.T) {
	paths, err := filepath.Glob("../../test/e2e/*.sh")
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

func TestParseClaudeJSON(t *testing.T) {
	js := `{"result":"hi\nSTATUS: done","total_cost_usd":0.5,"num_turns":3,"usage":{"input_tokens":2,"output_tokens":11,"cache_read_input_tokens":100,"cache_creation_input_tokens":50}}`
	text, u, calls, ok := parseClaudeJSON("\n" + js + "\n")
	if !ok || !strings.Contains(text, "STATUS: done") {
		t.Fatalf("parse: ok=%v text=%q", ok, text)
	}
	if want := (agentUsage{Cost: 0.5, In: 2, Out: 11, CacheRead: 100, CacheCreate: 50, Turns: 3}); u != want {
		t.Fatalf("usage: %+v want %+v", u, want)
	}
	if len(calls) != 0 {
		t.Fatalf("single JSON calls: %+v", calls)
	}
	if _, _, _, ok := parseClaudeJSON("STATUS: done"); ok {
		t.Fatal("plain text parsed as JSON")
	}
	if _, _, _, ok := parseClaudeJSON(`{"foo":1}`); ok {
		t.Fatal("JSON without result accepted")
	}
}

func TestParseClaudeStream(t *testing.T) {
	stream := `{"type":"system","subtype":"init"}
{"type":"assistant","message":{"content":[{"type":"text","text":"ok"},{"type":"tool_use","name":"Bash","input":{"command":"factory brief build 1 T1"}}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"x.go"}}]}}
{"type":"result","result":"STATUS: done","total_cost_usd":0.5,"num_turns":3,"usage":{"input_tokens":2,"output_tokens":11}}
`
	text, u, calls, ok := parseClaudeJSON(stream)
	if !ok || text != "STATUS: done" || u.Cost != 0.5 || u.Turns != 3 || u.Out != 11 {
		t.Fatalf("parse: ok=%v text=%q u=%+v", ok, text, u)
	}
	want := []toolCall{{"Bash", "factory brief build 1 T1"}, {"Read", "x.go"}}
	if len(calls) != 2 || calls[0] != want[0] || calls[1] != want[1] {
		t.Fatalf("calls: %+v", calls)
	}
	if got := toolCounts([]toolCall{{"Read", ""}, {"Bash", ""}, {"Edit", ""}, {"Bash", ""}}); got != "Bash:2 Edit:1 Read:1" {
		t.Fatalf("counts: %q", got)
	}
	if got := formatTools(want); got != "\n--- tools ---\n1. Bash factory brief build 1 T1\n2. Read x.go\n" {
		t.Fatalf("tools log: %q", got)
	}
	if formatTools(nil) != "" {
		t.Fatal("empty calls should log nothing")
	}
}

// An error result (no "result" field) must not leave the raw stream as text:
// tool_result events can carry whole files into retry notes.
func TestParseClaudeStreamNoResult(t *testing.T) {
	big := strings.Repeat("SECRETFILE ", 50)
	stream := `{"type":"system","subtype":"init"}
{"type":"assistant","message":{"content":[{"type":"text","text":"working on it"},{"type":"tool_use","name":"Read","input":{"file_path":"x.go"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","content":"` + big + `"}]}}
{"type":"result","subtype":"error_max_budget_usd","is_error":true,"total_cost_usd":2.5,"num_turns":9}
`
	text, u, calls, ok := parseClaudeJSON(stream)
	if !ok || strings.Contains(text, "SECRETFILE") || strings.Contains(text, `"type"`) {
		t.Fatalf("raw stream kept: ok=%v text=%q", ok, text)
	}
	if !strings.Contains(text, "working on it") || !strings.Contains(text, "error_max_budget_usd") {
		t.Fatalf("text: %q", text)
	}
	if u.Cost != 2.5 || u.Turns != 9 {
		t.Fatalf("usage: %+v", u)
	}
	if len(calls) != 1 || calls[0].Name != "Read" {
		t.Fatalf("calls: %+v", calls)
	}
	// Killed run: no result line at all; calls still returned.
	killed := strings.Join(strings.Split(stream, "\n")[:3], "\n")
	text, _, calls, ok = parseClaudeJSON(killed)
	if !ok || text != "working on it" || len(calls) != 1 {
		t.Fatalf("killed: ok=%v text=%q calls=%+v", ok, text, calls)
	}
	if _, _, _, ok := parseClaudeJSON("line one\nline two"); ok {
		t.Fatal("plain multi-line text parsed as stream")
	}
}

func TestParseAgentOutputNonClaude(t *testing.T) {
	// JSON on its own line: unconditional stream parsing would treat it as
	// claude output and drop the STATUS block.
	out := "{\"type\":\"module\"}\nSTATUS: done\nTASK: T1\n"
	text, _, calls := parseAgentOutput(false, out)
	if text != out || len(calls) != 0 {
		t.Fatalf("non-claude output altered: %q", text)
	}
	if parseReturn(text)["STATUS"] != "done" {
		t.Fatalf("STATUS lost: %q", text)
	}
	js := `{"type":"result","result":"STATUS: done"}`
	if text, _, _ := parseAgentOutput(true, js); text != "STATUS: done" {
		t.Fatalf("claude output not parsed: %q", text)
	}
}

func TestTopTools(t *testing.T) {
	for _, c := range []struct {
		counts map[string]int
		runs   int
		want   string
	}{
		{map[string]int{"Bash": 9, "Read": 5, "Edit": 3, "Grep": 1}, 2, "Bash 4.5/run, Read 2.5/run, Edit 1.5/run"},
		{map[string]int{"Read": 2, "Bash": 2}, 1, "Bash 2.0/run, Read 2.0/run"},
		{nil, 3, ""},
		{map[string]int{"Bash": 1}, 0, ""},
	} {
		if got := topTools(c.counts, c.runs, 3); got != c.want {
			t.Errorf("topTools(%v, %d) = %q want %q", c.counts, c.runs, got, c.want)
		}
	}
}

func TestRecordAgentRun(t *testing.T) {
	s := newTestStore(t)
	s.recordAgentRun(6, "build T1", "factory-builder", "opus", 12.5,
		agentUsage{Cost: 0.25, In: 1, Out: 2, CacheRead: 3, CacheCreate: 4, Turns: 5}, "Bash:2")
	var label, model, tools string
	var cost float64
	var turns, cacheCreate int
	err := s.db.QueryRow("SELECT label, model, cost_usd, turns, cache_creation, tools FROM agent_runs WHERE item_id=6").
		Scan(&label, &model, &cost, &turns, &cacheCreate, &tools)
	if err != nil {
		t.Fatal(err)
	}
	if label != "build T1" || model != "opus" || cost != 0.25 || turns != 5 || cacheCreate != 4 || tools != "Bash:2" {
		t.Fatalf("row: %q %q %v %d %d %q", label, model, cost, turns, cacheCreate, tools)
	}
}

func TestParseReturn(t *testing.T) {
	out := "thinking...\nSTATUS: blocked\nmore\n**STATUS:** done\n`TASK: T2`\n> NOTE: added limiter\nDECISION: tasks-added\n"
	m := parseReturn(out)
	if m["STATUS"] != "done" || m["TASK"] != "T2" || m["NOTE"] != "added limiter" || firstWord(m["DECISION"]) != "tasks-added" {
		t.Fatalf("parseReturn: %v", m)
	}
}

func TestClaudeToolList(t *testing.T) {
	list := func(name string) string {
		b, err := opencodeFS.ReadFile("agents/factory-" + name + ".md")
		if err != nil {
			t.Fatal(err)
		}
		front, _ := splitFrontmatter(string(b))
		return claudeToolList(claudeAllowedTools(front))
	}
	if got := list("builder"); got != "Read,Grep,Glob,Edit,Write,Bash" {
		t.Fatalf("builder: %q", got)
	}
	if got := list("verifier"); got != "Read,Grep,Glob,Bash" {
		t.Fatalf("verifier: %q", got)
	}
	if got := claudeToolList([]string{"Read", "Bash(a *)", "Edit", "Bash(b)"}); got != "Read,Bash,Edit" {
		t.Fatalf("order/dups: %q", got)
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

func TestBuilderCanRemoveFiles(t *testing.T) {
	b, err := opencodeFS.ReadFile("agents/factory-builder.md")
	if err != nil {
		t.Fatal(err)
	}
	front, _ := splitFrontmatter(string(b))
	got := strings.Join(claudeAllowedTools(front), "\n")
	// `git *` covers git rm and git mv (and every other git subcommand).
	for _, want := range []string{"Bash(git *)"} {
		if !strings.Contains("\n"+got+"\n", "\n"+want+"\n") {
			t.Fatalf("builder missing %q: %v", want, got)
		}
	}
}

func TestE2EScriptsCleanUp(t *testing.T) {
	files, _ := filepath.Glob("../../test/e2e/*.sh")
	if len(files) == 0 {
		t.Fatal("no e2e scripts")
	}
	for _, f := range files {
		if filepath.Base(f) == "stub-agent.sh" {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		if !strings.Contains(s, `[ -z "${FACTORY_E2E_DIR:-}" ] && trap 'rm -rf "$SP"' EXIT`) {
			t.Errorf("%s: no EXIT trap removing $SP", f)
		}
	}
}

func TestBuilderPromptCodeHint(t *testing.T) {
	src, err := opencodeFS.ReadFile("agents/factory-builder.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "## Code") {
		t.Fatal("builder prompt should say Read-first code is already in the brief's ## Code section")
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
	if got := nextRunnable(ts, nil); got == nil || got.N != 3 {
		t.Fatalf("want T3 (T2 waits on unverified T1), got %+v", got)
	}
	if got := nextRunnable(ts, set("T3")); got != nil {
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
	s.setCfg("claude_model_builder", "sonnet")
	if got := agentsLabel(s); got != "claude/opus (builder sonnet)" {
		t.Fatalf("claude overrides: got %q", got)
	}
	s.setCfg("claude_model_verifier", "haiku")
	if got := agentsLabel(s); got != "claude/opus (builder sonnet, verifier haiku)" {
		t.Fatalf("claude overrides: got %q", got)
	}
	s.setCfg("claude_model", "sonnet")
	if got := agentsLabel(s); got != "claude/sonnet (verifier haiku)" {
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
		"--append-system-prompt-file", "--settings", "--output-format stream-json --verbose", "{message}"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
	if !strings.Contains(got, "--model 'opus'") && !strings.Contains(got, "--model opus") {
		t.Fatalf("model: %q", got)
	}
	if strings.Contains(got, "--max-budget-usd") {
		t.Fatalf("budget flag without claude_max_usd: %q", got)
	}
	for _, want := range []string{"--strict-mcp-config", "--disable-slash-commands", "--setting-sources project",
		"--tools " + shellQuote("Read,Grep,Glob,Edit,Write,Bash")} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
	if !strings.HasSuffix(got, " -- {message}") {
		t.Fatalf("message not after --: %q", got)
	}
	s.setCfg("claude_max_usd", "2")
	if b, err := r.agentCmdTemplate("factory-builder"); err != nil ||
		!strings.Contains(b, "--max-budget-usd "+shellQuote("2")+" -- {message}") {
		t.Fatalf("claude_max_usd: %q %v", b, err)
	}
	s.setCfg("claude_max_usd", "-")
	s.setCfg("claude_model_builder", "sonnet")
	if b, err := r.agentCmdTemplate("factory-builder"); err != nil || !strings.Contains(b, "--model "+shellQuote("sonnet")) {
		t.Fatalf("builder override: %q %v", b, err)
	}
	if v, err := r.agentCmdTemplate("factory-verifier"); err != nil || !strings.Contains(v, "--model "+shellQuote("opus")) {
		t.Fatalf("verifier fallback: %q %v", v, err)
	}
	s.setCfg("claude_effort_builder", "medium")
	if b, err := r.agentCmdTemplate("factory-builder"); err != nil ||
		!strings.Contains(b, "--effort "+shellQuote("medium")+" -- {message}") {
		t.Fatalf("builder effort: %q %v", b, err)
	}
	if v, err := r.agentCmdTemplate("factory-verifier"); err != nil || strings.Contains(v, "--effort") {
		t.Fatalf("verifier effort unset: %q %v", v, err)
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

func TestAgentCmdTemplateConcurrent(t *testing.T) {
	s := newTestStore(t)
	t.Setenv("OPENCODE_CONFIG_DIR", t.TempDir()) // use the embedded agent files
	t.Setenv("FACTORY_AGENT_CMD", "")
	r := &runner{s: s, root: s.root, backend: "claude"}
	dir := filepath.Join(s.root, ".factory", "agents")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.agentCmdTemplate("factory-builder"); err != nil {
				t.Errorf("agentCmdTemplate: %v", err)
				return
			}
			b, err := os.ReadFile(filepath.Join(dir, "factory-builder.settings.json"))
			var v any
			if err != nil || json.Unmarshal(b, &v) != nil {
				t.Errorf("settings torn: %v %q", err, b)
			}
			body, err := os.ReadFile(filepath.Join(dir, "factory-builder.prompt.md"))
			if err != nil || len(body) == 0 {
				t.Errorf("prompt empty: %v", err)
			}
		}()
	}
	wg.Wait()
	if left, _ := filepath.Glob(filepath.Join(dir, "*.tmp")); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
}
