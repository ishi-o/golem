#!/usr/bin/env bash
set -euo pipefail

: "${PATHS:?PATHS must be set (the release-please paths output)}"
: "${GH_TOKEN:?GH_TOKEN must be set}"

git fetch --tags origin
mkdir -p dist
for m in $(printf '%s' "$PATHS" | jq -r '.[]'); do
	tag=$(git tag -l "$m/v*" --sort=-creatordate | head -1)
	[ -n "$tag" ] || {
		echo "no tag for $m" >&2
		exit 1
	}
	name="golem-$(echo "$m" | tr '/' '-')-${tag//\//-}.tar.gz"
	tar -czf "dist/$name" "$m"
	gh release upload "$tag" "dist/$name" --clobber
done
