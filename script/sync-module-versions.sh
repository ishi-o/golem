#!/usr/bin/env bash
set -euo pipefail

: "${PATHS:?PATHS must be set (the release-please paths output)}"
: "${GH_TOKEN:?GH_TOKEN must be set}"

git config user.name "release-please[bot]"
git config user.email "github-actions[bot]@users.noreply.github.com"
git fetch --tags origin

declare -A TAG
for m in $(printf '%s' "$PATHS" | jq -r '.[]'); do
	TAG[$m]=$(git tag -l "$m/v*" --sort=-creatordate | head -1)
	[ -n "${TAG[$m]}" ] || {
		echo "no tag for $m" >&2
		exit 1
	}
done

for mod in core store/sqlx store/mongodb store/redis sandbox/docker sandbox/kubernetes connector/feishu; do
	changed=0
	for dep in "${!TAG[@]}"; do
		[ "$dep" = "$mod" ] && continue
		if grep -q "github.com/ishi-o/golem/$dep v" "$mod/go.mod" 2>/dev/null; then
			(cd "$mod" && go mod edit -require="github.com/ishi-o/golem/$dep@${TAG[$dep]}")
			changed=1
		fi
	done
	if [ "$changed" = 1 ]; then
		(cd "$mod" && go mod tidy)
	fi
done

if ! git diff --quiet; then
	git add '**/go.mod' '**/go.sum'
	git commit -m "chore: sync module versions after release"
	git push origin HEAD:main
fi
