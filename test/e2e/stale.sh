#!/usr/bin/env bash
set -u
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SP="${FACTORY_E2E_DIR:-$(mktemp -d)}"
[ -z "${FACTORY_E2E_DIR:-}" ] && trap 'rm -rf "$SP"' EXIT
mkdir -p "$SP/bin" && (cd "$HERE/../.." && go build -o "$SP/bin/factory" ./cmd/factory) || exit 1
export XDG_DATA_HOME=$SP/e2es-xdg OPENCODE_CONFIG_DIR=$SP/e2es-oc CLAUDE_CONFIG_DIR=$SP/e2es-claude
export PATH=$SP/bin:$PATH FACTORY_AGENT_CMD="$HERE/stub-agent.sh {agent} {title} {message}"
R=$SP/e2es; rm -rf $R $XDG_DATA_HOME; mkdir -p $R; cd $R
fail=0; assert() { if eval "$2"; then echo "  ok: $1"; else echo "  FAIL: $1"; fail=1; fi; }
git init -q -b main && git config user.email t@t && git config user.name t
printf 'one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n' > lib.txt && git add -A && git commit -qm init
factory init >/dev/null; factory gate config test 'true' >/dev/null
mk() { # title prio packet-ref
  id=$(factory add "$1" -p $2 --scope lib | cut -c1-3); id=$((10#$id))
  printf '## Acceptance criteria\n- **AC1**: works\n' | factory write spec $id >/dev/null
  factory task-add $id "do it" --covers AC1 >/dev/null
  printf '## Goal\nx\n\n## Read first (only these)\n- `%s`: context\n\n## Do\n- it\n' "$3" | factory write task $id T1 >/dev/null
  factory ready $id >/dev/null; echo $id
}
X=$(mk "LIBEDIT top" 1 lib.txt:1-2); Y=$(mk "Shifted reader" 2 lib.txt:7-9); Z=$(mk "Changed reader" 3 lib.txt:4-6)
echo "== run --stale hold"
factory run --features 1 --stale hold > $SP/stale1.out 2>&1
grep -E 'auto-fixed|held|merged|squashed|summary' $SP/stale1.out
assert "X and Y merged" "factory show $X | grep -q 'status: merged' && factory show $Y | grep -q 'status: merged'"
assert "Y ref auto-remapped 7-9 → 9-11" "grep -q 'auto-fixed: fixed → \`lib.txt:9-11\`' $SP/stale1.out"
assert "Z held, still building" "grep -q 'held (1)' $SP/stale1.out && factory show $Z | grep -q 'status: building'"
echo "== stale / board / brief refresh"
factory stale | tee $SP/stale.list
assert "stale lists Z changed by X's commit" "grep -q '00$Z T1 changed .*lib.txt:4-6.*LIBEDIT top' $SP/stale.list"
assert "board shows stale count" "factory board | grep -q 'stale:1'"
assert "refresh brief has the relevant hunk" "factory brief refresh $Z T1 | grep -q '^+FIVE'"
echo "== run (refresh)"
factory run --features 1 > $SP/stale2.out 2>&1
grep -E 'refresh|merged|squashed|parked' $SP/stale2.out
assert "refresh job ran and Z merged" "grep -q 'packet refreshed' $SP/stale2.out && factory show $Z | grep -q 'status: merged'"
[ $fail = 0 ] && echo "E2E-STALE: ALL PASS" || echo "E2E-STALE: FAILURES"
exit $fail
