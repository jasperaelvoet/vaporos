#!/usr/bin/env bash
#
# Checks out a forked project at its pinned upstream commit (forks/sources)
# and applies its patches.
#
#   forks/fetch.sh NAME DEST
#
# moonlight-qt also gets the moonlight-common-c patches, in its submodule.
set -euo pipefail

(( $# == 2 )) || { echo "usage: $0 NAME DEST" >&2; exit 2; }
name=$1 dest=$2
here=$(cd "$(dirname "$0")" && pwd)

source_of() {
    awk -v n="$1" '$1 == n { print $2, $3 }' "$here/sources"
}
read -r url commit < <(source_of "$name") || { echo "error: $name is not in forks/sources" >&2; exit 1; }

apply() { # DIR PATCHDIR
    local p
    shopt -s nullglob
    for p in "$2"/*.patch; do
        git -C "$1" apply --whitespace=nowarn "$p" || { echo "error: $(basename "$p") does not apply" >&2; exit 1; }
    done
    shopt -u nullglob
}

rm -rf "$dest"
git init -q "$dest"
git -C "$dest" fetch -q --depth 1 "$url" "$commit"
git -C "$dest" -c advice.detachedHead=false checkout -q FETCH_HEAD
if [[ -f $dest/.gitmodules ]]; then
    git -C "$dest" submodule update -q --init --recursive --depth 1
fi
apply "$dest" "$here/$name"
if [[ $name == moonlight-qt ]]; then
    apply "$dest/moonlight-common-c/moonlight-common-c" "$here/moonlight-common-c"
fi
