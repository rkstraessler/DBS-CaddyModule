#!/usr/bin/env bash
set -euo pipefail

update_branch() {
    local number=$1
    local path="repos/$GITHUB_REPOSITORY/pulls/$number"
    local delay snapshot state base_ref repository mergeable base_sha head_sha behind response status

    for delay in 0 2 4; do
        if (( delay > 0 )); then
            sleep "$delay"
        fi
        snapshot=$(gh api --method GET "$path" --jq '
            [.state, .base.ref, (.head.repo.full_name // "-"), (.mergeable | tostring), .base.sha, .head.sha] | @tsv
        ') || return 1
        IFS=$'\t' read -r state base_ref repository mergeable base_sha head_sha <<< "$snapshot"
        if [[ "$state" != open || "$base_ref" != main || "$repository" != "$GITHUB_REPOSITORY" ]]; then
            return 0
        fi
        if [[ "$mergeable" != null ]]; then
            break
        fi
    done

    case "$mergeable" in
        false) printf '#%s: merge conflicts.\n' "$number"; return 0 ;;
        null) printf '#%s: mergeability pending.\n' "$number"; return 0 ;;
    esac

    behind=$(gh api --method GET "repos/$GITHUB_REPOSITORY/compare/$base_sha...$head_sha?per_page=1" --jq '.behind_by') || return 1
    if [[ "$behind" == 0 ]]; then
        return 0
    fi

    if response=$(gh api --method PUT "$path/update-branch" --include --silent -f "expected_head_sha=$head_sha" 2>/dev/null); then
        printf '#%s: GitHub accepted the branch update.\n' "$number"
        return 0
    fi
    read -r _ status _ <<< "$response"
    case "$status" in
        409|422) printf '#%s: GitHub declined the update (%s). The next run will recheck it.\n' "$number" "$status" ;;
        *) printf '::error::Pull request #%s: branch update failed (%s).\n' "$number" "${status:-no HTTP status}"; return 1 ;;
    esac
}

numbers=$(gh api --method GET "repos/$GITHUB_REPOSITORY/pulls?state=open&base=main&per_page=100" \
    --paginate --jq '.[] | select(.head.repo.full_name == .base.repo.full_name) | .number')

failed=0
while IFS= read -r number; do
    if [[ -n "$number" ]] && ! update_branch "$number"; then
        failed=1
    fi
done <<< "$numbers"
exit "$failed"
