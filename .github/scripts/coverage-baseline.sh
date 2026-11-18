#!/usr/bin/env bash
set -euo pipefail

dir="${1:?usage: $0 <output dir>}"
: "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY must be set}"
: "${BASE_BRANCH:?BASE_BRANCH must be set}"

mkdir -p "$dir"
for name in go-coverage go-coverage-unit web-coverage; do
  latest="$(
    gh api --paginate --slurp "repos/$GITHUB_REPOSITORY/actions/artifacts?name=$name&per_page=100" |
      jq -r --arg branch "$BASE_BRANCH" '
        [.[].artifacts[]
          | select((.expired | not)
              and .workflow_run.head_branch == $branch
              and .workflow_run.head_repository_id == .workflow_run.repository_id)]
        | sort_by(.created_at) | last
        | if . == null then "" else "\(.workflow_run.id) \(.workflow_run.head_sha)" end'
  )"
  if [ -z "$latest" ]; then
    echo "No $name report from $BASE_BRANCH."
    continue
  fi
  read -r run_id sha <<<"$latest"
  if gh run download "$run_id" -R "$GITHUB_REPOSITORY" -n "$name" -D "$dir/$name"; then
    echo "$sha" >"$dir/$name.sha"
    echo "$name: run $run_id at ${sha:0:7}"
  else
    echo "::warning::Could not download $name from run $run_id."
  fi
done
