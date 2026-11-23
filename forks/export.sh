#!/usr/bin/env bash
#
# Rewrites forks/NAME/*.patch from the commits in the development checkout
# third_party/NAME (a clone of upstream at the commit in forks/sources).
#
#   forks/export.sh NAME...
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$(dirname "$here")

for name in "$@"; do
    base=$(awk -v n="$name" '$1 == n { print $3 }' "$here/sources")
    [[ -n $base ]] || { echo "error: $name is not in forks/sources" >&2; exit 1; }
    case $name in
        moonlight-common-c) checkout=$repo/third_party/moonlight-qt/moonlight-common-c/moonlight-common-c ;;
        *) checkout=$repo/third_party/$name ;;
    esac
    [[ -z $(git -C "$checkout" status --porcelain --untracked-files=no --ignore-submodules=all) ]] ||
        { echo "error: $checkout has uncommitted changes" >&2; exit 1; }
    rm -f "$here/$name"/*.patch
    mkdir -p "$here/$name"
    git -C "$checkout" format-patch -q --no-signature --zero-commit --no-stat -N -o "$here/$name" "$base..HEAD"
    echo "$name: $(ls "$here/$name"/*.patch 2>/dev/null | wc -l | tr -d ' ') patches"
done
