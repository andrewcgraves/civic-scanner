#!/usr/bin/env bash
set -euo pipefail

usage="usage: $0 <current reports dir> <baseline reports dir>"
current="${1:?$usage}"
baseline="${2:?$usage}"
needs="${NEEDS:-"{}"}"
go_min="${GO_COVERAGE_MIN:-0}"
base_branch="${BASE_BRANCH:-main}"

go_total() {
  if [ -f "$1" ]; then tr -d '[:space:]' <"$1"; fi
}

web_metric() {
  if [ -f "$1" ]; then jq -r --arg metric "$2" '.total[$metric].pct' "$1"; fi
}

percent() {
  if [ -n "$1" ]; then printf '%.1f%%' "$1"; else printf -- '—'; fi
}

change() {
  if [ -z "$1" ] || [ -z "$2" ]; then
    printf -- '—'
    return
  fi
  awk -v now="$1" -v before="$2" 'BEGIN {
    d = sprintf("%.1f", now - before) + 0
    if (d == 0) printf "±0.0"; else printf "%+.1f", d
  }'
}

row() {
  local label="$1" job="$2" now="$3" before="$4" result
  result="$(jq -r --arg job "$job" '.[$job].result // "skipped"' <<<"$needs")"
  case "$result" in
    success) result="✅" ;;
    failure) result="❌" ;;
    skipped)
      printf '| %s | not run | %s | — | no changes |\n' "$label" "$(percent "$before")"
      return
      ;;
  esac
  printf '| %s | %s | %s | %s | %s |\n' "$label" "$(percent "$now")" "$(percent "$before")" "$(change "$now" "$before")" "$result"
}

baseline_shas() {
  local label name parts=()
  for pair in "Go:go-coverage" "Go unit:go-coverage-unit" "web:web-coverage"; do
    label="${pair%%:*}"
    name="${pair#*:}"
    if [ -f "$baseline/$name.sha" ]; then
      parts+=("$label \`$(head -c 7 "$baseline/$name.sha")\`")
    fi
  done
  if [ "${#parts[@]}" -eq 0 ]; then
    echo "No \`$base_branch\` baseline yet. It appears once CI has run on \`$base_branch\`."
  else
    local joined
    joined="$(printf '%s, ' "${parts[@]}")"
    echo "\`$base_branch\` column: latest report from \`$base_branch\` for each row (${joined%, })."
  fi
}

web_now="$current/web-coverage/coverage-summary.json"
web_before="$baseline/web-coverage/coverage-summary.json"

echo "### Coverage"
echo
echo "| | This PR | \`$base_branch\` | Change | |"
echo "| --- | --- | --- | --- | --- |"
row "Go, unit + integration" api-integration \
  "$(go_total "$current/go-coverage/coverage.total")" "$(go_total "$baseline/go-coverage/coverage.total")"
row "Go, unit only" api \
  "$(go_total "$current/go-coverage-unit/coverage.unit.total")" "$(go_total "$baseline/go-coverage-unit/coverage.unit.total")"
for metric in lines statements functions branches; do
  row "Web, $metric" web "$(web_metric "$web_now" "$metric")" "$(web_metric "$web_before" "$metric")"
done
echo
echo "Minimums: Go unit + integration $go_min%, web as set in \`web/vitest.config.ts\`. ✅ and ❌ are the job results, which include the minimum check."
echo
baseline_shas
if [ -n "${RUN_URL:-}" ]; then
  echo
  echo "[HTML reports]($RUN_URL#artifacts)"
fi
