#!/usr/bin/env bash
# Usage: check-version.sh <tag> [Chart.yaml]. Fails unless chart version and appVersion equal the tag without "v".
set -euo pipefail

tag="${1:?usage: check-version.sh <tag> [Chart.yaml]}"
chart="${2:-$(cd "$(dirname "$0")/../.." && pwd)/charts/csi-driver-rclone/Chart.yaml}"
want="${tag#v}"

field() { sed -n "s/^$1: *[\"']\{0,1\}\([^\"']*\)[\"']\{0,1\} *\$/\1/p" "$chart" | head -n1; }
version=$(field version)
app=$(field appVersion)

fail=0
if [ "$version" != "$want" ]; then
  echo "::error::tag $tag does not match chart version '$version'"
  fail=1
fi
if [ "$app" != "$want" ]; then
  echo "::error::tag $tag does not match chart appVersion '$app'"
  fail=1
fi
exit "$fail"
