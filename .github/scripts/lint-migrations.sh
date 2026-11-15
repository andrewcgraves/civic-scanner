#!/usr/bin/env bash
# Lint goose SQL migrations with Squawk (https://squawkhq.com).
#
#   .github/scripts/lint-migrations.sh [file...]   # default: api/migrations/*.sql
#
# Squawk does not understand goose annotations, so this wrapper adapts each file:
#   * Only the "-- +goose Up" half is linted. The Down half is cut off, since a
#     rollback is destructive by design (DROP TABLE ...) and would always fail.
#   * goose wraps a migration in a transaction unless the file says
#     "-- +goose NO TRANSACTION"; Squawk is told which case applies.
#   * A file containing the marker line "-- migration: contract" is a deliberate
#     contract step (see README, "Migrations"): dropping columns and tables is
#     allowed in it. Everything else is still checked.
set -euo pipefail

SQUAWK_VERSION="${SQUAWK_VERSION:-2.72.0}"
# Keep the first two in sync with excluded_rules in .squawk.toml: --exclude on
# the command line replaces the config file's list instead of adding to it.
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
