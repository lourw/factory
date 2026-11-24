#!/usr/bin/env bash
# E2E for phase-3 features: slots + cache/env, tagging, deps/overlap.
# Each scenario builds its own repo; everything lives under $SP. Real configs untouched.
set -u
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SP="${FACTORY_E2E_DIR:-$(mktemp -d)}"
[ -z "${FACTORY_E2E_DIR:-}" ] && trap 'rm -rf "$SP"' EXIT
mkdir -p "$SP/bin" && (cd "$HERE/../.." && go build -o "$SP/bin/factory" .) || exit 1
export XDG_DATA_HOME=$SP/p3-xdg OPENCODE_CONFIG_DIR=$SP/p3-oc CLAUDE_CONFIG_DIR=$SP/p3-claude
export PATH=$SP/bin:$PATH FACTORY_AGENT_CMD="$HERE/stub-agent.sh {agent} {title} {message}"
fail=0
assert() { if eval "$2"; then echo "  ok: $1"; else echo "  FAIL: $1"; fail=1; fi; }

# repo NAME: fresh repo with one file per feature area and a trivial gate
repo() {
  R=$SP/p3-$1; rm -rf "$R"; mkdir -p "$R"; cd "$R" || exit 1
  git init -q -b main && git config user.email t@t && git config user.name t
  echo base > shared.txt && git add -A && git commit -qm init
  factory init >/dev/null
  factory gate config test 'test -f shared.txt' >/dev/null
}

# feat TITLE [KIND] [extra add flags...]: one task with a packet; prints the id
feat() {
  local title=$1 kind=${2:-feat}; shift; [ $# -gt 0 ] && shift
  local id
  id=$(factory add "$title" --kind "$kind" --scope core "$@" | cut -c1-3); id=$((10#$id))
  printf '## Acceptance criteria\n- **AC1**: a regression test reproduces the bug and passes after the fix\n' | factory write spec $id >/dev/null
  factory task-add $id "do it" --covers AC1 >/dev/null
  printf '## Goal\nx\n\n## Read first\n- `shared.txt`: context\n' | factory write task $id T1 >/dev/null
  echo $id
}

scenario_slots() {
  echo "== slots: stable slot paths + {cache}/{slot} env"
  repo slots
  factory internal config worktree_root ../p3-slots-wt >/dev/null
  factory gate config --env CARGO_TARGET_DIR '{cache}/slot-{slot}-target' >/dev/null
  factory gate config test 'test -f shared.txt && echo "gate slot=$FACTORY_SLOT target=$CARGO_TARGET_DIR" >> '"$SP"'/p3-gate-env.txt' >/dev/null
  rm -f $SP/p3-gate-env.txt $SP/p3-agent-env.txt
  local a b
  a=$(feat "First thing"); b=$(feat "Second thing")
  factory ready $a >/dev/null; factory ready $b >/dev/null
  STUB_ENV_OUT=$SP/p3-agent-env.txt factory run --features 1 > $SP/p3-slots.out 2>&1
  grep -E 'started in slot|squashed' $SP/p3-slots.out
  local wt=$SP/p3-slots-wt/slot-1
  assert "both features merged" "factory show $a | grep -q 'status: merged' && factory show $b | grep -q 'status: merged'"
  assert "second feature recreated at the slot-1 path" "[ \$(grep -c 'started in slot 1 ($wt)' $SP/p3-slots.out) = 2 ]"
  assert "worktree_root config honored" "[ -d $SP/p3-slots-wt ]"
  assert "slot path free after merges" "[ ! -d $wt ]"
  assert "gate got slot + expanded cache env" "grep -q 'gate slot=1 target=$R/.factory/cache/slot-1-target' $SP/p3-gate-env.txt"
  echo more > NOTES && git add NOTES && git commit -qm "docs: notes"   # diverge main → land rebases + gates in slot int
  factory land >/dev/null
  assert "land's integration gate ran in slot int" "grep -q 'gate slot=int target=$R/.factory/cache/slot-int-target' $SP/p3-gate-env.txt"
  assert "land's temporary worktree is the integration path, removed after" "[ ! -d $SP/p3-slots-wt/_factory ]"
  assert "agents got slot + cache env" "grep -q 'factory-builder build slot=1 cache=$R/.factory/cache target=$R/.factory/cache/slot-1-target cwd=$wt' $SP/p3-agent-env.txt"
  assert "cache dir persists" "[ -d $R/.factory/cache ]"
}

scenario_slots

[ $fail = 0 ] && echo "E2E-PHASE3: ALL PASS" || echo "E2E-PHASE3: FAILURES"
exit $fail
