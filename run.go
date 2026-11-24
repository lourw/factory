package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const defaultAgentCmd = "opencode run --agent {agent} --auto --archived --title {title} {message}"

type runner struct {
	s         *Store
	root      string
	slots     int
	features  int
	exeDir    string
	factoryWT string

	mu          sync.Mutex
	logFile     *os.File
	procs       map[*exec.Cmd]bool
	mergePaused string
	merged      []string

	staleMode string
	backend   string          // opencode | claude
	held      map[int]string  // item id → reason (stale packets, --stale=hold)
	refreshed map[string]bool // "item:task" refreshed once this run

	running  map[int]string // item id → job label (main goroutine only)
	done     chan int
	stopping atomic.Bool
	killed   atomic.Bool
}

type action struct {
	prio  int // 0 overseer/merge, 1 verifier, 2 builder
	it    Item
	label string
	fn    func()
}

func newRunner(s *Store, slots, features int) *runner {
	exe, _ := os.Executable()
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	return &runner{s: s, root: s.root, slots: slots, features: features, exeDir: filepath.Dir(exe),
		procs: map[*exec.Cmd]bool{}, running: map[int]string{}, done: make(chan int, 16),
		staleMode: "refresh", held: map[int]string{}, refreshed: map[string]bool{}}
}

func (r *runner) log(format string, a ...any) {
	line := time.Now().Format("15:04:05") + " " + fmt.Sprintf(format, a...)
	r.mu.Lock()
	defer r.mu.Unlock()
	fmt.Println(line)
	if r.logFile != nil {
		fmt.Fprintln(r.logFile, line)
	}
}

func tag(it Item) string { return fmt.Sprintf("[%s %s]", fid(it.ID), it.Slug) }

func (r *runner) reload(it Item) Item { return r.s.item(strconv.Itoa(it.ID)) }

// ---------------------------------------------------------------- entry

func cmdRun(a Args) {
	s := cur()
	if m, ok := a.flag("main"); ok {
		s.setMeta("main_branch", m)
	}
	r := newRunner(s, a.intFlag("slots", 3), a.intFlag("features", 2))
	if m, ok := a.flag("stale"); ok {
		if m != "refresh" && m != "hold" {
			die("--stale must be refresh or hold")
		}
		r.staleMode = m
	}
	r.backend = s.cfg("backend", "opencode")
	if b, ok := a.flag("backend"); ok {
		if b != "opencode" && b != "claude" {
			die("--backend must be opencode or claude")
		}
		r.backend = b
	}
	if r.slots < 1 || r.features < 1 {
		die("--slots and --features must be ≥ 1")
	}
	if a.Bools["dry-run"] {
		r.dryRun()
		return
	}
	check(os.MkdirAll(filepath.Join(s.root, ".factory", "logs", "jobs"), 0o755))
	lf, err := os.Create(filepath.Join(s.root, ".factory", "logs", "run-"+time.Now().Format("20060102-150405")+".log"))
	check(err)
	r.logFile = lf
	defer lf.Close()

	acquireRunLock(s.root)
	defer releaseRunLock(s.root)
	r.log("run: slots=%d features=%d main=%s", r.slots, r.features, mainBranch(s))
	if n := gc(s, false, true); n > 0 {
		r.log("clean: removed %d stale worktree(s)", n)
	}
	r.factoryWT = ensureIntegration(s)
	defer teardownIntegration(s)
	r.recover()
	if r.syncFactory(Item{}) {
		r.log("factory is up to date with %s", mainBranch(s))
	}

	sigc := make(chan os.Signal, 2)
	signal.Notify(sigc, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigc)
	for {
		if !r.stopping.Load() {
			r.activate()
			r.launch()
		}
		if len(r.running) == 0 {
			break
		}
		select {
		case id := <-r.done:
			delete(r.running, id)
		case <-sigc:
			if !r.stopping.Load() {
				r.stopping.Store(true)
				r.log("stopping: waiting for %d running job(s); Ctrl-C again to kill them", len(r.running))
			} else {
				r.killed.Store(true)
				r.log("killing %d running job(s)", len(r.running))
				r.killAll()
			}
		}
	}
	r.summary()
}

// ---------------------------------------------------------------- scheduling

func (r *runner) inFlightCount() int { return len(r.s.itemsWithStatus(inFlight)) }

// activate moves ready features into building (worktree + setup) while under --features.
func (r *runner) activate() {
	for _, it := range r.s.itemsWithStatus([]string{"ready"}) {
		if r.inFlightCount() >= r.features {
			return
		}
		r.startFeature(it)
	}
}

// freeSlot returns the lowest slot number 1..--features not held by an in-flight item.
func (r *runner) freeSlot() int {
	used := map[int]bool{}
	for _, it := range r.s.itemsWithStatus(inFlight) {
		used[it.Slot] = true
	}
	n := 1
	for used[n] {
		n++
	}
	return n
}

// startFeature gives a ready feature a slot and (re)creates its worktree at that slot's fixed path:
// from its parked worktree, its existing branch, or a new branch off the factory tip.
func (r *runner) startFeature(it Item) bool {
	slot := r.freeSlot()
	wt := slotPath(r.s, slot)
	check(os.MkdirAll(filepath.Dir(wt), 0o755))
	if dirExists(wt) { // leftover from a crash or an earlier occupant
		removeWorktree(r.root, wt)
	}
	gitRun(r.root, "worktree", "prune")
	var out string
	var ok bool
	switch parked := parkedPath(r.s, it); {
	case dirExists(parked):
		out, ok = gitRun(r.root, "worktree", "move", parked, wt)
	case branchExists(r.root, branchName(it)):
		out, ok = gitRun(r.root, "worktree", "add", "-q", wt, branchName(it))
	default:
		out, ok = gitRun(r.root, "worktree", "add", "-q", "-b", branchName(it), wt, integrationBranch)
	}
	if !ok {
		r.s.park(it, "couldn't create worktree: "+firstLine(out))
		r.log("%s parked: couldn't create worktree", tag(it))
		return false
	}
	base, _ := gitRun(wt, "rev-parse", "HEAD")
	r.s.exec("UPDATE items SET base=?, slot=? WHERE id=?", base, slot, it.ID)
	it = r.reload(it)
	if ok, summary := runSetup(r.s, wt, it.ID); !ok {
		r.s.park(it, "setup failed")
		r.log("%s parked: setup failed\n%s", tag(it), summary)
		return false
	}
	r.s.setStatus(it, "building", "")
	r.log("%s started in slot %d (%s)", tag(it), slot, wt)
	return true
}

func (r *runner) mergeRunning() bool {
	for _, l := range r.running {
		if l == "merge" {
			return true
		}
	}
	return false
}

func (r *runner) launch() {
	var acts []action
	for _, it := range r.s.itemsWithStatus(inFlight) {
		if _, busy := r.running[it.ID]; busy {
			continue
		}
		if a := r.nextAction(it, false); a != nil {
			acts = append(acts, *a)
		}
	}
	sort.SliceStable(acts, func(i, j int) bool {
		if acts[i].prio != acts[j].prio {
			return acts[i].prio < acts[j].prio
		}
		if acts[i].it.Priority != acts[j].it.Priority {
			return acts[i].it.Priority < acts[j].it.Priority
		}
		return acts[i].it.ID < acts[j].it.ID
	})
	for _, a := range acts {
		if len(r.running) >= r.slots {
			return
		}
		if a.label == "merge" && r.mergeRunning() {
			continue
		}
		r.running[a.it.ID] = a.label
		id, fn := a.it.ID, a.fn
		go func() {
			defer func() { r.done <- id }()
			fn()
		}()
	}
}

// nextAction decides what an in-flight feature needs next. Unless dry, it applies
// synchronous transitions (judging, parking) and returns nil when no job is needed.
func (r *runner) nextAction(it Item, dry bool) *action {
	ts := r.s.tasks(it)
	park := func(reason string) *action {
		if dry {
			return &action{label: "park: " + reason, it: it}
		}
		r.s.park(it, reason)
		r.log("%s parked: %s", tag(it), reason)
		return nil
	}
	switch it.Status {
	case "building":
		if allVerified(ts) {
			if dry {
				return &action{prio: 0, it: it, label: "judge"}
			}
			r.s.setStatus(it, "judging", "all tasks verified")
			return r.nextAction(r.reload(it), dry)
		}
		var missing []string
		for _, t := range ts {
			if t.Status == "todo" && r.s.getDoc("task", it.ID, t.N) == "" {
				missing = append(missing, fmt.Sprintf("T%d", t.N))
			}
		}
		if len(missing) > 0 {
			return &action{prio: 0, it: it, label: "plan " + strings.Join(missing, ","), fn: func() { r.planJob(it) }}
		}
		for _, t := range ts {
			if t.Status == "failed" {
				if t.Fixes >= 2 {
					if !dry {
						r.s.exec("UPDATE tasks SET status='blocked' WHERE item_id=? AND n=?", it.ID, t.N)
					}
					return park(fmt.Sprintf("T%d still failing after %d fix rounds", t.N, t.Fixes))
				}
				t := t
				return &action{prio: 2, it: it, label: fmt.Sprintf("fix T%d", t.N), fn: func() { r.buildJob(it, t, true) }}
			}
		}
		for _, t := range ts {
			if t.Status == "built" {
				t := t
				return &action{prio: 1, it: it, label: fmt.Sprintf("verify T%d", t.N), fn: func() { r.verifyJob(it, t) }}
			}
		}
		if t := nextRunnable(ts, false, nil); t != nil {
			t := *t
			if a := r.staleGate(it, t, dry); a != nil || r.held[it.ID] != "" {
				return a
			}
			return &action{prio: 2, it: it, label: fmt.Sprintf("build T%d", t.N), fn: func() { r.buildJob(it, t, false) }}
		}
		for _, t := range ts {
			if t.Status == "blocked" {
				return park(fmt.Sprintf("T%d blocked: %s", t.N, orDash(t.Note)))
			}
		}
		if len(ts) == 0 {
			return park("no tasks")
		}
		return park("no runnable task (dependency cycle?)")
	case "judging":
		return &action{prio: 0, it: it, label: "judge", fn: func() { r.judgeJob(it) }}
	case "merging":
		r.mu.Lock()
		paused := r.mergePaused
		r.mu.Unlock()
		if paused != "" {
			return nil
		}
		return &action{prio: 0, it: it, label: "merge", fn: func() { r.mergeJob(it) }}
	}
	return nil
}

// staleGate checks a task's packet refs right before its builder launches. Shifted refs are
// auto-remapped; broken ones trigger an overseer refresh job (or hold the feature with --stale=hold).
func (r *runner) staleGate(it Item, t Task, dry bool) *action {
	results := r.s.checkTask(it.ID, t.N, !dry)
	for _, res := range results {
		if res.State == "shifted" && !dry {
			r.log("%s T%d packet ref auto-fixed: %s", tag(it), t.N, res.Detail)
		}
	}
	bad := needsModel(results)
	if len(bad) == 0 {
		delete(r.held, it.ID)
		return nil
	}
	var refs []string
	for _, b := range bad {
		refs = append(refs, b.State+" "+b.Ref.Raw)
	}
	reason := fmt.Sprintf("T%d packet stale: %s", t.N, strings.Join(refs, ", "))
	if dry {
		return &action{label: "stale: " + reason, it: it}
	}
	if r.staleMode == "hold" {
		if r.held[it.ID] == "" {
			r.log("%s held: %s", tag(it), reason)
		}
		r.held[it.ID] = reason
		return nil
	}
	key := fmt.Sprintf("%d:%d", it.ID, t.N)
	if r.refreshed[key] {
		r.s.park(it, reason+" (still stale after refresh)")
		r.log("%s parked: %s (still stale after refresh)", tag(it), reason)
		return nil
	}
	return &action{prio: 0, it: it, label: fmt.Sprintf("refresh T%d", t.N), fn: func() { r.refreshJob(it, t, key) }}
}

func (r *runner) refreshJob(it Item, t Task, key string) {
	out, alive := r.agent("factory-overseer", it, fmt.Sprintf("refresh T%d", t.N), fmt.Sprintf("refresh %d T%d", it.ID, t.N), featureWorktree(r.s, it))
	if !alive {
		return
	}
	r.refreshed[key] = true
	if firstWord(out["DECISION"]) == "park" {
		r.s.park(r.reload(it), fmt.Sprintf("T%d refresh: %s", t.N, orDash(out["REASON"])))
		r.log("%s parked by refresh: %s", tag(it), orDash(out["REASON"]))
		return
	}
	r.log("%s T%d packet refreshed", tag(it), t.N)
}

func (r *runner) dryRun() {
	fmt.Printf("dry run: slots=%d features=%d\n", r.slots, r.features)
	inflight := r.s.itemsWithStatus(inFlight)
	for _, it := range inflight {
		a := r.nextAction(it, true)
		label := "-"
		if a != nil {
			label = a.label
		}
		fmt.Printf("  %s %-9s next: %s\n", tag(it), it.Status, label)
	}
	free := r.features - len(inflight)
	for _, it := range r.s.itemsWithStatus([]string{"ready"}) {
		if free <= 0 {
			fmt.Printf("  %s ready     waits for a feature slot\n", tag(it))
			continue
		}
		free--
		fmt.Printf("  %s ready     would start (worktree + setup)\n", tag(it))
	}
}

// ---------------------------------------------------------------- agents

var returnRe = regexp.MustCompile(`^[\s>*_` + "`" + `-]*(STATUS|TASK|NOTE|VERDICT|BLOCKING|TOP|DECISION|REASON)[*_` + "`" + `]*\s*:[*_` + "`" + `]*\s*(.*?)[\s*_` + "`" + `]*$`)

// parseReturn extracts the agent's return block (last occurrence of each key wins).
func parseReturn(out string) map[string]string {
	m := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		if g := returnRe.FindStringSubmatch(l); g != nil {
			m[g[1]] = strings.TrimSpace(g[2])
		}
	}
	return m
}

func firstWord(s string) string {
	f := strings.Fields(strings.ToLower(s))
	if len(f) == 0 {
		return ""
	}
	return strings.Trim(f[0], ".,;:|")
}

func firstLine(s string) string { l, _, _ := strings.Cut(strings.TrimSpace(s), "\n"); return l }

// agent runs one headless agent process in dir and returns its parsed return block.
// alive is false when the run was killed (state must not be updated).
func (r *runner) agent(name string, it Item, label, briefArgs, dir string) (out map[string]string, alive bool) {
	title := strings.TrimSpace(fmt.Sprintf("factory %s %s", fid(it.ID), label))
	msg := fmt.Sprintf("Run `factory brief %s` and follow it exactly. Finish with the return block it specifies.", briefArgs)
	tmpl, err := r.agentCmdTemplate(name)
	if err != nil {
		r.log("%s %s: can't start agent: %v", tag(it), label, err)
		return map[string]string{}, true
	}
	cmdStr := strings.NewReplacer("{agent}", shellQuote(name), "{title}", shellQuote(title),
		"{message}", shellQuote(msg), "{dir}", shellQuote(dir)).Replace(tmpl)
	logPath := filepath.Join(r.root, ".factory", "logs", "jobs",
		fmt.Sprintf("%s-%s-%s.log", time.Now().Format("20060102-150405"), fid(it.ID), slugify(label)))
	f, err := os.Create(logPath)
	if err != nil {
		r.log("%s %s: can't create log: %v", tag(it), label, err)
		return map[string]string{}, true
	}
	defer f.Close()
	fmt.Fprintf(f, "$ %s\n(cwd %s)\n\n", cmdStr, dir)
	var buf bytes.Buffer
	cmd := exec.Command("bash", "-c", cmdStr)
	cmd.Dir = dir
	cmd.Stdout = io.MultiWriter(f, &buf)
	cmd.Stderr = f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = append(os.Environ(),
		"PATH="+r.exeDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GIT_EDITOR=true", "GIT_MERGE_AUTOEDIT=no",
		"FACTORY_JOB="+label, "FACTORY_ITEM="+strconv.Itoa(it.ID), "FACTORY_BACKEND="+r.backend)
	cmd.Env = append(cmd.Env, r.s.envFor(r.s.ctxFor(dir))...)
	r.log("%s %s → %s", tag(it), label, name)
	start := time.Now()
	r.mu.Lock()
	err = cmd.Start()
	if err == nil {
		r.procs[cmd] = true
	}
	r.mu.Unlock()
	if err != nil {
		r.log("%s %s: can't start agent: %v", tag(it), label, err)
		return map[string]string{}, true
	}
	werr := cmd.Wait()
	r.mu.Lock()
	delete(r.procs, cmd)
	r.mu.Unlock()
	if r.killed.Load() {
		return nil, false
	}
	secs := time.Since(start).Seconds()
	text := buf.String()
	claude := r.backend == "claude" && os.Getenv("FACTORY_AGENT_CMD") == ""
	parsed, u, calls := parseAgentOutput(claude, text)
	if parsed != text {
		text = parsed
		fmt.Fprintf(f, "\n--- result ---\n%s\n", text)
	}
	fmt.Fprint(f, formatTools(calls))
	out = parseReturn(text)
	out["_tail"] = lastLines(text, 20)
	model := ""
	if claude {
		model = claudeModel(r.s, name)
	}
	r.s.recordAgentRun(it.ID, label, name, model, secs, u, toolCounts(calls))
	if werr != nil {
		fmt.Fprintf(f, "\n[exit: %v]\n", werr)
	}
	fmt.Fprintf(f, "\n[%.0fs]\n", secs)
	return out, true
}

// parseAgentOutput extracts the agent's final text, usage and tool calls.
// Only claude output is JSON; other backends' text is returned unchanged so
// stray JSON-looking lines can't swallow the return block.
func parseAgentOutput(claude bool, out string) (text string, u agentUsage, calls []toolCall) {
	if claude {
		if parsed, u, calls, ok := parseClaudeJSON(out); ok {
			return parsed, u, calls
		}
	}
	return out, u, nil
}

// lastLines returns the last n lines of s.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func (r *runner) killAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for c := range r.procs {
		if c.Process != nil {
			syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		}
	}
}

// ---------------------------------------------------------------- jobs

func (r *runner) planJob(it Item) {
	out, alive := r.agent("factory-overseer", it, "plan", fmt.Sprintf("plan %d", it.ID), featureWorktree(r.s, it))
	if !alive {
		return
	}
	it = r.reload(it)
	if firstWord(out["DECISION"]) == "park" {
		r.s.park(it, "plan: "+orDash(out["REASON"]))
		r.log("%s parked by planner: %s", tag(it), orDash(out["REASON"]))
		return
	}
	var missing []string
	for _, t := range r.s.tasks(it) {
		if t.Status == "todo" && r.s.getDoc("task", it.ID, t.N) == "" {
			missing = append(missing, fmt.Sprintf("T%d", t.N))
		}
	}
	if len(missing) > 0 {
		r.s.park(it, "planner left tasks without packets: "+strings.Join(missing, ", "))
		r.log("%s parked: planner left %s without packets", tag(it), strings.Join(missing, ", "))
		return
	}
	r.log("%s planned (%d tasks)", tag(it), len(r.s.tasks(it)))
}

func (r *runner) buildJob(it Item, t Task, fix bool) {
	wt := featureWorktree(r.s, it)
	args := fmt.Sprintf("build %d T%d", it.ID, t.N)
	label := fmt.Sprintf("build T%d", t.N)
	if fix {
		r.s.exec("UPDATE tasks SET fixes=fixes+1 WHERE item_id=? AND n=?", it.ID, t.N)
		args += " --fix"
		label = fmt.Sprintf("fix T%d", t.N)
	}
	out, alive := r.agent("factory-builder", it, label, args, wt)
	if !alive {
		return
	}
	t = r.s.task(it, strconv.Itoa(t.N))
	switch firstWord(out["STATUS"]) {
	case "done":
	case "blocked", "failed":
		r.blockAndPark(it, t, fmt.Sprintf("builder %s: %s", firstWord(out["STATUS"]), orDash(out["NOTE"])))
		return
	default:
		if st, _ := gitRun(wt, "status", "--short"); st != "" {
			stat, _ := gitRun(wt, "diff", "--stat")
			r.log("%s %s: no STATUS, work left in worktree → continue", tag(it), label)
			r.failRound(it, t, "previous attempt ran out of budget before finishing (no STATUS block); "+
				"continue from the uncommitted changes, don't restart.\n\ngit status --short:\n"+st+
				"\n\ngit diff --stat:\n"+orDash(stat)+"\n\nlast output:\n"+out["_tail"], true)
			return
		}
		r.blockAndPark(it, t, "builder returned no STATUS block (see .factory/logs/jobs)")
		return
	}
	ok, summary := runGate(r.s, wt, it.ID, t.N, nil)
	if !ok {
		r.log("%s %s: gate FAIL after build", tag(it), label)
		r.failRound(it, t, "gate failed after the builder finished:\n"+summary, true)
		return
	}
	subject := fmt.Sprintf("%s: T%d %s", kindScope(it), t.N, t.Title)
	if fix {
		subject += fmt.Sprintf(" (fix %d)", t.Fixes)
	}
	gitRun(wt, "add", "-A")
	if o, ok := gitRun(wt, "commit", "-q", "--allow-empty", "--no-verify", "-m", subject); !ok {
		r.blockAndPark(it, t, "commit failed: "+firstLine(o))
		return
	}
	sha, _ := gitRun(wt, "rev-parse", "HEAD")
	stat, _ := gitRun(wt, "diff", "--shortstat", "HEAD~1", "HEAD")
	r.s.exec("UPDATE tasks SET status='built', sha=?, note=?, updated=? WHERE item_id=? AND n=?", sha, out["NOTE"], now(), it.ID, t.N)
	r.s.event(it.ID, "commit", strings.TrimSpace(sha[:8]+" "+stat), t.N)
	r.log("%s %s → done, gate PASS, committed %s %s", tag(it), label, sha[:8], stat)
}

func (r *runner) verifyJob(it Item, t Task) {
	out, alive := r.agent("factory-verifier", it, fmt.Sprintf("verify T%d", t.N),
		fmt.Sprintf("verify %d T%d --tests", it.ID, t.N), featureWorktree(r.s, it))
	if !alive {
		return
	}
	t = r.s.task(it, strconv.Itoa(t.N))
	if strings.ToUpper(firstWord(out["VERDICT"])) == "PASS" {
		r.s.exec("UPDATE tasks SET status='verified', updated=? WHERE item_id=? AND n=?", now(), it.ID, t.N)
		r.s.event(it.ID, "task", "verified", t.N)
		r.s.refreshItemStatus(it.ID)
		r.log("%s verify T%d → PASS", tag(it), t.N)
		return
	}
	reason := out["TOP"]
	if out["VERDICT"] == "" {
		reason = "verifier returned no VERDICT block"
	}
	r.log("%s verify T%d → FAIL: %s", tag(it), t.N, orDash(reason))
	r.failRound(it, t, orDash(reason), r.s.getDoc("verify", it.ID, t.N) == "")
}

// failRound records a failed attempt: back to the builder in fix mode, or blocked + parked after 2 fix rounds.
func (r *runner) failRound(it Item, t Task, findings string, writeDoc bool) {
	if writeDoc {
		r.s.putDoc("verify", "Verdict: FAIL\n\n## Findings\n- [blocking] "+findings, it.ID, t.N)
	}
	if t.Fixes >= 2 {
		r.blockAndPark(it, t, fmt.Sprintf("still failing after %d fix rounds: %s", t.Fixes, firstLine(findings)))
		return
	}
	r.s.exec("UPDATE tasks SET status='failed', note=?, updated=? WHERE item_id=? AND n=?", firstLine(findings), now(), it.ID, t.N)
	r.s.event(it.ID, "task", "failed: "+firstLine(findings), t.N)
}

func (r *runner) blockAndPark(it Item, t Task, reason string) {
	r.s.exec("UPDATE tasks SET status='blocked', note=?, updated=? WHERE item_id=? AND n=?", reason, now(), it.ID, t.N)
	r.s.event(it.ID, "task", "blocked: "+reason, t.N)
	r.s.park(r.reload(it), fmt.Sprintf("T%d %s", t.N, reason))
	r.log("%s parked: T%d %s", tag(it), t.N, reason)
}

func (r *runner) judgeJob(it Item) {
	out, alive := r.agent("factory-overseer", it, "judge", fmt.Sprintf("judge %d", it.ID), featureWorktree(r.s, it))
	if !alive {
		return
	}
	it = r.reload(it)
	if !allVerified(r.s.tasks(it)) {
		r.s.refreshItemStatus(it.ID)
		r.log("%s judge → tasks added; back to building", tag(it))
		return
	}
	reason := orDash(out["REASON"])
	switch firstWord(out["DECISION"]) {
	case "merge":
		r.s.setStatus(it, "merging", "")
		r.log("%s judge → merge", tag(it))
	case "park":
		r.s.park(it, "judge: "+reason)
		r.log("%s judge → park: %s", tag(it), reason)
	case "tasks-added":
		r.s.park(it, "judge said tasks-added but added none")
		r.log("%s parked: judge said tasks-added but added none", tag(it))
	default:
		r.s.park(it, "judge returned no DECISION block")
		r.log("%s parked: judge returned no DECISION block", tag(it))
	}
}

// syncFactory rebases the integration branch onto main if main moved. On an unresolved
// conflict it aborts and pauses merging. it may be the zero Item (start of run).
func (r *runner) syncFactory(it Item) bool {
	mb := mainBranch(r.s)
	fw := r.factoryWT
	if isAncestor(fw, mb, integrationBranch) {
		return true
	}
	if !isClean(fw) {
		gitRun(fw, "reset", "-q", "--hard")
	}
	r.log("factory: rebasing onto %s", mb)
	out, ok := gitRun(fw, "rebase", "-q", mb)
	if ok {
		r.log("factory: rebased onto %s", mb)
		return true
	}
	if !rebaseInProgress(fw) {
		r.pauseMerges("rebase onto " + mb + " failed: " + firstLine(out))
		return false
	}
	ref := "-"
	if it.ID != 0 {
		ref = strconv.Itoa(it.ID)
	}
	res, alive := r.agent("factory-overseer", it, "resolve rebase", "resolve "+ref+" --phase rebase", fw)
	if !alive {
		return false
	}
	if rebaseInProgress(fw) || len(conflictedFiles(fw)) > 0 {
		gitRun(fw, "rebase", "--abort")
		r.pauseMerges("rebase of factory onto " + mb + " has conflicts the overseer couldn't resolve: " + orDash(res["REASON"]))
		return false
	}
	r.log("factory: rebase conflicts resolved")
	return true
}

func (r *runner) pauseMerges(reason string) {
	r.mu.Lock()
	r.mergePaused = reason
	r.mu.Unlock()
	r.log("merging paused: %s", reason)
}

func (r *runner) mergeJob(it Item) {
	wt := featureWorktree(r.s, it)
	fw := r.factoryWT
	if it.MergedSha == "" {
		if !r.syncFactory(it) || r.killed.Load() {
			return
		}
		if !isAncestor(wt, integrationBranch, "HEAD") {
			out, ok := gitRun(wt, "merge", "--no-edit", "-q", integrationBranch)
			if !ok {
				if len(conflictedFiles(wt)) == 0 {
					gitRun(wt, "merge", "--abort")
					r.s.park(it, "merging factory failed: "+firstLine(out))
					r.log("%s parked: merging factory failed", tag(it))
					return
				}
				r.log("%s merge: conflicts with factory in %s", tag(it), strings.Join(conflictedFiles(wt), ", "))
				res, alive := r.agent("factory-overseer", it, "resolve merge", fmt.Sprintf("resolve %d --phase merge", it.ID), wt)
				if !alive {
					return
				}
				if mergeInProgress(wt) || len(conflictedFiles(wt)) > 0 {
					gitRun(wt, "merge", "--abort")
					r.s.park(it, "merge conflict with factory unresolved: "+orDash(res["REASON"]))
					r.log("%s parked: merge conflict unresolved", tag(it))
					return
				}
				r.log("%s merge: conflicts resolved", tag(it))
			}
		}
		if r.killed.Load() {
			return
		}
		if ok, summary := runGate(r.s, wt, it.ID, 0, nil); !ok {
			r.s.park(it, "gate failed after merging factory")
			r.log("%s parked: gate failed after merging factory\n%s", tag(it), summary)
			return
		}
		if !isClean(fw) {
			gitRun(fw, "reset", "-q", "--hard")
		}
		if out, ok := gitRun(fw, "merge", "--squash", branchName(it)); !ok {
			gitRun(fw, "reset", "-q", "--hard")
			r.s.park(it, "squash merge failed: "+firstLine(out))
			r.log("%s parked: squash merge failed", tag(it))
			return
		}
		subject, body := r.mergeMessage(it)
		if _, clean := gitRun(fw, "diff", "--cached", "--quiet"); !clean {
			if out, ok := gitRun(fw, "commit", "-q", "--no-verify", "-m", subject, "-m", body); !ok {
				gitRun(fw, "reset", "-q", "--hard")
				r.s.park(it, "squash commit failed: "+firstLine(out))
				return
			}
		}
		sha, _ := gitRun(fw, "rev-parse", "HEAD")
		r.s.exec("UPDATE items SET merged_sha=? WHERE id=?", sha, it.ID)
		r.log("%s squashed into factory: %s %s", tag(it), sha[:8], subject)
	}
	removeWorktree(r.root, wt)
	gitRun(r.root, "branch", "-D", branchName(it))
	r.s.exec("UPDATE items SET slot=0 WHERE id=?", it.ID)
	r.s.setStatus(r.reload(it), "merged", "")
	r.mu.Lock()
	_, cost, mins := itemUsage(r.s, it.ID, now())
	r.merged = append(r.merged, fmt.Sprintf("%s %s: %s | %.0fm $%.2f", fid(it.ID), kindScope(it), it.Title, mins, cost))
	r.mu.Unlock()
}

// itemUsage sums an item's agent runs: count, cost, and wall minutes from the first run's start to end.
func itemUsage(s *Store, itemID int, end float64) (int, float64, float64) {
	var runs int
	var cost, start float64
	if s.db.QueryRow("SELECT count(*), COALESCE(sum(cost_usd),0), COALESCE(min(ts-secs),0) FROM agent_runs WHERE item_id=?", itemID).
		Scan(&runs, &cost, &start) != nil || runs == 0 {
		return 0, 0, 0
	}
	return runs, cost, max(0, end-start) / 60
}

func (r *runner) mergeMessage(it Item) (string, string) {
	subject := kindScope(it) + ": " + it.Title
	body := strings.TrimSpace(r.s.getDoc("merge-msg", it.ID, 0))
	if body == "" {
		var lines []string
		for _, t := range r.s.tasks(it) {
			lines = append(lines, fmt.Sprintf("- T%d %s", t.N, t.Title))
		}
		body = strings.Join(lines, "\n")
	}
	return subject, body + fmt.Sprintf("\n\nFactory-Item: %s", fid(it.ID))
}

// ---------------------------------------------------------------- recovery & summary

// recover repairs state left by an interrupted run: aborted rebases/merges, stray squashes, missing worktrees.
func (r *runner) recover() {
	for _, it := range r.s.itemsWithStatus(inFlight) {
		if it.Slot == 0 { // from an older factory version: move into a slot
			legacy := featureWorktree(r.s, it)
			slot := r.freeSlot()
			r.s.exec("UPDATE items SET slot=? WHERE id=?", slot, it.ID)
			it = r.reload(it)
			if dirExists(legacy) {
				gitRun(r.root, "worktree", "move", legacy, slotPath(r.s, slot))
			}
			r.log("recover: %s moved into slot %d", tag(it), slot)
		}
		wt := featureWorktree(r.s, it)
		if it.Status == "merging" && it.MergedSha != "" {
			continue
		}
		if !dirExists(wt) {
			if branchExists(r.root, branchName(it)) {
				gitRun(r.root, "worktree", "prune")
				gitRun(r.root, "worktree", "add", "-q", wt, branchName(it))
				r.log("recover: %s recreated worktree", tag(it))
			} else {
				r.s.exec("UPDATE tasks SET status='todo', sha='', fixes=0 WHERE item_id=?", it.ID)
				r.s.exec("UPDATE items SET slot=0 WHERE id=?", it.ID)
				r.s.setStatus(it, "ready", "worktree and branch missing; restarting")
				r.log("recover: %s worktree and branch missing; back to ready", tag(it))
			}
			continue
		}
		if mergeInProgress(wt) {
			gitRun(wt, "merge", "--abort")
			r.log("recover: %s aborted an interrupted merge", tag(it))
		}
	}
}

func (r *runner) summary() {
	r.log("== summary")
	if len(r.merged) > 0 {
		r.log("merged (%d):", len(r.merged))
		for _, m := range r.merged {
			r.log("  %s", m)
		}
	} else {
		r.log("merged: none this run")
	}
	if parked := r.s.itemsWithStatus([]string{"parked"}); len(parked) > 0 {
		r.log("parked (%d):", len(parked))
		for _, it := range parked {
			r.log("  %s %s: %s, %s", fid(it.ID), kindScope(it), it.Title, orDash(it.ParkReason))
		}
	}
	counts := map[string]int{}
	for _, it := range r.s.itemsWithStatus([]string{"backlog", "ready", "building", "judging", "merging"}) {
		counts[it.Status]++
	}
	r.log("remaining: ready=%d building=%d judging=%d merging=%d backlog=%d",
		counts["ready"], counts["building"], counts["judging"], counts["merging"], counts["backlog"])
	if len(r.held) > 0 {
		r.log("held (%d), stale packets; refresh with the factory-feature skill or rerun without --stale=hold:", len(r.held))
		for id, reason := range r.held {
			r.log("  %s %s", fid(id), reason)
		}
	}
	if r.mergePaused != "" {
		r.log("merging is paused: %s", r.mergePaused)
	}
	if r.stopping.Load() {
		r.log("stopped early; run `factory run` again to resume")
	} else if len(r.merged) > 0 {
		r.log("next: review the `factory` branch, then `factory land`")
	}
}
