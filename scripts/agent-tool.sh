#!/bin/sh
# Compatibility entry point for maintenance tools owned by pyrycode-agents.
set -eu
ROOT="$(cd -P "$(dirname "$0")/.." && pwd)"
AGENTS="${AGENTS_REPO_PATH:-$ROOT/../pyrycode-agents}"
if [ ! -x "$AGENTS/bin/pyrycode-tool" ]; then
  echo "Install pyrycode-agents beside this checkout or set AGENTS_REPO_PATH." >&2
  exit 2
fi
cd "$ROOT"
exec "$AGENTS/bin/pyrycode-tool" "$@"
