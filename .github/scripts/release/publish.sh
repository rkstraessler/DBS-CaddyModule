#!/usr/bin/env bash
set -euo pipefail

tag=$GITHUB_REF_NAME
version=${tag#v}
number='(0|[1-9][0-9]*)'
identifier='(0|[1-9][0-9]*|[0-9]*[A-Za-z-][A-Za-z0-9-]*)'
if [[ "$GITHUB_REF_TYPE" != tag || ! "$tag" =~ ^v$number\.$number\.$number(-$identifier(\.$identifier)*)?$ || ${#version} -gt 128 ]]; then
    echo 'Expected a Docker-compatible vMAJOR.MINOR.PATCH[-PRERELEASE] tag.' >&2
    exit 1
fi
revision=$(git rev-parse "$GITHUB_REF^{commit}")
[[ "$revision" == "$GITHUB_SHA" ]]
git merge-base --is-ancestor "$revision" refs/remotes/origin/main

case "${1:-}" in
    validate) exit 0 ;;
    publish) ;;
    *) exit 2 ;;
esac

owner=${GITHUB_REPOSITORY_OWNER,,}
image="ghcr.io/$owner/caddy-domainbestellsystem"
ref="$image:$version"
printf '%s' "$GH_TOKEN" | docker login ghcr.io --username "$GITHUB_ACTOR" --password-stdin
trap 'docker logout ghcr.io >/dev/null' EXIT

if manifest_status=$(docker manifest inspect "$ref" 2>&1); then
    docker pull "$ref"
    [[ "$(docker image inspect --format '{{.Id}}' "$ref")" == "$(docker image inspect --format '{{.Id}}' dbs-caddy:release)" ]]
else
    if ! grep -Eiq 'manifest unknown|no such manifest|not[ -]found' <<< "$manifest_status"; then
        # GHCR can deny a manifest lookup before the package exists.
        if ! grep -Eiq ': denied$' <<< "$manifest_status"; then
            printf '%s\n' "$manifest_status" >&2
            exit 1
        fi
        package_status=$(gh api --include --silent "users/$owner/packages/container/caddy-domainbestellsystem" 2>/dev/null) || true
        if ! grep -Eq '^HTTP/[0-9.]+ 404([[:space:]]|$)' <<< "$package_status"; then
            printf '%s\n' "$manifest_status" >&2
            exit 1
        fi
    fi
    docker tag dbs-caddy:release "$ref"
    docker push "$ref"
fi

digest=$(docker buildx imagetools inspect "$ref" --format '{{json .Manifest}}' | jq -er '.digest')
release_flags=()
if [[ "$version" == *-* ]]; then
    release_flags+=(--prerelease --latest=false)
else
    # Keep channel aliases monotonic when backport releases are published.
    stable=$(gh api --paginate "repos/$GITHUB_REPOSITORY/releases" \
        --jq '.[] | select(.draft == false and .prerelease == false) | .tag_name')
    stable=$({ printf '%s\n' "$stable" | sed -nE 's/^v([0-9]+\.[0-9]+\.[0-9]+)$/\1/p'; printf '%s\n' "$version"; } | sort -V)
    major=${version%%.*}
    minor=${version%.*}
    for channel in "$major" "$minor" latest; do
        case "$channel" in
            latest) candidates=$stable ;;
            *) candidates=$(awk -v prefix="$channel." 'index($0, prefix) == 1' <<< "$stable") ;;
        esac
        if [[ "$(tail -n 1 <<< "$candidates")" == "$version" ]]; then
            docker buildx imagetools create --prefer-index=false --tag "$image:$channel" "$image@$digest"
            alias_digest=$(docker buildx imagetools inspect "$image:$channel" --format '{{json .Manifest}}' | jq -er '.digest')
            [[ "$alias_digest" == "$digest" ]]
            if [[ "$channel" == latest ]]; then
                release_flags+=(--latest)
            fi
        elif [[ "$channel" == latest ]]; then
            release_flags+=(--latest=false)
        fi
    done
fi

if ! gh release view "$tag" --repo "$GITHUB_REPOSITORY" >/dev/null 2>&1; then
    gh release create "$tag" --repo "$GITHUB_REPOSITORY" --verify-tag --generate-notes \
        --notes "$(printf '![REIKEM DBS Caddy Module release banner](https://raw.githubusercontent.com/%s/%s/.github/assets/release-banners/stable.png)\n\nContainer image: `%s@%s`' "$GITHUB_REPOSITORY" "$revision" "$image" "$digest")" "${release_flags[@]}"
fi
printf 'Published %s@%s\n' "$image" "$digest"
