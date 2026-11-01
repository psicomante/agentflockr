#!/bin/sh
# Build identity comes from the checkout, never from the runtime directory.
# Releases are tagged vX.Y.Z. A commit after a tag is a nightly build in
# semver form, vX.Y.Z-nightly.<commits since the tag>+<hash>, so it sorts below
# the next release. Untagged history builds as dev.
set -eu

if ! revision=$(git rev-parse --short=7 HEAD 2>/dev/null); then
    printf 'dev\n'
    exit 0
fi

if ! described=$(git describe --tags --long --match 'v[0-9]*' 2>/dev/null); then
    version="dev+$revision"
else
    # --long always prints <tag>-<commits>-g<hash>, even on the tag itself.
    tag=${described%-*-g*}
    rest=${described#"$tag"-}
    commits=${rest%%-*}
    if [ "$commits" = 0 ]; then
        version=$tag
    else
        version="$tag-nightly.$commits+$revision"
    fi
fi
if [ -n "$(git status --porcelain --untracked-files=normal)" ]; then
    version="$version.dirty"
fi
printf '%s\n' "$version"
