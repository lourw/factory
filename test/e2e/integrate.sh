#!/usr/bin/env bash
# e2e: a feature whose merge with factory breaks the gate gets an integration task and still merges.
set -u
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SP="${FACTORY_E2E_DIR:-$(mktemp -d)}"
[ -z "${FACTORY_E2E_DIR:-}" ] && trap 'rm -rf "$SP"' EXIT
mkdir -p "$SP/bin" && (cd "$HERE/../.." && go build -o "$SP/bin/factory" .) || exit 1
export XDG_DATA_HOME=$SP/e2ei-xdg OPENCODE_CONFIG_DIR=$SP/e2ei-oc CLAUDE_CONFIG_DIR=$SP/e2ei-claude
export PATH=$SP/bin:$PATH FACTORY_AGENT_CMD="$HERE/stub-agent.sh {agent} {title} {message}"
R=$SP/e2ei; rm -rf $R $XDG_DATA_HOME; mkdir -p $R; cd $R
fail=0; assert() { if eval "$2"; then echo "  ok: $1"; else echo "  FAIL: $1"; fail=1; fi; }
git init -q -b main && git config user.email t@t && git config user.name t
echo base > base.txt && git add -A && git commit -qm init
factory init >/dev/null
# Once needs-fix.txt exists, fixed.txt must list every feature file.
factory gate config test 'test ! -f needs-fix.txt || ls feature-*.txt | cmp -s - fixed.txt' >/dev/null
mk() { # title prio
  id=$(factory add "$1" -p $2 | cut -c1-3); id=$((10#$id))
  printf '## Acceptance criteria\n- **AC1**: works\n' | factory write spec $id >/dev/null
  factory task-add $id "do it" --covers AC1 >/dev/null
  printf '## Goal\nx\n\n## Do\n- it\n' | factory write task $id T1 >/dev/null
  factory ready $id >/dev/null; echo $id
}
A=$(mk "NEEDSFIX adds a requirement" 1); B=$(mk "Unaware feature" 2)
echo "== run (A and B in parallel)"
factory run --features 2 > $SP/int.out 2>&1
grep -E 'integration|merged|parked|gate' $SP/int.out
# Whichever of A/B merges second sees the other's commit and needs the integration task.
assert "integration task added after the post-merge gate failure" "grep -q 'gate failed after merging factory: integration task T2 added' $SP/int.out"
n=0; for i in $A $B; do factory show $i | grep -q 'T2.*Integrate with factory' && n=$((n+1)); done
assert "exactly one item lists the integration task" "[ $n = 1 ]"
assert "A and B merged" "factory show $A | grep -q 'status: merged' && factory show $B | grep -q 'status: merged'"
assert "factory has fixed.txt listing both features" "[ \"\$(git show factory:fixed.txt)\" = \"\$(printf 'feature-$A.txt\nfeature-$B.txt')\" ]"
[ $fail = 0 ] && echo "E2E-INTEGRATE: ALL PASS" || echo "E2E-INTEGRATE: FAILURES"
exit $fail
