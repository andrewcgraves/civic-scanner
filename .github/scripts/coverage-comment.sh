#!/usr/bin/env bash
set -euo pipefail

usage="usage: $0 <current reports dir> <baseline reports dir>"
current="${1:?$usage}"
baseline="${2:?$usage}"
needs="${NEEDS:-"{}"}"
go_min="${GO_COVERAGE_MIN:-}"
web_thresholds="${WEB_COVERAGE_THRESHOLDS:-web/coverage-thresholds.json}"
base_branch="${BASE_BRANCH:-main}"

go_total() {
  if [ -f "$1" ]; then tr -d '[:space:]' <"$1"; fi
}

web_metric() {
  if [ -f "$1" ]; then jq -r --arg metric "$2" '.total[$metric].pct' "$1"; fi
}

web_min() {
  if [ -f "$web_thresholds" ]; then jq -r --arg metric "$1" '.[$metric] // empty' "$web_thresholds"; fi
}

percent() {
  if [ -n "$1" ]; then printf '%.1f%%' "$1"; else printf -- '—'; fi
}

minimum() {
  if [ -n "$1" ]; then printf '%g%%' "$1"; else printf 'none'; fi
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

status() {
  local result="$1" now="$2" min="$3"
  if [ "$result" = "skipped" ]; then
    printf 'not run, no changes'
  elif [ -z "$now" ]; then
    printf '❌ no report (job %s)' "$result"
  elif [ -z "$min" ]; then
    printf 'reported only'
  elif awk -v now="$now" -v min="$min" 'BEGIN { exit !(now >= min) }'; then
    printf '✅ meets minimum'
  else
    printf '❌ below minimum'
  fi
}

row() {
  local label="$1" job="$2" now="$3" before="$4" min="$5" result
  result="$(jq -r --arg job "$job" '.[$job].result // "skipped"' <<<"$needs")"
  if [ "$result" = "skipped" ]; then
    now=""
  fi
  printf '| %s | %s | %s | %s | %s | %s |\n' \
    "$label" "$(percent "$now")" "$(percent "$before")" "$(change "$now" "$before")" \
    "$(minimum "$min")" "$(status "$result" "$now" "$min")"
}

baseline_shas() {
  local label name joined parts=()
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
    joined="$(printf '%s, ' "${parts[@]}")"
    echo "\`$base_branch\` column: latest report from \`$base_branch\` for each row (${joined%, })."
  fi
}

web_now="$current/web-coverage/coverage-summary.json"
web_before="$baseline/web-coverage/coverage-summary.json"

echo "### Coverage"
echo
echo "| | This PR | \`$base_branch\` | Change | Minimum | Status |"
echo "| --- | --- | --- | --- | --- | --- |"
row "Go, unit + integration" api-integration \
  "$(go_total "$current/go-coverage/coverage.total")" "$(go_total "$baseline/go-coverage/coverage.total")" "$go_min"
row "Go, unit only" api \
  "$(go_total "$current/go-coverage-unit/coverage.unit.total")" "$(go_total "$baseline/go-coverage-unit/coverage.unit.total")" ""
for metric in lines statements functions branches; do
  row "Web, $metric" web "$(web_metric "$web_now" "$metric")" "$(web_metric "$web_before" "$metric")" "$(web_min "$metric")"
done
echo
echo "The Go minimum applies to unit + integration coverage. Unit-only coverage is shown for reference and has no minimum."
echo
baseline_shas
if [ -n "${RUN_URL:-}" ]; then
  echo
  echo "[HTML reports]($RUN_URL#artifacts)"
fi
