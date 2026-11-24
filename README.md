# factory

An autonomous feature pipeline for one developer.

- **Claude Code** writes the work: features, bug reports, and per-task packets.
- **opencode** running a local model builds, verifies and merges it, through three short-lived agents (overseer, builder, verifier) that a Go scheduler launches.
- **State** lives per project in a SQLite database at `.factory/factory.db`, excluded from git. Only code gets committed.
- **Context budget:** everything is sized for 3 concurrent agents with about 130k tokens each. Agents start from a compact, assembled brief instead of exploring the repo.

## Install (once)

```sh
cd ~/personal/factory && ./install.sh
```

`install.sh` builds the binary into `~/.local/bin/factory` (`go install`), then runs `factory internal install --link "$PWD"`. That symlinks:

| What | Where |
|---|---|
| agents: `factory-overseer`, `factory-builder`, `factory-verifier` | `$OPENCODE_CONFIG_DIR/agents/` (default `~/.config/opencode/agents/`) |
| command: `/factory-init` | `$OPENCODE_CONFIG_DIR/commands/` |
| skill: `factory-feature` | `$CLAUDE_CONFIG_DIR/skills/` (default `~/.claude/skills/`) and `$OPENCODE_CONFIG_DIR/skills/` |

Because they're symlinks, edits in this repo take effect immediately. Without `--link`, the copies embedded in the binary are written instead. Install never overwrites files it didn't create (unless you pass `--force`). Remove everything it installed with `factory internal install --uninstall`.

## A new project, end to end (todo-app example)

1. **Hand-write a skeleton with a green gate.** For example: `npm init`, a `src/todos.ts` with one function, one passing test, and lint/typecheck configs, committed on `main`. The factory builds *onto* a working project. Every task is accepted only if the gate passes, so the gate has to pass before the first task.
2. **`factory init`** creates `.factory/`, adds it to `.git/info/exclude`, and registers the project.
3. **Project setup, from either tool.** In Claude Code, say "init factory" to the `factory-feature` skill (its init mode). In opencode, run `/factory-init` (overseer agent). Both follow `factory internal template init`: find the `setup` and gate commands (`format`, `lint`, `typecheck`, `test`, `test_focused`, `build`), record them with `factory gate config`, validate them with `factory gate`, and write the code map that agents read instead of exploring.
4. **Write work with `/factory-feature` in Claude Code.** For example: "add todo priorities", or "bug: completing a todo in a filtered view crashes". The skill interviews you briefly, then writes the spec with testable `**ACn**` criteria. It splits the work into tasks of about 150 changed lines or fewer, researches the code, writes a full packet for every task, checks the result with `factory cat plan <id>`, and queues it with `factory ready <id>`.
5. **`factory board`** shows the queue, the health header, and each item's next step.
6. **`factory run`** builds every ready feature until the queue is empty or everything left is parked.
7. **Preview** the integration branch: `git switch --detach factory` (or `git switch factory` while no run is active), then switch back to `main`.
8. **`factory land`** fast-forwards `main` to `factory`.

## Command reference

### Human commands (`factory --help`)

| Command | What it does |
|---|---|
| `init` | Set up `.factory/` in this repo. |
| `board [-a] [--flat] [--done] [--kind k]` | Health header: integration commit and how far `factory` is ahead of main, install status, gate checks and last result, worktree count, stale-packet count, registered projects. Below it, features grouped by status, each with its next step. `-a` covers every registered project; `--flat` gives one list. |
| `show <item> [--md] [-n N]` | One feature: status, park reason, worktree, tasks, docs, recent events. `--md` prints a markdown summary for a PR. |
| `run [--slots 3] [--features 2] [--stale refresh\|hold] [--main b] [--dry-run]` | The scheduler (see "How a run works"). |
| `review` | Per unlanded `factory` commit: subject, item, ACs, tasks and fix rounds, agent runs and cost, shortstat; then a total. |
| `land` | Fast-forward main to `factory`. If main diverged, it first rebases `factory` onto main in a temporary worktree and runs the gate there. Refuses while a run is active or when the main checkout is dirty. |
| `set <item> [-p 1-4] [--title t] [--kind k] [--scope s] [--status s]` | Edit a feature. On a parked item, `--status ready` resumes it: blocked and failed tasks reset, and the existing worktree and branch are kept. |
| `stats [-a]` | Lead time, fix rounds, gate pass rates (`factory internal stats` still works). |
| `clean [--parked]` | Remove worktrees and branches of merged, dropped and orphaned features, run `git worktree prune`, and auto-remap shifted packet refs. Parked worktrees: asks y/N per item on a TTY, keeps them otherwise, `--parked` removes them (branch kept). It lists changed packets as "needs a model → /factory-feature refresh" and never edits those. |

### Project setup: skill init mode (Claude Code) or /factory-init (opencode)
One-time interactive setup: discovers the setup and gate commands and writes the code map (and optional lessons). Both routes print and follow `factory internal template init`, so the steps are the same either way.

### The `factory-feature` skill (Claude Code or opencode)
- **feature (full plan, default):** spec, tasks, and a complete packet per task. Tasks that already have packets skip the overseer's plan job.
- **bug:** investigates the likely cause first, fills the bug template (Repro, Expected, Actual, Suspected area, ACs), sets kind `fix` and p1. `**AC1**` must be the regression test.
- **refresh:** `factory stale`, then `factory brief refresh <id> T<n>` per flagged task, then the packet is rewritten with the same scope.
- **quick (only on request):** spec plus one-line tasks; the overseer writes the packets during the run.
- **init:** one-time project setup ("init factory"): follows `factory internal template init`, the same steps as `/factory-init`.
- **retro:** `factory internal retro`, then only the changed map sections are rewritten (`factory write map`) and 1-3 lessons appended for recurring friction (`factory write lessons --append`).

### Other command groups
Skill commands (`add`, `write`, `task-add`, `cat`, `ready`, `stale`, `brief refresh`, `edit`), agent commands (`brief`, `write`, `test`, `gate`) and internal commands (`task-set`, `log`, `template`, `gate config`, `install`, `retro`) are documented by `factory help skill|agent|internal`. Internal commands run as `factory internal <cmd>`; the exception is `gate config`, which is spelled `factory gate config`.

`factory internal retro [--since <ts|item>]` prints a short digest of what changed since the code map was last saved: merged items, `git diff --stat` on `factory`, park reasons and blocking verify findings, fix rounds, and the map's size. The skill's retro mode uses it to keep the map and lessons current. When items have merged since the map was saved, `factory board` shows `map stale (N merged since)` in its header.

## How a run works

```
factory run
  ├─ lock .factory/run.lock, clean merged/dropped/orphan worktrees, recover interrupted state
  ├─ create the transient integration worktree for `factory`; rebase it onto main if main moved
  └─ loop until nothing is running and nothing is launchable:
       activate ready features (priority, then id) while in-flight < --features
           → worktree from the factory tip + `setup`
       for each in-flight feature without a running job, pick its next action:
           building: plan (missing packets) | fix T(n) | verify T(n) | stale refresh | build T(n)
           judging:  judge          merging: merge (one at a time)
       launch by priority until --slots agents run:  overseer/merge  >  verifier  >  builder
       wait for a job to finish (or Ctrl-C)
  └─ summary: merged, parked (with reasons), held, remaining; remove the integration worktree
```

- **One job per feature at a time.** A feature's verifier runs on the committed worktree while that feature's builder is idle, so tests are allowed. Parallelism comes from running several features at once.
- **Agents** are headless processes in the feature's worktree, told "Run \`factory brief …\` and follow it exactly": `opencode run --agent <name> …` by default, or `claude -p …` with the [Claude backend](#claude-backend). The scheduler parses the final `STATUS:`, `VERDICT:` or `DECISION:` block and treats the database as the source of truth. Each job's output goes to `.factory/logs/jobs/`.
- **Build:** builder → the scheduler re-runs the gate and records it → commit in the worktree as `<kind>(<scope>): T<n> <title>` → verifier.
- **Fix rounds → park:** a gate failure or `VERDICT: FAIL` sends the task back to the builder in fix mode, with the findings in its brief. After 2 fix rounds the task is blocked and the feature parked. A builder `blocked`/`failed` result parks immediately; a missing STATUS block with changes left gets a continue round (see Troubleshooting), without changes it parks.
- **Judge:** when every task is verified, the overseer gets the ACs, task verdicts and diffstat (not the full diff). It decides `merge` (and writes the squash-commit body with `factory write merge-msg`), `tasks-added` (back to building), or `park`.
- **Merge sequence** (serialized):
  1. In the integration worktree, rebase `factory` onto main if main moved. On conflict, an overseer resolve job runs; if that fails, the rebase is aborted and merging pauses for the rest of the run.
  2. In the feature worktree, `git merge factory`. On conflict, an overseer resolve job runs (resolve, commit, gate); if it can't resolve, the merge is aborted and the feature parked.
  3. The gate runs in the feature worktree.
  4. `git merge --squash` into `factory`, committed as `<kind>(<scope>): <title>` with the judge's body and a `Factory-Item:` trailer.
  5. The feature worktree and branch are removed and the status becomes `merged`.
- **Worktrees** live in `$XDG_DATA_HOME/factory/worktrees/<project>-<hash>/`: `slot-N/` per in-flight feature (branch `factory-wip/NNN-slug`), `parked-NNN-slug/` while parked, `_factory/` during a run, and `_land/` during a diverged land. Merged features are cleaned up immediately. Parked features keep their worktree until `factory clean`.
- **Stale packets:** packet "Read first" refs (`path:A-B`, `path (symbol)`, `path`) are recorded against the factory tip when written. Right before a builder launches, refs are re-checked:
  - shifted ranges are auto-remapped in the packet text;
  - changed, missing or symbol-gone refs trigger an overseer refresh job with `--stale=refresh` (the default);
  - with `--stale=hold`, the feature is skipped (it stays `building`) and listed as held in the summary.
- **Ctrl-C** stops launching new jobs and waits for running ones; a second Ctrl-C kills them. The next `factory run` resumes from the database and worktree state.

## Git model

- **You stay on `main`.** `factory run` never touches your checkout. Finished features become squash commits on the long-lived `factory` branch, created from main on the first run.
- **The integration worktree is transient.** It exists only while a run is active (created at start, removed at exit, and any leftover from a crash is removed at the next start). Between runs you can `git switch factory` normally. `run` refuses to start while `factory` is checked out anywhere.
- **`factory land`** is the only thing that moves main, and it is never automatic. If you committed on main in the meantime, it rebases `factory` onto main in a temporary worktree and runs the gate before fast-forwarding. On conflict it aborts and tells you to `factory run` (the overseer resolves conflicts in its pre-merge rebase) or to resolve manually.

## Priorities and bugs

- Priority runs from `-p 1` (urgent) to `-p 4` (someday). Ready features are picked by priority, then id. Change it with `factory set <id> -p 1`.
- Bugs are items with kind `fix`. Their spec uses the bug template (`factory internal template bug`): Repro (numbered steps), Expected, Actual, Suspected area, Acceptance criteria, Out of scope. `factory ready` refuses a `fix` without a regression-test AC (`**AC1**: a regression test reproduces the bug and passes after the fix`).
- Filter bugs on the board with `factory board --kind fix`.

## Troubleshooting

- **Parked feature:** `factory show <id>` gives the reason and the worktree path. Inspect the worktree; the job logs are in `.factory/logs/jobs/`. Fix the cause (often a packet: `/factory-feature refresh`, or edit with `factory edit task <id> T<n>`), then `factory set <id> --status ready`.
- **Agent ran out of budget:** a builder that stops without a STATUS block but left changes gets one continue round (counts as a fix round) with its diff stat and the tail of its output. With no changes, or after 2 fix rounds, the feature parks; the worktree is kept, so resume with `factory set <id> --status ready`. On the Claude backend, `factory internal config claude_max_usd <usd>` caps each job's spend (`claude --max-budget-usd`; unset by default).
- **Gate failing at init:** fix the project until the gate passes, or, for a pre-existing failure you accept, `factory gate config <name> '<cmd>' --known-failing`. It's reported as KNOWN-FAIL and doesn't block.
- **Stuck run:** Ctrl-C (it waits for running jobs), Ctrl-C again to kill them, then `factory run` to resume. If a run died hard, the next run removes the stale lock and integration worktree on its own.
- **Logs:** `.factory/logs/run-<ts>.log` (run progress), `.factory/logs/jobs/` (one file per agent job), `.factory/logs/<ts>-<pid>/` (full gate and test output; agents only see excerpts).

## Configuration

| Variable | Effect |
|---|---|
| `FACTORY_AGENT_CMD` | Agent launcher template, run with `bash -c` in the job's directory. Placeholders `{agent}`, `{title}`, `{message}`, `{dir}` (shell-quoted). Default: `opencode run --agent {agent} --auto --archived --title {title} {message}`. The e2e tests point it at a stub script. |
| `XDG_DATA_HOME` | Holds the project registry, the installed-files list and the worktrees (`$XDG_DATA_HOME/factory/`, default `~/.local/share/factory/`). |
| `XDG_CONFIG_HOME` / `OPENCODE_CONFIG_DIR` | Where opencode agents, commands and skills are installed (default `~/.config/opencode`). |
| `CLAUDE_CONFIG_DIR` | Where the Claude Code skill is installed (default `~/.claude`). |
| `FACTORY_TAIL` | Lines of failure excerpt shown from gate/test logs (default 30). |
| `VISUAL` / `EDITOR` | Used by `factory edit`. |

Per-project settings (`factory internal config <key> <value>`) include:

- `post_land`: shell command run in the main checkout after `factory land`, with `FACTORY_LANDED_RANGE=<old>..<new>`; failure only warns.
- `remote`: remote that `factory land --push` pushes main to (default `origin`), with `--follow-tags`.
- `tagging`: `semver` tags each squash commit with an annotated `vX.Y.Z` (feat bumps minor, fix/perf bump patch, other kinds aren't tagged), re-points the tags after factory is rebased, and `factory land --push` pushes them. Default `off`.

The main branch is detected as `main`, then `master`, then the current branch, and stored. Override it with `factory run --main <branch>`.

### Claude backend

Agents run with opencode by default. To run them with Claude Code instead:

- `factory internal config backend claude` selects the Claude backend for the project (`opencode` switches back).
- `factory internal config claude_model <model>` sets the model (default `opus`).
- `factory run --backend opencode|claude` overrides the configured backend for one run.

With the Claude backend, each job runs `claude -p … --permission-mode dontAsk`, with the agent's prompt appended to the system prompt and a tool allowlist derived from that agent's opencode `permission` block (the derived prompt and settings files are written to `.factory/agents/`). `FACTORY_AGENT_CMD`, when set, overrides both backends. `factory board` shows the effective backend in its header (`agents opencode`, `agents claude/<model>` or `agents custom (FACTORY_AGENT_CMD)`).

## Working on the factory itself

```
main.go         command table, help groups, arg parser, embeds
store.go        schema, migrations, items/tasks/docs/events
cmd_human.go    board, show, set, clean
cmd_pm.go       init, add, ready, show details
cmd_task.go     task-add, task-set, nextRunnable
cmd_doc.go      cat, write, edit, template, briefs (build verify plan judge resolve)
cmd_gate.go     gate, gate config, test, setup, stats, export
cmd_retro.go    retro digest (what changed since the map was saved)
git.go          worktrees, integration branch, run lock, land, gc
run.go          the scheduler: jobs, agents, merge sequence, recovery, summary
stale.go        packet refs, range remapping, stale/refresh
install.go      install/uninstall of agents, commands, skills
templates/      spec bug task report verify map lessons (embedded)
agents/         opencode agent prompts (embedded; symlinked by install.sh)
commands/       /factory-init (embedded)
skills/         factory-feature skill (embedded)
```

Run the tests with `go vet ./... && go test ./...`. The unit tests cover the arg parser, return-block parsing, `nextRunnable`, ready validation and status transitions, the v1 → v2 migration, range remapping, and ref parsing. The end-to-end tests are shell scripts that drive real git repos with a stub agent (`FACTORY_AGENT_CMD`): `bash test/e2e/run.sh` and `bash test/e2e/stale.sh` (each builds the binary into a temp dir; set `FACTORY_E2E_DIR` to keep the artifacts).

## Not yet

- No parallel tasks within one feature (one job per feature at a time, by design for now).
- The second-Ctrl-C kill path isn't covered by the e2e tests.
- opencode also reads `~/.claude/skills`, so it may list `factory-feature` twice.
- Plain-path packet refs (no range, no symbol) are only checked for `missing`; content changes don't flag them.
