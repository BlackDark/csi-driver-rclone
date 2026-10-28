#!/usr/bin/env bash
# Usage: check-main-ci.sh <owner/repo> <sha>. Needs GH_TOKEN with actions: read and checks: read.
# Succeeds once a ci.yml run for <sha> on a push to main succeeded and its `check` check run
# passed; waits while such a run is still in progress, up to WAIT_SECONDS (default 3300).
set -euo pipefail

repo="${1:?usage: check-main-ci.sh <owner/repo> <sha>}"
sha="${2:?usage: check-main-ci.sh <owner/repo> <sha>}"
deadline=$(( $(date +%s) + ${WAIT_SECONDS:-3300} ))

while :; do
  runs=$(gh api "repos/$repo/actions/workflows/ci.yml/runs?head_sha=$sha&branch=main&event=push" \
    --jq '.workflow_runs[] | "\(.status) \(.conclusion) \(.check_suite_id) \(.html_url)"')
  pending=0
  while read -r status conclusion suite url; do
    [ -n "$status" ] || continue
    if [ "$status" != completed ]; then
      pending=1
    elif [ "$conclusion" = success ]; then
      check=$(gh api "repos/$repo/commits/$sha/check-runs?check_name=check&filter=all" \
        --jq ".check_runs[] | select(.check_suite.id == $suite) | .conclusion")
      if [ "$check" = success ]; then
        echo "CI on main passed for $sha: $url"
        exit 0
      fi
    fi
  done <<< "$runs"
  if [ "$pending" = 0 ]; then
    echo "::error::no successful ci.yml run with a passing check for $sha on main; push the commit to main and let CI pass first"
    exit 1
  fi
  if [ "$(date +%s)" -ge "$deadline" ]; then
    echo "::error::CI on main for $sha still running; re-run this job once it finished"
    exit 1
  fi
  echo "CI on main for $sha in progress; waiting"
  sleep 30
done
