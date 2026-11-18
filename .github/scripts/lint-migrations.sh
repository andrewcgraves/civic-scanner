#!/usr/bin/env bash
set -euo pipefail

SQUAWK_VERSION="${SQUAWK_VERSION:-2.72.0}"
CONTRACT_EXCLUDE="require-lock-timeout,require-statement-timeout,ban-drop-column,ban-drop-table"

cd "$(dirname "$0")/../.."

if command -v squawk >/dev/null 2>&1; then
  squawk=(squawk)
else
  squawk=(npx --yes "squawk-cli@${SQUAWK_VERSION}")
fi

if [ "$#" -eq 0 ]; then
  shopt -s nullglob
  set -- api/migrations/*.sql
  shopt -u nullglob
fi
if [ "$#" -eq 0 ]; then
  echo "No migrations to lint."
  exit 0
fi

reporter=tty
if [ -n "${GITHUB_ACTIONS:-}" ]; then
  reporter=gcc
fi

failed=0
for file in "$@"; do
  args=(--reporter "$reporter" --stdin-filepath "$file")
  if grep -qiE '^-- \+goose NO TRANSACTION' "$file"; then
    args+=(--no-assume-in-transaction)
  else
    args+=(--assume-in-transaction)
  fi
  kind=expand
  if grep -qiE '^-- migration: contract[[:space:]]*$' "$file"; then
    kind=contract
    args+=("--exclude=${CONTRACT_EXCLUDE}")
  fi
  echo "==> $file ($kind)"
  if ! awk '/^-- \+goose Down/ { exit } { print }' "$file" | "${squawk[@]}" "${args[@]}"; then
    failed=1
  fi
done

if [ "$failed" -ne 0 ]; then
  echo
  echo "Migration lint failed. Destructive changes belong in a separate, later"
  echo "migration marked with a '-- migration: contract' line."
  exit 1
fi
