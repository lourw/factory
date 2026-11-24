// factory: local work tracker and context assembler for the personal software factory.
//
// State (items, tasks, docs, events, gate config and runs) lives in <repo>/.factory/factory.db,
// which is excluded from git via .git/info/exclude. Only code gets committed.
// Agents read assembled context (`factory brief ...`, `factory cat ...`) and write bodies
// (`factory write ...`); headers and metadata are injected from the DB.
package main

import (
	"embed"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

//go:embed templates/*.md
var templatesFS embed.FS

//go:embed agents commands skills
var opencodeFS embed.FS

func tpl(name string) string {
	b, err := templatesFS.ReadFile("templates/" + name + ".md")
	if err != nil {
		return ""
	}
	return string(b)
}

// ---------------------------------------------------------------- args

type Args struct {
	Pos   []string
	Flags map[string]string
	Bools map[string]bool
}

var shortAlias = map[string]string{"p": "prio", "a": "all", "s": "status", "n": "events"}

// parseArgs allows flags anywhere: --x v, --x=v, -p v, -p1. Bool flags never consume a value.
func parseArgs(in []string, boolFlags ...string) Args {
	bools := set(boolFlags...)
	a := Args{Flags: map[string]string{}, Bools: map[string]bool{}}
	for i := 0; i < len(in); i++ {
		s := in[i]
		if s == "--" {
			a.Pos = append(a.Pos, in[i+1:]...)
			break
		}
		var name, val string
		hasVal := false
		switch {
		case strings.HasPrefix(s, "--") && len(s) > 2:
			name = s[2:]
			if k, v, ok := strings.Cut(name, "="); ok {
				name, val, hasVal = k, v, true
			}
		case strings.HasPrefix(s, "-") && len(s) > 1 && !isNumber(s):
			name = s[1:2]
			if len(s) > 2 {
				val, hasVal = strings.TrimPrefix(s[2:], "="), true
			}
			if full, ok := shortAlias[name]; ok {
				name = full
			}
		default:
			a.Pos = append(a.Pos, s)
			continue
		}
		if bools[name] {
			a.Bools[name] = !hasVal || (val != "false" && val != "0")
			continue
		}
		if !hasVal {
			if i+1 >= len(in) {
				die("flag --%s needs a value", name)
			}
			i++
			val = in[i]
		}
		a.Flags[name] = val
	}
	return a
}

func isNumber(s string) bool { _, err := strconv.ParseFloat(s, 64); return err == nil }

func (a Args) pos(i int) string {
	if i < len(a.Pos) {
		return a.Pos[i]
	}
	return ""
}

func (a Args) need(i int, what string) string {
	if i >= len(a.Pos) {
		die("missing <%s>", what)
	}
	return a.Pos[i]
}

func (a Args) rest(i int) string {
	if i >= len(a.Pos) {
		return ""
	}
	return strings.Join(a.Pos[i:], " ")
}

func (a Args) flag(name string) (string, bool) { v, ok := a.Flags[name]; return v, ok }

func (a Args) intFlag(name string, def int) int {
	v, ok := a.Flags[name]
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		die("--%s must be a number", name)
	}
	return n
}

// ---------------------------------------------------------------- dispatch

type command struct {
	fn    func(Args)
	bools []string
	group string // human | skill | agent | internal
	usage string
}

var commands = map[string]command{
	// human
	"init":   {cmdInit, nil, "human", "init: set up .factory/ in this repo (then the factory-feature skill's init mode, or /factory-init in opencode)"},
	"board":  {cmdBoard, []string{"all", "done", "flat"}, "human", "board [-a] [--flat] [--done] [--kind k]: health summary + features by status with their next step"},
	"show":   {cmdShow, []string{"md"}, "human", "show <item> [--md] [-n events]: one feature: tasks, worktree, events (--md: summary for a PR)"},
	"run":    {cmdRun, []string{"dry-run"}, "human", "run [--slots 3] [--features 2] [--stale refresh|hold] [--backend opencode|claude] [--main b] [--dry-run]: build ready features until the queue is empty"},
	"land":   {cmdLand, []string{"push"}, "human", "land [--push]: fast-forward main to factory (rebases + gates first if main diverged), run post_land, then push main to `remote` if --push"},
	"review": {cmdReview, nil, "human", "review: per unlanded factory commit: subject, item, ACs, tasks/fix rounds, agent runs + cost, shortstat; then a total"},
	"set":    {cmdSet, nil, "human", "set <item> [-p 1-4] [--title t] [--kind k] [--scope s] [--status s]: edit a feature (parked → --status ready resumes it)"},
	"clean":  {cmdClean, []string{"parked"}, "human", "clean [--parked]: remove merged/dropped/orphan worktrees and branches, remap shifted packets, report stale ones"},
	// skill
	"add":      {cmdAdd, nil, "skill", "add <title...> [--kind feat|fix|chore|refactor|perf|test|docs|build|ci] [--scope s] [-p 1-4] [--slug s]"},
	"write":    {cmdWrite, []string{"append"}, "skill", "write <kind> [item] [task] [--append] <<'EOF': save a doc (spec task report verify merge-msg notes map lessons)"},
	"task-add": {cmdTaskAdd, []string{"body", "force"}, "skill", "task-add <item> <title...> [--covers AC1,AC2] [--deps T1] [--body (packet on stdin)]"},
	"cat":      {cmdCat, []string{"raw"}, "skill", "cat <kind> [item] [task] [--raw]: print a doc with its header (plan = tasks + coverage + packets)"},
	"import":   {cmdImport, []string{"ready"}, "skill", "import [file|-] [--ready]: create an item with spec, tasks and packets from one file"},
	"ready":    {cmdReady, []string{"force"}, "skill", "ready <item>: validate (ACs, coverage, regression AC for fixes) and queue for `factory run`"},
	"stale":    {cmdStale, []string{"all"}, "skill", "stale [item] [-a]: packet refs changed by merged work (shifted ones are auto-fixed)"},
	"edit":     {cmdEdit, nil, "skill", "edit <kind> [item] [task]: edit a doc in $EDITOR (human escape hatch)"},
	// agent (brief also serves the skill: `brief refresh`)
	"brief": {cmdBrief, []string{"fix", "tests"}, "agent", "brief build|verify <item> <task> [--fix|--tests] | plan|judge <item> | refresh <item> <task> | resolve <item|-> --phase rebase|merge"},
	"gate":  {cmdGate, nil, "agent", "gate [name...] [--item i] [--task t]: run the gate in the current worktree (gate config: see internal)"},
	"test":  {cmdTest, nil, "agent", "test <args...>: run test_focused in the current worktree"},
	// internal
	"task-next": {cmdTaskNext, []string{"pipeline"}, "internal", "task-next <item> [--pipeline] [--exclude T3]"},
	"task-set":  {cmdTaskSet, nil, "internal", "task-set <item> <task> [--status s] [--sha x] [--note n] [--title t] [--covers c] [--deps d]"},
	"log":       {cmdLog, nil, "internal", "log <item> <msg...> [--task Tn] [--kind k]"},
	"template":  {cmdTemplate, nil, "internal", "template <name>: spec bug task report verify map lessons init"},
	"install":   {cmdInstall, []string{"uninstall", "force"}, "internal", "install [--link <repo>] [--uninstall] [--force]: opencode agents/commands + skills"},
	"stats":     {cmdStats, []string{"all"}, "internal", "stats [-a]: lead time, fix rounds, gate pass rates"},
	"retro":     {cmdRetro, nil, "internal", "retro [--since ts|item]: what changed since the map was written (for the skill's retro mode)"},
	"config":    {cmdConfig, nil, "internal", "config [key] [value]: per-project settings (worktree_root cache_dir tagging post_land remote main_branch backend claude_model claude_model_overseer claude_model_builder claude_model_verifier claude_effort claude_effort_overseer claude_effort_builder claude_effort_verifier claude_max_usd; '-' unsets)"},
}

// shown alongside a group in help, without being a separate command
var helpExtras = map[string][]string{
	"skill":    {"brief refresh <item> <task>: packet + diff hunks for its stale refs (see `factory help agent`)"},
	"internal": {"gate config [name] [cmd...] [--known-failing] | gate config --env NAME '<value>': gate checks and env for setup/gate/test/agents; {slot} {cache} {worktree} expand ('n/a' removes)"},
}

func usage(group string) {
	switch group {
	case "human":
		fmt.Print("usage: factory <command> [args]   (flags may appear anywhere)\n\n")
	case "internal":
		fmt.Print("usage: factory internal <command> [args]\n\n")
	default:
		fmt.Printf("%s commands:\n\n", group)
	}
	names := make([]string, 0, len(commands))
	for n, c := range commands {
		if c.group == group || (group == "skill" && n == "brief") || (group == "agent" && n == "write") {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Printf("  %-10s %s\n", n, commands[n].usage)
	}
	for _, e := range helpExtras[group] {
		fmt.Printf("  %s\n", e)
	}
	if group == "human" {
		fmt.Print("\nmore: factory help skill | agent | internal\n")
	}
}

func main() {
	args := os.Args[1:]
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		usage("human")
		return
	}
	if args[0] == "help" {
		g := "human"
		if len(args) > 1 {
			g = args[1]
		}
		if !contains([]string{"human", "skill", "agent", "internal"}, g) {
			die("help topics: skill, agent, internal")
		}
		usage(g)
		return
	}
	internal := args[0] == "internal"
	if internal {
		args = args[1:]
		if len(args) == 0 {
			usage("internal")
			return
		}
	}
	c, ok := commands[args[0]]
	if !ok || (c.group == "internal") != internal {
		fmt.Fprintf(os.Stderr, "factory: unknown command %q (see factory --help)\n", args[0])
		os.Exit(1)
	}
	rest := args[1:]
	if len(rest) > 0 && (rest[0] == "-h" || rest[0] == "--help") {
		fmt.Println("factory " + c.usage)
		return
	}
	c.fn(parseArgs(rest, c.bools...))
}
