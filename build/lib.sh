# shellcheck shell=bash
#
# Helpers for build/build.sh, kept apart so build/selftest.sh can test them.
# Everything here is plain functions: sourcing the file has no side effects.

# pacstrap exits 0 even when a package's install scriptlet or a hook fails, so
# the build reads its log instead. Exactly one failure is expected and
# allowed: sunshine's scriptlet ends in an unguarded `modprobe uhid`, which
# cannot work in a chroot (the chroot's modules belong to the image's kernel,
# not the builder's). build.sh redoes the part of that scriptlet that matters
# (setcap), and uhid is loaded at boot through modules-load.d.
#
# pacman prints "installing <pkg>..." before each package when its output is
# not a terminal, and "( n/m) <hook>" for each hook, so every failure can be
# pinned on a package or hook.
#
# Usage: check_pacstrap_log LOG   (0 = fine; otherwise prints what failed)
check_pacstrap_log() {
    awk '
        /^(installing|upgrading|reinstalling) [^ ]+\.\.\.$/ {
            pkg = $2; sub(/\.\.\.$/, "", pkg); out = ""; next
        }
        /^:: Running (pre|post)-transaction hooks/ { hooks = 1; pkg = "a hook"; out = ""; next }
        hooks && /^\( *[0-9]+\/[0-9]+\) / {
            pkg = "hook \"" substr($0, index($0, ")") + 2) "\""; out = ""; next
        }
        /^error: command failed to execute correctly/ {
            if (pkg == "sunshine" && out ~ /modprobe: [A-Z]+: [^\n]*uhid/) {
                print "    pacstrap: ignoring the expected sunshine scriptlet failure (modprobe uhid in a chroot)"
                next
            }
            printf "pacstrap: the scriptlet of %s failed:\n%s%s\n", pkg, out, $0
            bad = 1; next
        }
        /^Fatal glibc/ { print "pacstrap: " $0; bad = 1 }
        { out = out $0 "\n" }
        END { exit bad }
    ' "$1"
}

# Verify a manifest signature the way a device does, but with openssl rather
# than the Go code that made it (docs/CONTRACTS.md "Update format"):
#   PUBKEY  one line, base64 of the raw 32-byte ed25519 public key
#   SIG     base64 of the ed25519 signature over the exact bytes of FILE
#
# Usage: verify_ed25519 PUBKEY FILE SIG
verify_ed25519() {
    local tmp rc=0
    tmp=$(mktemp -d)
    # A raw ed25519 key becomes a DER SubjectPublicKeyInfo with this fixed
    # 12-byte prefix (SEQUENCE { SEQUENCE { OID 1.3.101.112 } BIT STRING }).
    { printf '\x30\x2a\x30\x05\x06\x03\x2b\x65\x70\x03\x21\x00'; base64 -d <"$1"; } >"$tmp/pub.der" &&
        base64 -d <"$3" >"$tmp/sig" &&
        openssl pkeyutl -verify -pubin -inkey "$tmp/pub.der" -keyform DER -rawin \
            -in "$2" -sigfile "$tmp/sig" >/dev/null 2>&1 || rc=1
    rm -rf "$tmp"
    return "$rc"
}

# The builder runs in an LXC whose /dev has no loop devices, though the kernel
# has plenty. Make the nodes; mount's own loop setup then asks the kernel for
# a free one (and releases it on umount).
loop_nodes() {
    [[ -e /dev/loop-control ]] || mknod -m 0660 /dev/loop-control c 10 237
    local i
    for i in $(seq 0 63); do
        [[ -e /dev/loop$i ]] || mknod -m 0660 "/dev/loop$i" b 7 "$i"
    done
}

# The value systemd-sysctl gives KEY in the image at ROOT: a file in
# /etc/sysctl.d replaces the same-named one in /usr/lib/sysctl.d (a link to
# /dev/null masks it), all files are read in file name order, and the last
# assignment wins. Prints nothing when no file sets KEY.
# Usage: effective_sysctl ROOT KEY
effective_sysctl() {
    local root=$1 re=${2//./[./]} name f target dirs=()
    for f in "$root/usr/lib/sysctl.d" "$root/etc/sysctl.d"; do
        if [[ -d $f ]]; then dirs+=("$f"); fi
    done
    if (( ${#dirs[@]} == 0 )); then return 0; fi
    find "${dirs[@]}" -maxdepth 1 -name '*.conf' -printf '%f\n' |
        sort -u | while read -r name; do
            f=$root/etc/sysctl.d/$name
            [[ -e $f || -L $f ]] || f=$root/usr/lib/sysctl.d/$name
            if [[ -L $f ]]; then
                target=$(readlink "$f")
                [[ $target != /dev/null ]] || continue
                [[ $target != /* ]] || f=$root$target
            fi
            cat "$f" 2>/dev/null || true
            echo
        done |
        sed -nE "s/^[[:space:]]*-?${re}[[:space:]]*=[[:space:]]*(.*[^[:space:]])[[:space:]]*\$/\\1/p" | tail -n 1
}

# Check a public key file: one line of base64 that decodes to 32 bytes.
# Usage: is_ed25519_pubkey FILE
is_ed25519_pubkey() {
    local bytes
    [[ -f $1 && $(wc -l <"$1") -le 1 ]] || return 1
    bytes=$(base64 -d <"$1" 2>/dev/null | wc -c) || return 1
    [[ $bytes == 32 ]]
}

# Check the firewall as the kernel's nftables parser sees it (nft -c changes
# nothing), with every optional port both closed and open, and make sure a
# broken config.json opens nothing. Needs CAP_NET_ADMIN.
# Usage: check_firewall SCRIPT RULES
check_firewall() {
    local script=$1 rules=$2 tmp cfg rc=0
    tmp=$(mktemp -d)
    for cfg in '{}' \
               '{"ssh":{"enabled":true},"web":{"https":true}}' \
               '{"ssh":{"enabled":true},"web":{"https":true,"allow_public":true}}' \
               'not json'; do
        printf '%s\n' "$cfg" >"$tmp/config.json"
        if ! VOS_NFT_RULES=$rules VOS_CONFIG=$tmp/config.json bash "$script" --check; then
            echo "firewall: the ruleset does not load with config.json = $cfg" >&2
            rc=1
        fi
    done
    # $tmp/config.json is the malformed one now.
    if VOS_NFT_RULES=$rules VOS_CONFIG=$tmp/config.json bash "$script" --print |
            grep -q '^add rule inet vos optional'; then
        echo "firewall: a malformed config.json opened ports" >&2
        rc=1
    fi
    rm -rf "$tmp"
    return "$rc"
}

# ------------------------------------------------------------- extensions --
# For build/extensions.sh (docs/CONTRACTS.md "Extensions").

# The filesystem UUID of extension ID's image, the same in every build: the
# name-based SHA-1 UUID (version 5) of "vaporos-ext-ID" in the URL namespace.
# Usage: ext_uuid ID
ext_uuid() {
    uuidgen --sha1 --namespace @url --name "vaporos-ext-$1"
}

# Whether PATH is a clean relative path, as a descriptor's paths must be: not
# empty, not absolute, and no empty, "." or ".." component.
# Usage: ext_rel_ok PATH
ext_rel_ok() {
    local p=$1 c
    local -a parts=()
    [[ -n $p && $p != /* && $p != */ && $p != *$'\n'* ]] || return 1
    IFS=/ read -ra parts <<<"$p"
    for c in "${parts[@]}"; do
        [[ -n $c && $c != . && $c != .. ]] || return 1
    done
}

# The extensions in DIR (DIR/<id>/extension.json), requirements first and
# otherwise by id, as the catalog lists them: the order they are built in.
# Fails, saying why, on a directory without extension.json, an id that is not
# its directory's name, a requirement DIR does not have, or a requires cycle.
# Usage: ext_order DIR
ext_order() {
    local dir=$1 d id
    local -A state=()
    local -a ids=() out=()
    [[ -d $dir ]] || return 0
    for d in "$dir"/*/; do
        [[ -d $d ]] || continue
        id=${d%/}
        id=${id##*/}
        if [[ ! -f $d/extension.json ]]; then
            echo "extensions/$id has no extension.json" >&2
            return 1
        fi
        if [[ ! $id =~ ^[a-z][a-z0-9-]{0,31}$ || $(jq -r '.id' "$d/extension.json" 2>/dev/null) != "$id" ]]; then
            echo "extensions/$id/extension.json: its id must be \"$id\", the name of its directory" >&2
            return 1
        fi
        ids+=("$id")
    done
    (( ${#ids[@]} )) || return 0
    mapfile -t ids < <(printf '%s\n' "${ids[@]}" | LC_ALL=C sort)
    for id in "${ids[@]}"; do
        _ext_visit "$id" "" || return 1
    done
    printf '%s\n' "${out[@]}"
}

# ext_order's depth-first walk, on ext_order's own dir, state and out.
_ext_visit() {
    local id=$1 by=$2 r list
    local -a reqs=()
    case ${state[$id]:-} in
        ok) return 0 ;;
        busy) echo "extensions: $id is part of a requires cycle" >&2; return 1 ;;
    esac
    if [[ ! $id =~ ^[a-z][a-z0-9-]{0,31}$ || ! -f $dir/$id/extension.json ]]; then
        echo "extensions/$by requires \"$id\", which extensions/ does not have" >&2
        return 1
    fi
    state[$id]=busy
    list=$(jq -r '.requires // [] | sort | .[]' "$dir/$id/extension.json") || {
        echo "extensions/$id/extension.json: cannot read its requires" >&2
        return 1
    }
    [[ -z $list ]] || mapfile -t reqs <<<"$list"
    for r in "${reqs[@]}"; do
        _ext_visit "$r" "$id" || return 1
    done
    state[$id]=ok
    out+=("$id")
}

# What extension ID in DIR requires, directly or not, in ext_order's order
# (ID itself left out).
# Usage: ext_closure DIR ID
ext_closure() {
    local dir=$1 x r list
    local -A want=()
    local -a todo=("$2") reqs=() order=()
    while (( ${#todo[@]} )); do
        x=${todo[0]}
        todo=("${todo[@]:1}")
        list=$(jq -r '.requires // [] | .[]' "$dir/$x/extension.json") || return 1
        reqs=()
        [[ -z $list ]] || mapfile -t reqs <<<"$list"
        for r in "${reqs[@]}"; do
            if [[ -z ${want[$r]:-} ]]; then
                want[$r]=1
                todo+=("$r")
            fi
        done
    done
    list=$(ext_order "$dir") || return 1
    [[ -z $list ]] || mapfile -t order <<<"$list"
    for x in "${order[@]}"; do
        if [[ -n ${want[$x]:-} ]]; then echo "$x"; fi
    done
}

# A hash of the tree under DIR: every entry's path and type, a directory's
# or file's mode, a file's content and a symlink's target. A missing DIR
# hashes like an empty one.
# Usage: ext_tree_hash DIR
ext_tree_hash() {
    local list="" p
    if [[ -d $1 ]]; then
        list=$(cd "$1" && find . -mindepth 1 -printf '%P\0' | LC_ALL=C sort -z |
            while IFS= read -r -d '' p; do
                if [[ -L $p ]]; then
                    printf 'l %s %s\n' "$p" "$(readlink "$p")"
                elif [[ -d $p ]]; then
                    printf 'd %s %s\n' "$p" "$(stat -c %a "$p")"
                elif [[ -f $p ]]; then
                    printf 'f %s %s %s\n' "$p" "$(stat -c %a "$p")" "$(sha256sum <"$p" | cut -d' ' -f1)"
                else
                    printf 'o %s\n' "$p"
                fi
            done) || return 1
    fi
    printf '%s' "$list" | sha256sum | cut -d' ' -f1
}

# The input key of an extension image: an image is made again only when it
# changes (docs/CONTRACTS.md, the manifest's "key"). It is the sha256 of the
# descriptor, the tree of its files/ directory (ext_tree_hash), PACKAGES (the
# resolved package list: repo, name, version, file and sha256 of each), the
# fetch[] sha256s, FLAGS (how mkfs.erofs runs) and each further FILE (the
# scripts that make the image, the requirements' keys). 64 hex digits.
# Usage: ext_input_key DESCRIPTOR FILESDIR PACKAGES FLAGS [FILE...]
ext_input_key() {
    local desc=$1 files=$2 pkgs=$3 flags=$4 f fetch tree
    shift 4
    [[ -f $desc && -f $pkgs ]] || return 1
    for f in "$@"; do
        [[ -f $f ]] || return 1
    done
    fetch=$(jq -r '[.fetch // [] | .[].sha256] | join(" ")' "$desc") || return 1
    tree=$(ext_tree_hash "$files") || return 1
    {
        echo "vaporos-ext-key 1"
        echo "descriptor $(sha256sum <"$desc" | cut -d' ' -f1)"
        echo "files $tree"
        echo "packages $(sha256sum <"$pkgs" | cut -d' ' -f1)"
        echo "fetch $fetch"
        echo "mkfs $flags"
        for f in "$@"; do
            echo "file ${f##*/} $(sha256sum <"$f" | cut -d' ' -f1)"
        done
    } | sha256sum | cut -d' ' -f1
}

# The fs-verity digest of FILE as docs/CONTRACTS.md defines an image's
# identity (SHA-256, 4096-byte blocks, no salt), by fsverity-utils: 64 hex
# digits.
# Usage: fsverity_hex FILE
fsverity_hex() {
    local out d
    out=$(fsverity digest --hash-alg=sha256 --block-size=4096 "$1") || return 1
    d=${out%% *}
    d=${d#sha256:}
    [[ $d =~ ^[0-9a-f]{64}$ ]] || return 1
    echo "$d"
}

# The changes to packages that were there before an install: those of BEFORE
# ("name version" lines, as `pacman -Q` prints them) that AFTER has at
# another version or not at all, and every package pacman's log ALPMLOG says
# it reinstalled, upgraded, downgraded or removed (a reinstall keeps the
# version). An extension may only add packages, so each one is a failure.
# Prints one line per change; fails if there is any.
# Usage: ext_pkg_changes BEFORE AFTER [ALPMLOG]
ext_pkg_changes() {
    local rc=0
    awk 'FILENAME == ARGV[1] { now[$1] = $2; next }
         !($1 in now) { print $1 " " $2 ": removed"; bad = 1; next }
         now[$1] != $2 { print $1 ": " $2 " -> " now[$1]; bad = 1 }
         END { exit bad }' "$2" "$1" || rc=1
    if [[ -n ${3:-} && -f $3 ]] &&
            grep -E '^\[[^]]*\] \[ALPM\] (reinstalled|upgraded|downgraded|removed) ' "$3"; then
        rc=1
    fi
    return "$rc"
}

# The package database entries (directories of local/) of the packages in
# PACKAGES ("name version" lines): name-version, one per line.
# Usage: ext_local_entries PACKAGES
ext_local_entries() {
    awk 'NF >= 2 { print $1 "-" $2 }' "$1"
}

# The packages with more than one entry in DIR, a package database's local/
# (an entry is name-pkgver-pkgrel). pacman sees only one of them, so a
# database with any is broken.
# Usage: ext_local_dupes DIR
ext_local_dupes() {
    [[ -d $1 ]] || return 0
    find "$1" -mindepth 1 -maxdepth 1 -type d -printf '%f\n' |
        sed -E 's/-[^-]+-[^-]+$//' | LC_ALL=C sort | uniq -d
}

# What an install into an overlay with upper layer UPPER removed under usr/
# from the layers below it: whiteouts (0/0 character devices) and opaque
# directories (trusted.overlay.opaque: removed, then made again). An image
# can only add to those layers, so each is a failure. Prints one line per
# removal; fails if there is any, or if it cannot read the xattrs.
# Usage: ext_upper_removals UPPER
ext_upper_removals() {
    local up=$1 p out="" opaque
    [[ -d $up/usr ]] || return 0
    while IFS= read -r -d '' p; do
        if [[ $(stat -c %t:%T "$up/$p") == 0:0 ]]; then
            out+="removed /$p (a whiteout)"$'\n'
        fi
    done < <(cd "$up" && find usr -type c -print0)
    if ! opaque=$(cd "$up" && getfattr -R -P --absolute-names -m '^trusted\.overlay\.opaque$' usr 2>&1); then
        printf 'getfattr cannot read the xattrs under %s/usr:\n%s\n' "$up" "$opaque" >&2
        return 1
    fi
    out+=$(sed -n 's|^# file: \(.*\)$|replaced /\1 (an opaque directory: removed, then made again)|p' <<<"$opaque")
    [[ -n $out ]] || return 0
    printf '%s\n' "${out%$'\n'}"
    return 1
}

# Readies TREE, a copy of the overlay upper layer an extension's packages
# were installed into, for an image that holds usr/ alone:
#  - a file the packages own (OWNED, as `pacman -Qlq` lists them) outside
#    usr/ is payload no extension may have, and a whiteout in usr/ is a file
#    of a lower layer the install removed, which no image can do: either
#    fails, naming each, and changes nothing;
#  - anything else outside usr/ is what their scriptlets and hooks left
#    behind (ld.so.cache, users, logs, caches under var/): removed;
#  - in usr/, files a LOWER layer (the base, a requirement) has that the
#    packages do not own: removed, so the lower layer's copy shows. Those are
#    hooks redoing a cache the base has (icon caches, mime.cache, ...; see
#    ext_stale_caches). So are directories that end up empty and that a
#    lower layer has as well.
# Prints "removed PATH" (relative to TREE) for every file it removes.
# Usage: ext_prune_tree TREE OWNED LOWER...
ext_prune_tree() {
    local tree=$1 owned_list=$2 p l top
    local -A owned=()
    local -a payload=() whiteouts=()
    shift 2
    while IFS= read -r p; do
        p=${p#/}
        [[ -n $p && $p != */ ]] || continue
        owned[$p]=1
        [[ $p == usr/* ]] || payload+=("$p")
    done <"$owned_list"
    if [[ -d $tree/usr ]]; then
        while IFS= read -r -d '' p; do
            if [[ $(stat -c %t:%T "$tree/$p") == 0:0 ]]; then whiteouts+=("$p"); fi
        done < <(cd "$tree" && find usr -type c -print0)
    fi
    if (( ${#payload[@]} )); then
        printf 'payload outside usr/: /%s\n' "${payload[@]}" >&2
    fi
    if (( ${#whiteouts[@]} )); then
        printf 'removed from a lower layer (a whiteout): /%s\n' "${whiteouts[@]}" >&2
    fi
    if (( ${#payload[@]} + ${#whiteouts[@]} )); then
        return 1
    fi
    for top in "$tree"/* "$tree"/.[!.]* "$tree"/..?*; do
        [[ -e $top || -L $top ]] || continue
        [[ ${top##*/} != usr ]] || continue
        (cd "$tree" && find "${top##*/}" ! -type d -printf 'removed %p\n') || return 1
        rm -rf "$top"
    done
    [[ -d $tree/usr ]] || return 0
    while IFS= read -r -d '' p; do
        [[ -z ${owned[$p]:-} ]] || continue
        for l in "$@"; do
            if [[ -e $l/$p || -L $l/$p ]]; then
                rm -f "$tree/$p"
                echo "removed $p"
                break
            fi
        done
    done < <(cd "$tree" && find usr ! -type d -print0)
    while IFS= read -r p; do
        [[ $p != usr && -n $(find "$tree/$p" -maxdepth 0 -empty) ]] || continue
        for l in "$@"; do
            if [[ -d $l/$p && ! -L $l/$p ]]; then
                rmdir "$tree/$p"
                break
            fi
        done
    done < <(cd "$tree" && find usr -type d | LC_ALL=C sort -r)
}

# The caches among PRUNED (ext_prune_tree's "removed PATH" lines) that hooks
# regenerate from inputs TREE ships: the image leaves them out, so the base's
# copy stays, and it does not know those inputs. Prints "PATH INPUTDIR" for
# each.
# Usage: ext_stale_caches TREE PRUNED
ext_stale_caches() {
    local tree=$1 p name inputs
    while IFS= read -r p; do
        p=${p#removed }
        name=${p##*/}
        case $name in
            icon-theme.cache | gschemas.compiled | giomodule.cache) inputs=${p%/*} ;;
            mime.cache) inputs=${p%/*}/packages ;;
            loaders.cache) inputs=${p%/*}/loaders ;;
            *) continue ;;
        esac
        if [[ -d $tree/$inputs && -n $(find "$tree/$inputs" ! -type d ! -name "$name" -print -quit) ]]; then
            echo "$p $inputs"
        fi
    done <"$2"
}

# Puts a downloaded fetch[] entry into an image: OUT gets the member EXTRACT
# of the tar archive FILE (FILE itself when EXTRACT is empty), mode 0644;
# with LICENSE, the archive's licence text goes into LICDIR under the
# member's base name. Fails, saying why, on a member the archive lacks or
# that is empty, and on a licence name another entry took with other text.
# Usage: ext_place_fetch FILE EXTRACT OUT [LICENSE LICDIR]
ext_place_fetch() {
    local f=$1 ex=$2 out=$3 lic=${4:-} dir=${5:-} name
    mkdir -p "${out%/*}" || return 1
    if [[ -z $ex ]]; then
        install -m0644 "$f" "$out" || return 1
    elif ! tar -xOf "$f" -- "$ex" >"$out" || [[ ! -s $out ]]; then
        echo "the archive has no member $ex, or it is empty" >&2
        return 1
    fi
    chmod 0644 "$out"
    [[ -n $lic ]] || return 0
    name=${lic##*/}
    if [[ -z $ex || $name == fetched.txt ]]; then
        echo "license_file $lic needs an archive (extract) and another name than fetched.txt" >&2
        return 1
    fi
    mkdir -p "$dir" || return 1
    if ! tar -xOf "$f" -- "$lic" >"$dir/.$name.tmp" || [[ ! -s $dir/.$name.tmp ]]; then
        rm -f "$dir/.$name.tmp"
        echo "the archive has no licence file $lic, or it is empty" >&2
        return 1
    fi
    if [[ -e $dir/$name && $(sha256sum <"$dir/$name") != "$(sha256sum <"$dir/.$name.tmp")" ]]; then
        rm -f "$dir/.$name.tmp"
        echo "another fetch[] entry's licence text is $name already, and it differs from $lic" >&2
        return 1
    fi
    mv "$dir/.$name.tmp" "$dir/$name"
    chmod 0644 "$dir/$name"
}
