#!/bin/bash
# Updates GitHub Actions SHAs in workflow files based on versions in build.env
# Usage:
#   1. Update version numbers in hack/build.env (e.g., GH_CHECKOUT=7.0.0)
#   2. Run ./hack/gh-tag-to-sha.sh
#   3. Script fetches SHAs for specified versions and updates workflows
set -e

source hack/build.env

declare -A REPOS=(
    [GH_CHECKOUT]="actions/checkout"
    [GH_SETUP_GO]="actions/setup-go"
    [GH_LOGIN_ACTION]="docker/login-action"
    [GH_CODESPELL]="codespell-project/actions-codespell"
    [GH_COMMITLINT]="wagoid/commitlint-github-action"
    [GH_YAMLLINT]="ibiqlik/action-yamllint"
    [GH_GOLANGCI_LINT]="golangci/golangci-lint-action"
)

declare -A DATA

for key in "${!REPOS[@]}"; do
    repo="${REPOS[$key]}"
    version="${!key}"
    [ -z "$version" ] && echo "Error: $key not set in build.env" && exit 1
    tag="v$version"
    sha=$(git ls-remote --tags "https://github.com/$repo.git" | grep "refs/tags/$tag$" | awk '{print $1}')
    [ -z "$sha" ] && echo "Error: Tag $tag not found for $repo" && exit 1
    files=$(grep -l "$repo@" .github/workflows/*.yaml 2>/dev/null | tr '\n' ' ')
    DATA[$key]="$repo|$sha|$files"
    echo "$repo $tag -> $sha"
done

read -p "Update workflow files? (y/n) " -n 1 -r
echo
[[ ! $REPLY =~ ^[Yy]$ ]] && exit 0

for key in "${!DATA[@]}"; do
    IFS='|' read -r repo sha files <<< "${DATA[$key]}"
    [ -n "$files" ] && echo "Updating $repo:" && for f in $files; do echo "  $f"; sed -i.bak "s|$repo@[a-f0-9]\{40\}|$repo@$sha|g" "$f" && rm -f "$f.bak"; done
done
