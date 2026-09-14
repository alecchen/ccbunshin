#!/bin/sh
# Generates the body for a GitHub release from the commits since the previous
# tag, one bullet per commit subject, followed by the full-changelog compare
# link. Deterministic: no AI service, no network beyond the compare link the
# reader may click.
#
# Usage: GITHUB_REF_NAME=v0.1.5 sh tests/release-notes.sh
set -eu

tag="${GITHUB_REF_NAME:?set GITHUB_REF_NAME to the tag being released}"
repo_url="${GITHUB_SERVER_URL:-https://github.com}/${GITHUB_REPOSITORY:-alecchen/ccbunshin}"

# A shallow or tagless checkout resolves nothing, which would otherwise publish
# an empty body and a compare link that 404s, with a green job. Fail instead.
# actions/checkout needs `fetch-depth: 0` to avoid this.
git rev-parse --verify --quiet "${tag}^{commit}" >/dev/null ||
  { echo "release-notes: tag $tag is not in this checkout (set fetch-depth: 0)" >&2; exit 1; }

# git describe rather than listing every tag by version, so a release cut from
# an older commit names the tag it branched from, not the newest tag overall.
# (No publish command is named literally in this file: tests/publish-scan.sh
# matches them even inside comments, backticks being command substitution.)
prev="$(git describe --tags --abbrev=0 "${tag}^" 2>/dev/null || true)"

if [ -n "$prev" ]; then
  range="$prev..$tag"
  changelog="**Full Changelog**: $repo_url/compare/$prev...$tag"
else
  range="$tag"
  changelog="**Full Changelog**: $repo_url/commits/$tag"
fi

# --no-merges: a merge commit's subject restates the branch it brought in.
# The prefix strip keeps the body prose: a repo writing Conventional Commits
# should publish "- report the pid", not "- fix: report the pid". Only the
# known types are stripped, so a prose subject like "Proxy: stop the daemon"
# survives - the same type list the commit-msg hook enforces, and the reason
# the older prose-era commits come through unchanged.
git log --no-merges --pretty='%s' "$range" \
  | sed -E 's/^(feat|fix|docs|style|refactor|perf|test|build|ci|chore|revert)(\([^)]+\))?!?: //' \
  | awk 'NF {print "- " $0}' || true

printf '\n%s\n' "$changelog"
