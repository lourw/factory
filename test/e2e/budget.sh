#!/usr/bin/env bash
# A builder that ends without STATUS but leaves work gets a continue (fix) round instead of parking.
set -u
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SP="${FACTORY_E2E_DIR:-$(mktemp -d)}"
[ -z "${FACTORY_E2E_DIR:-}" ] && trap 'rm -rf "$SP"' EXIT
mkdir -p "$SP/bin" && (cd "$HERE/../.." && go build -o "$SP/bin/factory" ./cmd/factory) || exit 1
export XDG_DATA_HOME=$SP/e2eb-xdg OPENCODE_CONFIG_DIR=$SP/e2eb-oc CLAUDE_CONFIG_DIR=$SP/e2eb-claude
export PATH=$SP/bin:$PATH FACTORY_AGENT_CMD="$HERE/stub-agent.sh {agent} {title} {message}"
R=$SP/e2eb; rm -rf $R $XDG_DATA_HOME; mkdir -p $R; cd $R
fail=0; assert() { if eval "$2"; then echo "  ok: $1"; else echo "  FAIL: $1"; fail=1; fi; }
git init -q -b main && git config user.email t@t && git config user.name t
printf 'one\ntwo\nthree\n' > lib.txt && git add -A && git commit -qm init
factory init >/dev/null; factory gate config test 'true' >/dev/null
id=$(factory add "NOSTATUS partial" -p 1 --scope lib | cut -c1-3); id=$((10#$id))
printf '## Acceptance criteria\n- **AC1**: works\n' | factory write spec $id >/dev/null
factory task-add $id "do it" --covers AC1 >/dev/null
printf '## Goal\nx\n\n## Read first (only these)\n- `lib.txt:1-2`: context\n\n## Do\n- it\n' | factory write task $id T1 >/dev/null
factory ready $id >/dev/null
echo "== run"
factory run --features 1 > $SP/budget.out 2>&1
grep -E 'no STATUS|continue|fix T1|merged|parked' $SP/budget.out
assert "continued, not parked" "grep -q 'no STATUS, work left in worktree → continue' $SP/budget.out && ! grep -q 'parked' $SP/budget.out"
assert "merged" "factory show $id | grep -q 'status: merged'"
factory cat verify $id T1 > $SP/budget.verify 2>&1
assert "verify doc says ran out of budget" "grep -q 'ran out of budget' $SP/budget.verify"
assert "verify doc lists partial.txt" "grep -q 'partial.txt' $SP/budget.verify"
assert "verify doc has output tail" "grep -q 'working...' $SP/budget.verify"
[ $fail = 0 ] && echo "E2E-BUDGET: ALL PASS" || echo "E2E-BUDGET: FAILURES"
exit $fail
