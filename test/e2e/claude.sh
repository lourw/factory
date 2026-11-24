#!/usr/bin/env bash
# e2e: backend claude launches `claude -p` (a fake on PATH) and merges a feature.
set -u
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SP="${FACTORY_E2E_DIR:-$(mktemp -d)}"
mkdir -p "$SP/bin" && (cd "$HERE/../.." && go build -o "$SP/bin/factory" .) || exit 1
export XDG_DATA_HOME=$SP/e2ec-xdg OPENCODE_CONFIG_DIR=$SP/e2ec-oc CLAUDE_CONFIG_DIR=$SP/e2ec-claude
export PATH=$SP/bin:$PATH
unset FACTORY_AGENT_CMD
cat > "$SP/bin/claude" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$*" >> "$SP/claude-args"
agent=""; prev=""
for a in "\$@"; do
  [ "\$prev" = "--append-system-prompt-file" ] && agent=\$(basename "\$a" .prompt.md)
  prev="\$a"
done
exec "$HERE/stub-agent.sh" "\$agent" x "\${@: -1}"
EOF
chmod +x "$SP/bin/claude"
R=$SP/e2ec; rm -rf $R $XDG_DATA_HOME "$SP/claude-args"; mkdir -p $R; cd $R
fail=0; assert() { if eval "$2"; then echo "  ok: $1"; else echo "  FAIL: $1"; fail=1; fi; }
git init -q -b main && git config user.email t@t && git config user.name t
printf 'one\ntwo\nthree\n' > lib.txt && git add -A && git commit -qm init
factory init >/dev/null; factory gate config test 'true' >/dev/null
factory internal config backend claude >/dev/null
assert "bad backend rejected" "! factory internal config backend nope >/dev/null 2>&1"
assert "bad --backend rejected" "! factory run --backend nope >/dev/null 2>&1"
id=$(factory add "Claude feature" -p 1 --scope lib | cut -c1-3); id=$((10#$id))
printf '## Acceptance criteria\n- **AC1**: works\n' | factory write spec $id >/dev/null
factory task-add $id "do it" --covers AC1 >/dev/null
printf '## Goal\nx\n\n## Read first (only these)\n- `lib.txt:1-2`: context\n\n## Do\n- it\n' | factory write task $id T1 >/dev/null
factory ready $id >/dev/null
echo "== run (backend claude)"
factory run --features 1 > $SP/claude.out 2>&1
grep -E 'merged|squashed|parked|can.t' $SP/claude.out
assert "feature merged" "factory show $id | grep -q 'status: merged'"
assert "claude -p invoked" "grep -q -- '-p ' $SP/claude-args"
assert "--model opus" "grep -q -- '--model opus' $SP/claude-args"
assert "--permission-mode dontAsk" "grep -q -- '--permission-mode dontAsk' $SP/claude-args"
assert "--no-session-persistence" "grep -q -- '--no-session-persistence' $SP/claude-args"
assert "--settings" "grep -q -- '--settings ' $SP/claude-args"
assert "builder settings allow factory" "grep -q 'Bash(factory \*)' .factory/agents/factory-builder.settings.json"
[ $fail = 0 ] && echo "E2E-CLAUDE: ALL PASS" || echo "E2E-CLAUDE: FAILURES"
