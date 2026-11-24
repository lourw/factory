#!/usr/bin/env bash
# Stub agent for e2e tests: $1=agent $2=title $3=message; cwd = worktree.
agent="$1"; msg="$3"
args=$(sed -E 's/.*`factory brief ([^`]*)`.*/\1/' <<<"$msg")
set -- $args
kind=$1; item=$2; task=$3
echo "stub $agent: $args (cwd $PWD)"
if [ -n "${STUB_ENV_OUT:-}" ]; then
  echo "$agent ${args%% *} slot=${FACTORY_SLOT:-} cache=${FACTORY_CACHE:-} target=${CARGO_TARGET_DIR:-} cwd=$PWD" >> "$STUB_ENV_OUT"
fi
sleep "${STUB_SLEEP:-0}"
head=$(factory show "$item" 2>/dev/null | head -1)
case "$kind" in
plan)
  for t in $(factory show "$item" | grep '(no packet)' | grep -oE 'T[0-9]+' | sort -u); do
    printf '## Goal\nstub packet for %s\n\n## Verify\n- gate\n' "$t" | factory write task "$item" "$t"
  done
  echo "DECISION: planned"; echo "REASON: stub" ;;
build)
  if grep -q NOSTATUS <<<"$head" && [ ! -e .nostatus-done ]; then
    echo partial > partial.txt; touch .nostatus-done; echo "working..."; exit 0
  fi
  echo "item $item $task" >> "feature-$item.txt"
  if grep -q SHARED <<<"$head"; then echo "line from item $item $task" >> shared.txt; fi
  if grep -q LIBEDIT <<<"$head"; then sed -i 's/^five$/FIVE/' lib.txt; sed -i '1i new-b' lib.txt; sed -i '1i new-a' lib.txt; fi
  if grep -q NEEDSFIX <<<"$head"; then touch needs-fix.txt; ls feature-*.txt > fixed.txt; fi
  if factory show "$item" | grep -qE "\b$task\b.*Integrate with factory"; then ls feature-*.txt > fixed.txt; fi
  echo "Status: done" | factory write report "$item" "$task"
  printf 'STATUS: done\nTASK: %s\nNOTE: stub\n' "$task" ;;
verify)
  if grep -q FAILVERIFY <<<"$head"; then
    printf 'Verdict: FAIL\n- [blocking] stub: always fails\n' | factory write verify "$item" "$task"
    printf 'VERDICT: FAIL\nTOP: stub always fails\n'
  else
    printf 'VERDICT: PASS\nTOP: none\n'
  fi ;;
judge)
  flag="$(git rev-parse --git-common-dir)/stub-repack-$item"
  if grep -q REPACK <<<"$head" && [ ! -e "$flag" ]; then # rewrite T1 mid-judge, once
    touch "$flag"; printf '## Goal\nrewritten packet\n' | factory write task "$item" T1
  fi
  echo "Stub body for item $item." | factory write merge-msg "$item"
  printf 'DECISION: merge\nREASON: ok\n' ;;
refresh)
  factory cat task "$item" "$task" --raw | sed 's/lib.txt:4-6/lib.txt:6-8/' | factory write task "$item" "$task"
  printf 'DECISION: refreshed
REASON: updated range
' ;;
resolve)
  for f in $(git diff --name-only --diff-filter=U); do
    sed -i '/^<<<<<<< /d;/^=======$/d;/^>>>>>>> /d' "$f"; git add "$f"
  done
  if [[ "$args" == *rebase* ]]; then git rebase --continue >/dev/null; else git commit -q --no-edit; fi
  printf 'DECISION: resolved\nREASON: kept both sides\n' ;;
esac
