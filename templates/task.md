<!-- Budget: <= 120 lines. Header (task, status, covers, deps) and AC text are injected by the CLI. The builder reads ONLY this packet, lessons, and the files listed below. -->
## Goal
<one or two sentences>

## Read first (only these; use the line ranges)
- `path/to/file.ts:40-120`: <why it matters>
- `path/to/similar_feature.ts` (`functionName`): pattern to copy
- `path/to/existing.test.ts:1-60`: test style to copy

## Do
- <concrete change 1>
- <concrete change 2>

## Verify
- Add test `<name>` in `<test file>`: <what it asserts>
- `factory test <test file>` must pass, then the full gate

## Constraints
- Expected size: ~<N> changed lines across <N> files. If it's much bigger, stop and report.
- Don't touch: <files/areas>

## Out of scope
- <things the builder may notice but must not do>
