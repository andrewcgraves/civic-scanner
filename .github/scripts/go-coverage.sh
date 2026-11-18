#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -lt 2 ] || [ "$#" -gt 3 ]; then
  echo "usage: $0 <label> <profile, relative to api/> [build tags]" >&2
  exit 2
fi
label="$1"
profile="$2"
tags="${3:-}"
min="${GO_COVERAGE_MIN:-0}"

cd "$(dirname "$0")/../../api"

module="$(go list -m)"
coverpkg="$(go list ./... | grep -v -e '/internal/httpapi/v1$' -e '/internal/testbin$' -e '/internal/testdb$' | paste -sd, -)"

bincov="$(mktemp -d)"
binprofile="$(mktemp)"
trap 'rm -rf "$bincov" "$binprofile"' EXIT

TESTBIN_COVERPKG="$coverpkg" TESTBIN_COVERDIR="$bincov" \
  go test -race -covermode=atomic -coverpkg="$coverpkg" -coverprofile="$profile" -tags="$tags" ./...

if [ -n "$(ls -A "$bincov")" ]; then
  go tool covdata textfmt -i="$bincov" -o "$binprofile"
  tail -n +2 "$binprofile" >>"$profile"
fi

report="$(go tool cover -func="$profile" | sed "s#^$module/##")"
total="$(awk '/^total:/ { sub(/%/, "", $NF); print $NF }' <<<"$report")"
go tool cover -html="$profile" -o "${profile%.*}.html"

echo "$report"
echo "$label coverage: $total% (minimum $min%)"

if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
  {
    echo "### $label coverage: $total%"
    echo
    echo "<details><summary>Per function</summary>"
    echo
    echo '```'
    echo "$report"
    echo '```'
    echo
    echo "</details>"
  } >>"$GITHUB_STEP_SUMMARY"
fi

if awk -v total="$total" -v min="$min" 'BEGIN { exit !(total < min) }'; then
  echo "::error::$label coverage $total% is below the minimum of $min%."
  exit 1
fi
