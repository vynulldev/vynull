#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-or-later
#
# The steps of .github/workflows/nightly.yml, run from a checkout with its
# tags.
#
#   nightly.sh check     whether HEAD (main) gets a nightly: not if a nightly
#                        or a release already has its commit. Writes build,
#                        sha and date to $GITHUB_OUTPUT.
#   nightly.sh publish   publishes dist/ as the nightly for $DATE, tagging
#                        $SHA nightly-$DATE (a second one that day replaces
#                        the first), then deletes all but the newest
#                        $NIGHTLY_KEEP nightlies, tags included. Needs
#                        GH_TOKEN.
#
# With DRY_RUN=1, publish prints the release notes and the nightlies it would
# delete, and changes nothing.
set -euo pipefail

repo=${GITHUB_REPOSITORY:-vynulldev/vynull}

check() {
	local sha published build=true
	sha=$(git rev-parse HEAD)
	published=$(git tag --points-at "$sha" --list 'nightly-*' 'v[0-9]*')
	if [ -n "$published" ]; then
		echo "${sha:0:7} is already published as $published; no nightly"
		build=false
	fi
	{
		echo "build=$build"
		echo "sha=$sha"
		echo "date=$(date -u +%F)"
	} >> "${GITHUB_OUTPUT:-/dev/stdout}"
}

publish() {
	: "${SHA:?the commit to publish}" "${DATE:?the date of the nightly}"
	local tag=nightly-$DATE version since
	tmp=$(mktemp -d) # not local: the EXIT trap runs after publish returns
	trap 'rm -rf "$tmp"' EXIT

	# The version the build reports, as a user sees it.
	tar -xzf "dist/vynull-nightly-$DATE-linux-amd64.tar.gz" -C "$tmp" vynull
	version=$("$tmp/vynull" --version)
	version=${version#vynull }

	# The changes since the nightly or release before this one.
	since=$(git describe --tags --abbrev=0 --match 'nightly-*' --match 'v[0-9]*' \
		--exclude "$tag" "$SHA" 2> /dev/null || true)

	{
		echo "The build of \`main\` for $DATE: \`$version\`."
		echo
		echo "**Not a release.** A nightly skips the checks a release gets before it ships, so don't take one to a gig. Changes that affect the decks are tested on hardware before they merge; if this build misbehaves, open an issue with its version (\`vynull --version\`, or the right end of the web UI's status bar)."
		echo
		echo "To check a download:"
		echo
		echo '```'
		echo 'sha256sum --check --ignore-missing SHA256SUMS'
		echo "gh attestation verify vynull-nightly-$DATE-linux-amd64.tar.gz --repo $repo"
		echo '```'
		if [ -n "$since" ]; then
			echo
			echo "Changes since $since:"
			echo
			git log --first-parent --format='- %s' "$since..$SHA"
		fi
	} > "$tmp/notes.md"

	if [ -n "${DRY_RUN:-}" ]; then
		cat "$tmp/notes.md"
		echo
		prune
		return
	fi

	git tag -f "$tag" "$SHA"
	git push -f origin "refs/tags/$tag"
	if gh release view "$tag" > /dev/null 2>&1; then
		gh release edit "$tag" --title "Nightly $DATE" --notes-file "$tmp/notes.md" --prerelease --latest=false
		gh release upload "$tag" dist/* --clobber
	else
		gh release create "$tag" dist/* --verify-tag --title "Nightly $DATE" \
			--notes-file "$tmp/notes.md" --prerelease --latest=false
	fi
	prune
}

# prune deletes all but the newest $NIGHTLY_KEEP nightlies: each release with
# its tag, or a tag left without its release.
prune() {
	local keep=${NIGHTLY_KEEP:?how many nightlies to keep} old
	git ls-remote --tags --refs origin |
		sed -n 's#.*[[:space:]]refs/tags/\(nightly-[0-9-]*\)$#\1#p' |
		sort -r | tail -n +$((keep + 1)) |
		while read -r old; do
			if [ -n "${DRY_RUN:-}" ]; then
				echo "would delete $old"
			elif gh release view "$old" > /dev/null 2>&1; then
				echo "deleting $old"
				gh release delete "$old" --cleanup-tag --yes
			else
				echo "deleting the tag $old"
				git push origin --delete "refs/tags/$old"
			fi
		done
}

case "${1:-}" in
check) check ;;
publish) publish ;;
*)
	echo "usage: $0 check|publish" >&2
	exit 2
	;;
esac
