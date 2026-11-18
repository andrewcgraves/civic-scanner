#!/usr/bin/env bash
set -euo pipefail

if [ -z "${DATABASE_URL:-}" ]; then
  echo "DATABASE_URL must point at an empty database." >&2
  exit 2
fi
if [ -z "${BASE_DIR:-}" ] && [ -z "${BASE_REF:-}" ]; then
  echo "Set BASE_DIR to a checkout of the production code, or BASE_REF to a git ref to check out." >&2
  exit 2
fi

root="$(cd "$(dirname "$0")/../.." && pwd)"

if [ "$#" -gt 0 ]; then
  all_contract=true
  for file in "$@"; do
    if ! grep -qiE '^-- migration: contract[[:space:]]*$' "$root/$file"; then
      all_contract=false
    fi
  done
  if [ "$all_contract" = true ]; then
    echo "Skipped: every changed migration is marked '-- migration: contract'."
    echo "Destructive changes are expected to break the code in production."
    exit 0
  fi
fi

worktree=""
cleanup() {
  if [ -n "$worktree" ]; then
    git -C "$root" worktree remove --force "$worktree" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

base_dir="${BASE_DIR:-}"
if [ -z "$base_dir" ]; then
  worktree="$(mktemp -d)"
  git -C "$root" worktree add --detach "$worktree" "$BASE_REF" >/dev/null
  base_dir="$worktree"
fi

echo "==> Applying this change's migrations"
(cd "$root/api" && go run ./cmd/migrate up)

echo "==> Running the API integration tests of $(git -C "$base_dir" rev-parse --short HEAD) against the migrated database"
if ! (cd "$base_dir/api" && go test -tags=integration -count=1 ./...); then
  echo
  echo "::error::Migration is not backward compatible: the integration tests of the code in production fail against the new schema."
  echo "For a short window the old code runs against the new schema, so a migration may only add structures the old code ignores."
  echo "Split the change: expand now, and ship the destructive part later in a migration marked '-- migration: contract'."
  exit 1
fi

echo "Backward compatible: the base branch's integration tests pass against the new schema."
