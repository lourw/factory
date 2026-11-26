#!/usr/bin/env bash
# E2E: a verified task's packet is rewritten (by the stub judge, while the item is judging);
# the scheduler resets the worktree to rerun_from, rebuilds T1+T2, and the feature still merges.
set -u
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SP="${FACTORY_E2E_DIR:-$(mktemp -d)}"
[ -z "${FACTORY_E2E_DIR:-}" ] && trap 'rm -rf "$SP"' EXIT
mkdir -p "$SP/bin" && (cd "$HERE/../.." && go build -o "$SP/bin/factory" ./cmd/factory) || exit 1
export XDG_DATA_HOME=$SP/e2er-xdg OPENCODE_CONFIG_DIR=$SP/e2er-oc CLAUDE_CONFIG_DIR=$SP/e2er-claude
export PATH=$SP/bin:$PATH FACTORY_AGENT_CMD="$HERE/stub-agent.sh {agent} {title} {message}"
R=$SP/e2er; rm -rf $R $XDG_DATA_HOME; mkdir -p $R; cd $R
fail=0; assert() { if eval "$2"; then echo "  ok: $1"; else echo "  FAIL: $1"; fail=1; fi; }
git init -q -b main && git config user.email t@t && git config user.name t
echo base > base.txt && git add -A && git commit -qm init
factory init >/dev/null; factory gate config test 'true' >/dev/null
id=$(factory add "REPACK two tasks" --scope core | cut -c1-3); id=$((10#$id))
printf '## Acceptance criteria\n- **AC1**: works\n' | factory write spec $id >/dev/null
factory task-add $id "first" --covers AC1 >/dev/null
factory task-add $id "second" --deps T1 >/dev/null
for t in 1 2; do printf '## Goal\npacket\n' | factory write task $id T$t >/dev/null; done
factory ready $id >/dev/null

echo "== run"
factory run --features 1 > $SP/rerun.out 2>&1; echo "exit=$?"
grep -E 'rerun|build T|judge|merged|discarded' $SP/rerun.out
assert "rerun event recorded" "factory show $id --events 100 | grep -q 'rerun: packet changed (T1, T2)'"
assert "worktree reset to rerun_from" "grep -q 'rerun: worktree reset to' $SP/rerun.out"
assert "T1 and T2 built twice" '[ "$(grep -c "build T1 → done" $SP/rerun.out)" = 2 ] && [ "$(grep -c "build T2 → done" $SP/rerun.out)" = 2 ]'
assert "item merged" "factory show $id | grep -q 'status: merged'"
assert "squash holds only the rebuilt work" '[ "$(git show factory:feature-$id.txt | grep -c T1)" = 1 ] && [ "$(git show factory:feature-$id.txt | grep -c T2)" = 1 ]'
[ $fail = 0 ] && echo "E2E-RERUN: ALL PASS" || echo "E2E-RERUN: FAILURES"
exit $fail
