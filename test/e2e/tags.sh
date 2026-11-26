#!/usr/bin/env bash
# E2E for `tagging semver`: squash commits get vX.Y.Z tags that follow factory rebases and land.
set -u
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SP="${FACTORY_E2E_DIR:-$(mktemp -d)}"
[ -z "${FACTORY_E2E_DIR:-}" ] && trap 'rm -rf "$SP"' EXIT
mkdir -p "$SP/bin" && (cd "$HERE/../.." && go build -o "$SP/bin/factory" ./cmd/factory) || exit 1
export XDG_DATA_HOME=$SP/e2et-xdg OPENCODE_CONFIG_DIR=$SP/e2et-oc CLAUDE_CONFIG_DIR=$SP/e2et-claude
export PATH=$SP/bin:$PATH FACTORY_AGENT_CMD="$HERE/stub-agent.sh {agent} {title} {message}"
R=$SP/e2et; rm -rf $R $XDG_DATA_HOME $SP/e2et-remote.git; mkdir -p $R; cd $R
fail=0; assert() { if eval "$2"; then echo "  ok: $1"; else echo "  FAIL: $1"; fail=1; fi; }
git init -q -b main && git config user.email t@t && git config user.name t
echo base > base.txt && git add -A && git commit -qm init
factory init >/dev/null; factory gate config test 'true' >/dev/null
factory internal config tagging semver >/dev/null
mk() { # title kind prio
  id=$(factory add "$1" --kind $2 -p $3 --scope lib | cut -c1-3); id=$((10#$id))
  printf '## Problem\nx\n\n## Acceptance criteria\n- **AC1**: a regression test reproduces the bug and passes after the fix\n' | factory write spec $id >/dev/null
  factory task-add $id "do it" --covers AC1 >/dev/null
  printf '## Goal\nx\n' | factory write task $id T1 >/dev/null
  factory ready $id >/dev/null; echo $id
}
item_of() { git log -1 --format=%B "$1^{commit}" | sed -n 's/^Factory-Item: //p'; }
F=$(mk "Add thing" feat 1); X=$(mk "Fix thing" fix 2)
echo "== run (feat then fix)"
factory run --features 1 > $SP/tags1.out 2>&1
grep -E 'squashed|tagged' $SP/tags1.out
assert "both merged" "factory show $F | grep -q 'status: merged' && factory show $X | grep -q 'status: merged'"
assert "run log prints tags in merge order" "grep -o 'tagged v[0-9.]*' $SP/tags1.out | tr '\n' ' ' | grep -qx 'tagged v0.1.0 tagged v0.1.1 '"
assert "v0.1.0 on feat commit, v0.1.1 on fix commit" '[ "$(item_of v0.1.0)" = 00$F ] && [ "$(item_of v0.1.1)" = 00$X ]'
assert "tags are annotated with the subject" '[ "$(git cat-file -t v0.1.0)" = tag ] && git tag -n1 v0.1.0 | grep -q "feat(lib): Add thing"'
echo "== main diverges; next run rebases factory (syncTags) and tags a fix"
echo a > a.txt && git add a.txt && git commit -qm "docs: a"
Y=$(mk "Fix other" fix 1)
factory run --features 1 > $SP/tags2.out 2>&1
grep -E 'rebas|squashed|tagged|tag v' $SP/tags2.out
assert "next fix tagged v0.1.2" "grep -q 'tagged v0.1.2' $SP/tags2.out && [ \"\$(item_of v0.1.2)\" = 00$Y ]"
assert "old tags follow the rebase" "git merge-base --is-ancestor v0.1.0 factory && git merge-base --is-ancestor v0.1.1 factory"
echo "== main diverges again; land rebases factory"
echo b > b.txt && git add b.txt && git commit -qm "docs: b"
git init -q --bare $SP/e2et-remote.git && git remote add origin $SP/e2et-remote.git
factory land --push > $SP/tags3.out 2>&1; cat $SP/tags3.out
assert "landed" '[ "$(git rev-parse main)" = "$(git rev-parse factory)" ]'
assert "push sent main and the tags" '[ "$(git -C $SP/e2et-remote.git rev-parse main)" = "$(git rev-parse main)" ] && [ "$(git -C $SP/e2et-remote.git tag -l "v*" | tr "\n" " ")" = "v0.1.0 v0.1.1 v0.1.2 " ]'
for v in v0.1.0 v0.1.1 v0.1.2; do
  assert "$v reachable from main" "git merge-base --is-ancestor $v main"
done
assert "tags still on their items' commits" '[ "$(item_of v0.1.0)" = 00$F ] && [ "$(item_of v0.1.1)" = 00$X ] && [ "$(item_of v0.1.2)" = 00$Y ]'
[ $fail = 0 ] && echo "E2E-TAGS: ALL PASS" || echo "E2E-TAGS: FAILURES"
exit $fail
