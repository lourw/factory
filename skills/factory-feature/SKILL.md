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
- **Retro**: refresh the code map and lessons after merges (see "Retro mode").

Decide feature vs bug from the user's wording ("broken", "crashes", "wrong", "regression" → bug). Ask if it's unclear.

## 1. Interview (brief)
Ask only what changes what gets built: scope edges, behavior on errors, what's out of scope. Two rounds at most. Record defaults you picked as assumptions.

## 2. Research, then import the whole plan
Research narrowly (search for symbols, read the relevant windows). Then write header, spec, and every task with its packet in one call:
```sh
factory import - --ready <<'EOF'
title: Add rate limiting to the public API
kind: feat
scope: api
prio: 2
=== spec
## Problem
<who is affected, what's wrong today: 2-4 sentences>
## Acceptance criteria
- **AC1**: GET /x past 100 requests/minute returns 429
- **AC2**: 429 responses carry Retry-After
## Out of scope
- ...
## Assumptions
- ...
=== T1: Add token-bucket limiter in src/api/middleware/ratelimit.ts [covers AC1]
## Goal
...
## Read first (only these; use the line ranges)
- `src/api/middleware/auth.ts:10-64`: middleware shape to copy
## Do
- ...
## Verify
- Add test `rejects the 101st request within a minute` in `src/api/middleware/ratelimit.test.ts`
## Constraints
- ~80 lines across 2 files; don't touch: ...
=== T2: Return Retry-After on 429 responses [covers AC2] [deps T1]
...
EOF
```
- **Header**: `title` (imperative), `kind` feat|fix|chore|refactor|perf|test|docs|build|ci, `scope` (short area), `prio` 1 (urgent)..4. Kind and scope become the conventional commit.
- **ACs**: each checkable by an automated test. Write `**ACn**:` exactly; the CLI parses it.
- **Tasks**: one builder session each: ≤150 changed lines on opencode, ≤400 on claude (`factory internal config` → effective budget), one coherent change, repo stays green. Cover every AC, risky work first, `[deps]` only for real dependencies.
- **Packets** (`factory internal template task` shows the skeleton): ≤120 lines, self-contained (covered ACs are injected). Refs with line ranges (`path:A-B`) or symbols (`path (fn)`); `factory stale` tracks them. Name the exact test to add, its file, the pattern to copy, and what's out of scope.
  - **opencode**: full detail: line-ranged reads, step-by-step Do.
  - **claude**: may be lighter: goal, the behaviour the ACs need, key file refs, the test to add, constraints.
- **Quick mode:** task blocks with no body, titles written as strong hints (area, files, pattern). The overseer writes the packets.

## 3. Check
```sh
factory cat plan <id>  # task list, AC coverage, every packet: read it like the builder would
```
If `--ready` complained, the item still exists: fix it with `write spec|task <id> ...` or `task-add`, then `factory ready <id>`. Tell the user it's queued and that `factory run` builds it.

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

## Retro mode (keep the map and lessons current)
Run it when the user asks for a retro, or when `factory board` shows `map stale (N merged since)`.
1. `factory internal retro`: what merged since the map was saved (diff stat, park reasons, verify findings, fix rounds, map size).
2. Rewrite only the map sections the digest shows changed; keep what's still true. Save with `factory write map` (120 lines or fewer).
3. Add 1-3 lessons only for friction that recurred (same park reason or finding across items): `factory write lessons --append`.
4. Report what changed in the map and which lessons you added (or that none were needed).
