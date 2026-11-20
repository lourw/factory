---
description: One-time factory setup for this repo - discover setup/gate commands and write the code map
agent: factory-overseer
---

Set up this repository for the factory. Work interactively with the user; ask when you're unsure.

1. If `factory gate config` errors with "no factory db", run `factory init` first.
2. **Discover commands.** Read CI config (`.github/workflows`, etc.), package manifests, and the README. CI is the source of truth for "passing". Find:
   - `setup`: what makes a fresh checkout buildable (install deps). It runs once per feature worktree and isn't part of the gate.
   - `format` (check mode), `lint`, `typecheck`, `test`, `build`
   - `test_focused`: run a single test file, with `{}` as the placeholder for the path
   Prefer quiet/CI reporters. For monorepos, prefer "affected" variants.
3. **Record them** with `factory gate config <name> '<command>'`. Run `factory gate` to validate. If a check fails on a clean tree for pre-existing reasons, confirm with the user, then re-set it with `factory gate config <name> '<command>' --known-failing`.
4. **Write the code map** (120 lines or fewer; agents read it instead of exploring). Research narrowly with `rg` and `ls`, then save it:
   `factory write map <<'EOF'` … `EOF`, following the sections in `factory internal template map` (stack, layout, key patterns to copy, testing, don't-read list).
5. Optionally seed `factory write lessons` with 3–5 non-obvious project conventions.
6. Report: the gate commands and their timings, known failures, and anything you couldn't determine. Then remind the user: write features with the factory-feature skill, then run `factory run`.
