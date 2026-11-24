#!/usr/bin/env bash
# E2E for factory run/merge/land/clean. Everything lives under $SP; real configs untouched.
set -u
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SP="${FACTORY_E2E_DIR:-$(mktemp -d)}"
[ -z "${FACTORY_E2E_DIR:-}" ] && trap 'rm -rf "$SP"' EXIT
mkdir -p "$SP/bin" && (cd "$HERE/../.." && go build -o "$SP/bin/factory" .) || exit 1
export XDG_DATA_HOME=$SP/e2e-xdg OPENCODE_CONFIG_DIR=$SP/e2e-oc CLAUDE_CONFIG_DIR=$SP/e2e-claude
export PATH=$SP/bin:$PATH FACTORY_AGENT_CMD="$HERE/stub-agent.sh {agent} {title} {message}"
R=$SP/e2e2; rm -rf $R $XDG_DATA_HOME $OPENCODE_CONFIG_DIR $CLAUDE_CONFIG_DIR; mkdir -p $R; cd $R
fail=0
assert() { if eval "$2"; then echo "  ok: $1"; else echo "  FAIL: $1"; fail=1; fi; }

git init -q -b main && git config user.email t@t && git config user.name t
echo base > shared.txt && git add -A && git commit -qm init
factory init >/dev/null
factory gate config setup 'true' >/dev/null
factory gate config lint '! grep -rq "^<<<<<<<" --include=*.txt .' >/dev/null
factory gate config test 'test -f shared.txt' >/dev/null

mk() { # title kind scope ntasks packets?
  local id
  id=$(factory add "$1" --kind "$2" --scope "$3" | cut -c1-3); id=$((10#$id))
  printf '## Problem\nx\n\n## Acceptance criteria\n- **AC1**: a regression test reproduces the bug and passes after the fix\n' | factory write spec $id >/dev/null
  factory task-add $id "first" --covers AC1 >/dev/null
  if [ "$4" = 2 ]; then factory task-add $id "second" --deps T1 >/dev/null; fi
  if [ "$5" = yes ]; then
    for t in $(seq 1 "$4"); do printf '## Goal\npacket\n' | factory write task $id T$t >/dev/null; done
  fi
  factory ready $id >/dev/null 2>&1 || echo "ready-failed $id" >&2
  echo $id
}
A=$(mk "SHARED one" feat core 2 yes)
B=$(mk "SHARED two" fix core 1 no)
C=$(mk "FAILVERIFY three" chore misc 1 yes)
MAIN0=$(git rev-parse main)

echo "== dry run"; factory run --dry-run

echo "== run 1 (interrupted)"
STUB_SLEEP=1 factory run --slots 3 --features 2 > $SP/run1.out 2>&1 &
pid=$!; sleep 2.5; kill -INT $pid; wait $pid; echo "exit=$?"
tail -6 $SP/run1.out
assert "interrupt waited for jobs and stopped early" "grep -q 'stopping: waiting' $SP/run1.out && grep -q 'stopped early' $SP/run1.out"
assert "main untouched after run 1" '[ "$(git rev-parse main)" = "$MAIN0" ]'
assert "integration worktree removed after Ctrl-C" "! git worktree list | grep -q _factory"

echo "== user commits to main between runs (forces factory rebase)"
echo readme > README && git add README && git commit -qm "docs: readme"
MAIN1=$(git rev-parse main)

echo "== run 2 (resume)"
factory run --slots 3 --features 2 > $SP/run2.out 2>&1; echo "exit=$?"
grep -E 'conflict|parked|rebas|squashed|merged|remaining' $SP/run2.out | head -20
assert "A merged" "factory show $A | grep -q 'status: merged'"
assert "B merged (after conflict resolve)" "factory show $B | grep -q 'status: merged'"
assert "conflict was resolved by overseer" "grep -q 'conflicts resolved' $SP/run2.out"
assert "plan job ran for B" "grep -qE '\[00$B [a-z-]+\] plan' $SP/run1.out $SP/run2.out"
assert "C parked" "factory show $C | grep -q 'status: parked'"
assert "C keeps worktree" "factory show $C | grep -q '^worktree:'"
assert "summary lists parked C" "grep -q 'parked (1)' $SP/run2.out"
assert "main untouched by run 2" '[ "$(git rev-parse main)" = "$MAIN1" ]'
assert "factory contains main's new commit" "git merge-base --is-ancestor main factory"
echo "-- factory log"; git log --format='%s' main..factory
assert "squash commits are conventional" "git log --format=%s main..factory | grep -qx 'feat(core): SHARED one' && git log --format=%s main..factory | grep -qx 'fix(core): SHARED two'"
assert "exactly 2 squash commits on factory" '[ "$(git rev-list --count main..factory)" = 2 ]'
assert "merge-msg body used" "git log -1 --format=%b factory | grep -q 'Stub body for item'"
assert "shared.txt keeps both features" "git show factory:shared.txt | grep -q 'item $A' && git show factory:shared.txt | grep -q 'item $B'"
assert "merged branches deleted" "! git show-ref --verify --quiet refs/heads/factory-wip/00$A-shared-one && ! git show-ref --verify --quiet refs/heads/factory-wip/00$B-shared-two"
WT=$XDG_DATA_HOME/factory/worktrees/$(ls $XDG_DATA_HOME/factory/worktrees)
assert "merged worktrees removed" "[ ! -d $WT/00$A-shared-one ] && [ ! -d $WT/00$B-shared-two ]"
assert "integration worktree removed after run" "! git worktree list | grep -q _factory"
assert "user checkout untouched (on main, clean)" '[ "$(git symbolic-ref --short HEAD)" = main ] && [ -z "$(git status --porcelain)" ]'
factory board | head -3
assert "board header shows ahead count" "factory board | grep -q '+2 ahead of main → factory land'"

echo "== refuse run while factory is checked out"
git switch -q factory; out=$(factory run 2>&1); git switch -q main
assert "run refuses with clear message" 'grep -q "switch away from the factory branch first" <<<"$out"'
echo "== lock blocks land"
echo $$ > .factory/run.lock; out=$(factory land 2>&1); rm .factory/run.lock
assert "land refuses during active run" 'grep -q "run is active" <<<"$out"'

echo "== main diverges, then land (rebase + gate in temp worktree)"
echo more > NOTES && git add NOTES && git commit -qm "docs: notes"
factory internal config post_land 'echo POSTLAND $FACTORY_LANDED_RANGE'
out=$(factory land 2>&1); echo "$out"
assert "post_land ran with landed range" 'grep -Eq "POSTLAND [0-9a-f]+\.\.[0-9a-f]+" <<<"$out"'
assert "main fast-forwarded to factory" '[ "$(git rev-parse main)" = "$(git rev-parse factory)" ]'
assert "land kept the diverged main commit" "git log --format=%s main | grep -qx 'docs: notes'"
assert "land rebased + gated, temp worktree gone" "! git worktree list | grep -q _factory"
assert "checkout updated by land" "grep -q 'item $B' shared.txt"

assert "parked worktree moved out of its slot" "[ -d $WT/parked-00$C-failverify-three ] && [ ! -d $WT/slot-1 ] && [ ! -d $WT/slot-2 ]"
echo "== clean --parked"
factory clean --parked
assert "parked worktree removed" "[ ! -d $WT/parked-00$C-failverify-three ]"
assert "parked branch kept" "git show-ref --verify --quiet refs/heads/factory-wip/00$C-failverify-three"
factory set $C --status ready >/dev/null
assert "set --status ready unparks (back to the queue; next run gives it a slot)" "factory show $C | grep -q 'status: ready'"
factory set $C --status parked >/dev/null

echo "== bug ready check + help groups"
bid=$(factory add "Fix crash on empty list" --kind fix | cut -c1-3); bid=$((10#$bid))
printf '## Acceptance criteria\n- **AC1**: empty list renders\n' | factory write spec $bid >/dev/null
factory task-add $bid fix --covers AC1 >/dev/null
assert "ready refuses fix without regression AC" "! factory ready $bid 2>/dev/null"
assert "bug template used for empty fix spec" "factory add 'Fix other' --kind fix >/dev/null && factory cat spec $((bid+1)) | grep -q '^## Repro'"
printf '## Acceptance criteria\n- **AC1**: a regression test reproduces the bug and passes after the fix\n' | factory write spec $bid >/dev/null
assert "ready accepts fix with regression AC" "factory ready $bid >/dev/null"
factory set $bid --status dropped >/dev/null; factory set $((bid+1)) --status dropped >/dev/null
assert "board --kind filters" "factory board --kind chore | grep -q FAILVERIFY && ! factory board --kind feat | grep -q FAILVERIFY"
assert "help shows only the 8 human commands" '[ "$(factory --help | grep -cE "^  [a-z]")" = 8 ]'
assert "old names removed" "! factory gc 2>/dev/null && ! factory gate-set x y 2>/dev/null && ! factory template spec 2>/dev/null && ! factory ls 2>/dev/null"
assert "internal dispatch works" "factory internal template task | grep -q '## Goal'"
assert "show --md exports" "factory show $A --md | grep -q '^## feat(core): SHARED one'"

echo "== install"
factory internal install >/dev/null
assert "skills installed (claude + opencode)" "[ -f $CLAUDE_CONFIG_DIR/skills/factory-feature/SKILL.md ] && [ -f $OPENCODE_CONFIG_DIR/skills/factory-feature/SKILL.md ] && [ -f $OPENCODE_CONFIG_DIR/agents/factory-overseer.md ] && [ -f $OPENCODE_CONFIG_DIR/commands/factory-init.md ]"
factory internal install --uninstall >/dev/null
assert "uninstall clean" "[ ! -e $CLAUDE_CONFIG_DIR/skills/factory-feature ] && [ -z \"\$(ls -A $OPENCODE_CONFIG_DIR/agents)\" ]"
factory board
[ $fail = 0 ] && echo "E2E: ALL PASS" || echo "E2E: FAILURES"
exit $fail
