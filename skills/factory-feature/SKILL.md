---
name: factory-feature
description: Turn an idea or a bug report into factory-ready work (spec with testable acceptance criteria, sized tasks with packets) using the `factory` CLI, or refresh stale task packets. Use when the user wants to add, plan, report, or queue work for `factory run`, check the factory queue, or fix stale packets. Also sets up a repository for the factory (gate commands and code map) when the user asks to init or set up factory.
---

# factory-feature

`factory` is a local work queue plus an autonomous implementer. You write features; `factory run` builds them with agents (builder, verifier, overseer), one task per agent session, and squash-merges finished features into a `factory` branch. The user lands that branch on main later with `factory land`.

The builder **reads only its task packet**. Packet quality decides whether a task succeeds.

## Modes
- **Feature, full plan** (default when you can read the codebase): spec, tasks, and a complete packet for every task. The scheduler then skips the overseer's plan job.
- **Bug**: same flow with the bug template; investigate the cause first (see "Bugs").
- **Quick** (only when the user asks for it): spec plus one-line tasks with hints. The overseer writes the packets during `factory run`.
- **Refresh**: fix packets that went stale because other features merged (see the end of this file).
- **Init**: one-time setup of a repo for the factory (see "Init mode" at the end).

Decide feature vs bug from the user's wording ("broken", "crashes", "wrong", "regression" → bug). Ask if it's unclear.

## 1. Interview (brief)
Ask only what changes what gets built: scope edges, behavior on errors, what's out of scope. Two rounds at most. Record defaults you picked as assumptions.

## 2. Create the feature
```sh
factory add "<imperative title>" --kind feat --scope <area> -p 2
#   kind: feat|fix|chore|refactor|perf|test|docs|build|ci   (it becomes the conventional-commit type)
#   scope: short area name, e.g. api, auth, cli               (it becomes the conventional-commit scope)
#   -p: 1 (urgent) .. 4 (someday)
```
It prints the item id (e.g. `004`); use the number below.

## 3. Write the spec
```sh
factory write spec 4 <<'EOF'
## Problem
<who is affected, what's wrong today: 2-4 sentences>

## Acceptance criteria
- **AC1**: <observable, testable behavior: "GET /x with an expired token returns 401">
- **AC2**: ...

## Out of scope
- ...

## Constraints
- ...

## Assumptions
- ...
EOF
```
Every AC must be checkable by an automated test. Use `**ACn**:` exactly; the CLI parses it.

## 4. Split into tasks
Each task is one builder session: ≤150 changed lines on the opencode backend, ≤400 on claude (see `factory internal config` → effective budget), one coherent change, leaving the repo green.
```sh
factory task-add 4 "Add token-bucket limiter in src/api/middleware/ratelimit.ts" --covers AC1
factory task-add 4 "Return Retry-After on 429 responses" --covers AC2 --deps T1
```
- Every AC must be covered by at least one task. Order risky work first. Use `--deps` for real dependencies only.
- **Quick mode:** make each title a strong hint (area, files, the pattern to follow), then skip to step 6.

## 5. Full plan: write every packet
Research the codebase narrowly (search for symbols, read the relevant windows), then write one packet per task:
```sh
factory internal template task     # the section skeleton
factory write task 4 T1 <<'EOF'
## Goal
...
## Read first (only these; use the line ranges)
- `src/api/middleware/auth.ts:10-64`: middleware shape to copy
- `src/api/middleware/auth.test.ts:1-50`: test style to copy
## Do
- ...
## Verify
- Add test `rejects the 101st request within a minute` in `src/api/middleware/ratelimit.test.ts`
- `factory test src/api/middleware/ratelimit.test.ts` must pass, then the full gate
## Constraints
- Expected size: ~80 lines across 2 files
- Don't touch: ...
## Out of scope
- ...
EOF
```
Packet rules:
- 120 lines or fewer. Self-contained: assume the reader knows nothing beyond the packet. The ACs it covers are injected automatically.
- Use exact paths with **line ranges** (`path:A-B`) or a symbol (`path (functionName)`). These refs are tracked; if later merges move or change those lines, `factory stale` flags them.
- Name the exact test to add and the file it goes in. Name the existing pattern to copy.
- Spell out out-of-scope items, so the builder doesn't wander.

## 6. Check and queue
```sh
factory cat plan 4     # task list, AC coverage, every packet: read it like the builder would
factory ready 4        # validates: spec has ACs, every AC covered, at least one task
```
Fix anything `ready` complains about. Then tell the user it's queued, and that `factory run` builds it.

## Bugs
1. **Investigate before writing.** You have the context budget; the builder doesn't. Reproduce the bug mentally from the report, then search the code (`rg`, read the relevant windows) until you can name the likely cause with `path:A-B` refs.
2. `factory add "<what's broken, imperatively: Fix ...>" --kind fix --scope <area> -p 1` (priority p1 unless the user says otherwise).
3. `factory internal template bug` shows the skeleton (`factory cat spec <id>` shows it too, for a fix item). Fill in Repro (numbered steps), Expected, Actual, Suspected area (path refs), then the ACs. **AC1 must be: "a regression test reproduces the bug and passes after the fix"** (`factory ready` checks this). Then Out of scope.
4. Usually 1–2 tasks. The first task's packet starts with writing the failing regression test, then the fix. Put the suspected-cause refs in its "Read first".

## Checking the queue
```sh
factory board          # health header + features by status, each with its next step (--kind fix for bugs)
factory show 4         # one feature: tasks, worktree, recent events, park reason
factory stale          # packets whose referenced code changed since they were written
```
Parked features keep their worktree for inspection. After fixing the cause (often a refresh, below), resume with `factory set <id> --status ready`. Change priority with `factory set <id> -p 1`.

## Refresh mode (stale packets)
When other features merge, the code a packet points at can move or change.
```sh
factory stale                  # lists stale refs for todo tasks; pure line shifts are auto-fixed
factory brief refresh 4 T2     # the packet plus the relevant diff hunks for each broken ref
factory write task 4 T2 <<'EOF'
...                            # rewritten packet: same scope and intent, updated refs
EOF
```
Keep the task's scope identical; only update refs, line ranges, and instructions that the code change invalidated. During full plan, run `factory stale` first if the feature builds on recently merged work.

## Init mode (one-time setup)
When the user asks to init or set up the factory for this repo:
1. If `factory board` errors with no factory db (`.factory/` is missing), run `factory init`.
2. Run `factory internal template init` and follow it exactly.
3. Finish by telling the user to write features with this skill, then run `factory run`.
