#!/usr/bin/env bash
# Build the factory binary into ~/.local/bin and symlink the opencode agents/commands from this checkout.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
GOBIN="$HOME/.local/bin" go install .
factory internal install --link "$PWD" "$@"
