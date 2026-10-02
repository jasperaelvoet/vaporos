# shellcheck shell=bash
#
# The extension images (docs/CONTRACTS.md "Extensions"). build/build.sh
# sources this file, calls ext_build_all once the rootfs is assembled and
# before root.erofs is made, and calls ext_cleanup from its cleanup trap. It
# uses build.sh's SRC, WORK, STAGE, ROOT, VOS, COMPRESS, its step/info/warn/
# die and erofs_flags, and build/lib.sh.
#
# First `vos ext validate` checks every descriptor; nothing here reads one
# before. Then each extensions/<id>/, requirements first:
#   key      the input key (ext_input_key): the descriptor, files/, the
#            packages it resolves to in the base's own sync snapshot, the
#            mkfs flags, these scripts and the requirements' keys. An image
#            made from the same inputs comes from $WORK/ext-cache, and is
#            still checked against this build's rootfs.
#   install  a fresh overlay on the rootfs (under the requirements' images),
#            into which pacman installs its packages from that snapshot (no
#            -y), recorded in a copy of the base's package database. It may
#            only add: a package of the base or a requirement that changes
#            or goes, or a path under usr/ it removes, fails the build.
#   tree     the overlay's upper layer without overlay xattrs and without
#            what scriptlets and hooks left behind (ext_prune_tree), plus
#            fetch[], strip[], files/ and usr/lib/vos/ext/<id>/
#   image    mkfs.erofs, then `vos ext check-tree` on the mounted image
#            against the rootfs and every image before it, and its fs-verity
#            digest by fsverity-utils and by vos, which must agree
# Then `vos ext catalog` writes the catalog and descriptors, which go into the
# rootfs, and the manifest's "extensions" object. The images stay out of
# $STAGE/vos, which becomes the ISO: extensions are never on it.

EXT_SRC=$SRC/extensions
# Per extension: its package database (CI trims its package cache against
# these and the base's) and local.added (the entries it adds there), logs,
# the overlay and the mounted image.
EXT_WORK=$WORK/ext
# <key>.raw and its .sha256, .build.json, .packages.txt and .local.tar (the
# package database entries it adds): images by input key, kept two weeks
# after their last use. The image is checked against its sha256 on reuse.
EXT_CACHE=$WORK/ext-cache
# fetch[] downloads, by sha256, kept two weeks after their last use.
EXT_FETCH=$WORK/ext-fetch
# What `vos ext catalog --stage` reads: ext-<id>.raw, <id>.json (the source
# descriptor), <id>.build.json (check-tree --json), <id>.key, <id>.packages.txt.
EXT_STAGE=$STAGE/ext
# What it writes: extensions.list, extensions.json, descriptors/<id>.json.
EXT_CATALOG=$STAGE/ext-catalog

# What ext_build_all made, for build.sh: the ids in catalog order, the
# mkfs.erofs flags, each image's fs-verity digest, and which were reused.
EXT_IDS=()
EXT_MKFS=()
declare -A EXT_FSVERITY=() EXT_REUSED=()

# pacman on DB, an extension's copy of the package database.
# Usage: ext_pacman DB ARGS...
ext_pacman() {
    local db=$1
    shift
    pacman --config "$SRC/build/pacman.conf" --dbpath "$db" "$@"
}

# The API filesystems package scriptlets expect, as pacstrap and arch-chroot
# mount them.
ext_api_mounts() {
    mount -t proc -o nosuid,noexec,nodev proc "$1/proc" &&
        mount -t sysfs -o nosuid,noexec,nodev,ro sys "$1/sys" &&
        mount -t devtmpfs -o mode=0755,nosuid udev "$1/dev" &&
        mount -t devpts -o mode=0620,gid=5,nosuid,noexec devpts "$1/dev/pts" &&
        mount -t tmpfs -o mode=1777,nosuid,nodev shm "$1/dev/shm" &&
        mount -t tmpfs -o mode=0755,nosuid,nodev run "$1/run" &&
        mount -t tmpfs -o mode=1777,strictatime,nodev,nosuid tmp "$1/tmp"
}

# Unmounts every overlay (with its API mounts) and then every image; an
# overlay holds the images and the rootfs under it.
ext_cleanup() {
    local m
    [[ -d $EXT_WORK ]] || return 0
    for m in "$EXT_WORK"/*/merged "$EXT_WORK"/*/mnt; do
        if mountpoint -q "$m" 2>/dev/null; then
            umount -R "$m" 2>/dev/null || umount -R -l "$m" || true
        fi
    done
}

ext_build_all() {
    local id list f
    step "Building extension images"
    ext_cleanup
    # A merged root has /dev mounted in it: never remove through a mount.
    if grep -q " $EXT_WORK/" /proc/self/mounts; then
        die "something is still mounted under $EXT_WORK; unmount it first"
    fi
    rm -rf "$EXT_WORK" "$EXT_STAGE" "$EXT_CATALOG"
    mkdir -p "$EXT_WORK" "$EXT_STAGE" "$EXT_CATALOG" "$EXT_CACHE" "$EXT_FETCH"

    for f in "$EXT_SRC"/*/extension.json; do
        [[ -f $f ]] || continue
        "$VOS" ext validate "$f" || die "${f#"$SRC"/} is not a valid descriptor (see above)"
    done
    list=$(ext_order "$EXT_SRC") || die "extensions/ cannot be built (see above)"
    EXT_IDS=()
    EXT_FSVERITY=()
    EXT_REUSED=()
    [[ -z $list ]] || mapfile -t EXT_IDS <<<"$list"

    # Compressed as the root is (zstd in release builds, lz4hc in debug
    # ones): the build mounts each image to check it, and the root's
    # compressor is the one it already mounts.
    mapfile -t EXT_MKFS < <(erofs_flags "$COMPRESS")
    EXT_MKFS=(-z"$COMPRESS" "${EXT_MKFS[@]}" -T0 --all-root)
    info "${#EXT_IDS[@]} extension(s): ${EXT_IDS[*]:-none}"
    info "mkfs.erofs ${EXT_MKFS[*]}"

    loop_nodes
    for id in "${EXT_IDS[@]}"; do
        ext_build "$id"
    done
    ext_catalog
    ext_cleanup
    find "$EXT_CACHE" "$EXT_FETCH" -maxdepth 1 -type f -mtime +14 -delete
}

# Makes (or reuses) ext-ID.raw in $EXT_STAGE and checks it.
ext_build() {
    local id=$1 x=$EXT_WORK/$1 desc=$EXT_SRC/$1/extension.json img=$EXT_STAGE/ext-$1.raw
    local list key c r e why a b fresh=0 started=$SECONDS
    local -a pkgs=() reqs=() others=()
    step "Extension $id"
    mkdir -p "$x/mnt"

    list=$(ext_closure "$EXT_SRC" "$id") || die "$id: cannot read what it requires"
    [[ -z $list ]] || mapfile -t reqs <<<"$list"
    printf '%s\n' "${reqs[@]}" | sed '/^$/d' >"$x/requires"
    [[ -z $list ]] || info "requires: ${reqs[*]}"

    # Its own copy of the package database: the base's, sync snapshot
    # included, plus the entries its requirements added.
    cp -a "$WORK/base/var/lib/pacman" "$x/db"
    rm -f "$x/db/db.lck"
    : >"$x/requires.keys"
    for r in "${reqs[@]}"; do
        while IFS= read -r e; do
            cp -a "$EXT_WORK/$r/db/local/$e" "$x/db/local/"
        done <"$EXT_WORK/$r/local.added"
        echo "$r $(<"$EXT_STAGE/$r.key")" >>"$x/requires.keys"
    done
    ext_db_single "$id"

    # What its packages resolve to, from that snapshot alone (no -y), so
    # the image matches the base's own package versions.
    list=$(jq -r '.packages // [] | .[]' "$desc") || die "$desc is not a valid descriptor"
    [[ -z $list ]] || mapfile -t pkgs <<<"$list"
    : >"$x/resolved.txt"
    if (( ${#pkgs[@]} )); then
        if ! ext_pacman "$x/db" -S --print --needed --noconfirm --print-format '%r %n %v %f %h' -- \
                "${pkgs[@]}" >"$x/resolved.raw" 2>"$x/resolve.log"; then
            # (pacman says which dependency it cannot satisfy on stdout.)
            cat "$x/resolved.raw" "$x/resolve.log" >&2
            die "$id: its packages do not resolve in the base's package snapshot (a stale cached base? REFRESH=1 renews it)"
        fi
        LC_ALL=C sort "$x/resolved.raw" >"$x/resolved.txt"
    fi

    key=$(ext_input_key "$desc" "$EXT_SRC/$id/files" "$x/resolved.txt" "${EXT_MKFS[*]}" \
        "$SRC/build/extensions.sh" "$SRC/build/lib.sh" "$SRC/build/pacman.conf" "$x/requires.keys") ||
        die "$id: cannot compute its input key"
    echo "$key" >"$EXT_STAGE/$id.key"
    cp "$desc" "$EXT_STAGE/$id.json"

    c=$EXT_CACHE/$key
    if [[ -e $c.raw ]] && ! why=$(ext_cache_check "$id" "$c"); then
        warn "$id: dropping the cached image of input key ${key:0:16}: $why"
        rm -f "$c.raw" "$c.sha256" "$c.build.json" "$c.packages.txt" "$c.local.tar"
    fi
    if [[ -e $c.raw ]]; then
        info "reusing the image of input key ${key:0:16} (checked again below)"
        ln -f "$c.raw" "$img" 2>/dev/null || cp "$c.raw" "$img"
        cp "$c.packages.txt" "$EXT_STAGE/$id.packages.txt"
        ext_local_entries "$c.packages.txt" >"$x/local.added"
        tar -xf "$c.local.tar" -C "$x/db/local" || die "$id: cannot unpack its cached package database entries"
        touch "$c.raw" "$c.sha256" "$c.build.json" "$c.packages.txt" "$c.local.tar"
        EXT_REUSED[$id]=1
    else
        info "input key ${key:0:16}: making the image"
        mkdir -p "$x/tree"
        : >"$x/packages.txt"
        : >"$x/local.added"
        if (( ${#pkgs[@]} )); then
            ext_install "$id" "${pkgs[@]}"
        fi
        cp "$x/packages.txt" "$EXT_STAGE/$id.packages.txt"
        ext_fill "$id"
        ext_mkfs "$id"
        fresh=1
    fi
    ext_db_single "$id"

    mount -t erofs -o ro,loop "$img" "$x/mnt" || die "$id: its image does not mount"
    [[ -d $x/mnt/usr ]] || die "$id: its image has no usr/"
    for r in "${EXT_IDS[@]}"; do
        [[ $r != "$id" ]] || break
        others+=(--other "$EXT_WORK/$r/mnt")
    done
    "$VOS" ext check-tree --id "$id" --descriptor "$desc" --tree "$x/mnt" --base "$ROOT" \
        "${others[@]}" --json "$EXT_STAGE/$id.build.json" || die "extension $id failed its checks"
    [[ -s $EXT_STAGE/$id.build.json ]] || die "vos ext check-tree wrote no $id.build.json"

    a=$(fsverity_hex "$img") || die "$id: fsverity digest cannot read its image"
    b=$("$VOS" ext digest "$img") || die "$id: vos ext digest cannot read its image"
    b=${b%% *}
    [[ $a == "$b" ]] || die "$id: fsverity-utils gives its image the digest $a, vos $b; they must agree"
    EXT_FSVERITY[$id]=$a

    if (( fresh )); then
        cp "$EXT_STAGE/$id.build.json" "$c.build.json"
        cp "$EXT_STAGE/$id.packages.txt" "$c.packages.txt"
        tar -C "$x/db/local" -cf "$c.local.tar" -T "$x/local.added"
        sha256sum <"$img" | cut -d' ' -f1 >"$c.sha256"
        # The image last, under its final name only once the rest is on
        # disk: it is what says the entry is there. (Nothing writes to an
        # image once made, so the stage and the cache can share it.)
        rm -f "$c.raw.tmp"
        ln "$img" "$c.raw.tmp" 2>/dev/null || cp "$img" "$c.raw.tmp"
        sync -- "$c.build.json" "$c.packages.txt" "$c.local.tar" "$c.sha256" "$c.raw.tmp"
        mv "$c.raw.tmp" "$c.raw"
        sync -- "$EXT_CACHE"
    fi
    info "ext-$id.raw  $(mib "$(stat -c %s "$img")") MiB  fsverity ${a:0:16}  (${EXT_REUSED[$id]:+reused, }$((SECONDS - started))s)"
}

# Whether cache entry C (the path without its extension) can stand in for
# extension ID's image in this build: complete, its image still the one its
# sha256 names, and its package database entries exactly its packages, none
# of which ID's database (the base's and the requirements') has yet. Says
# why not, and fails, otherwise.
# Usage: ext_cache_check ID C
ext_cache_check() {
    local id=$1 c=$2 f entries have
    for f in sha256 build.json packages.txt local.tar; do
        if [[ ! -f $c.$f ]]; then
            echo "it has no .$f"
            return 1
        fi
    done
    if [[ $(sha256sum <"$c.raw" | cut -d' ' -f1) != "$(<"$c.sha256")" ]]; then
        echo "its image is not the one its sha256 names"
        return 1
    fi
    if ! entries=$(set -o pipefail; tar -tf "$c.local.tar" | cut -d/ -f1 | LC_ALL=C sort -u) ||
            [[ $entries != "$(ext_local_entries "$c.packages.txt" | LC_ALL=C sort -u)" ]]; then
        echo "its package database entries are not those of its packages"
        return 1
    fi
    have=$(LC_ALL=C comm -12 <(ext_pacman "$EXT_WORK/$id/db" -Qq | LC_ALL=C sort) \
        <(cut -d' ' -f1 "$c.packages.txt" | LC_ALL=C sort))
    if [[ -n $have ]]; then
        echo "it adds packages the base or a requirement has already: ${have//$'\n'/ }"
        return 1
    fi
}

# Fails the build when extension ID's package database has two entries for
# one package: pacman would see only one of them.
# Usage: ext_db_single ID
ext_db_single() {
    local dupes
    dupes=$(ext_local_dupes "$EXT_WORK/$1/db/local")
    [[ -z $dupes ]] || die "$1: its package database has more than one version of ${dupes//$'\n'/, }"
}

# Installs PKGS into a fresh overlay on the rootfs (with the requirements'
# images over it) and leaves its upper layer, without overlay xattrs and
# pruned, in $x/tree; the packages it added in $x/packages.txt ("name
# version") and their database entries in $x/local.added. It fails if the
# install changed or removed a package that was there, or removed a path
# under usr/: an image can only add to the layers below it.
# Usage: ext_install ID PKG...
ext_install() {
    local id=$1 x=$EXT_WORK/$1 m=$EXT_WORK/$1/merged r lowerdir="" n e p inputs
    local -a reqs=() lowers=() new=()
    shift
    mapfile -t reqs <"$x/requires"
    # The last requirement depends on the ones before it: it goes on top.
    for r in "${reqs[@]}"; do
        lowers=("$EXT_WORK/$r/mnt" "${lowers[@]}")
    done
    lowers+=("$ROOT")
    for r in "${lowers[@]}"; do
        lowerdir+=${lowerdir:+:}$r
    done

    mkdir -p "$x/upper" "$x/work" "$m"
    mount -t overlay overlay \
        -o "lowerdir=$lowerdir,upperdir=$x/upper,workdir=$x/work,index=off,metacopy=off,redirect_dir=off" "$m" ||
        die "$id: cannot mount an overlay on the rootfs"
    ext_api_mounts "$m" || die "$id: cannot mount /proc, /sys, /dev, /run and /tmp in its overlay"

    ext_pacman "$x/db" -Q | LC_ALL=C sort >"$x/before.q"
    info "installing ${*}"
    if ! ext_pacman "$x/db" --root "$m" --cachedir /var/cache/pacman/pkg --logfile "$x/alpm.log" \
            -S --needed --noconfirm -- "$@" >"$x/pacman.log" 2>&1; then
        tail -n 40 "$x/pacman.log" >&2
        if grep -q 'returned error: 404' "$x/pacman.log"; then
            die "$id: the mirrors no longer have some of its packages: the cached base's package snapshot is stale (REFRESH=1 renews it; full log: $x/pacman.log)"
        fi
        die "$id: pacman could not install its packages (full log: $x/pacman.log; a stale cached base? REFRESH=1 renews it)"
    fi
    check_pacstrap_log "$x/pacman.log" >&2 ||
        die "$id: a package scriptlet or hook failed (full log: $x/pacman.log)"
    umount -R "$m" || die "$id: cannot unmount its overlay (does a scriptlet's process still run in it?)"

    ext_pacman "$x/db" -Q | LC_ALL=C sort >"$x/after.q"
    if ! ext_pkg_changes "$x/before.q" "$x/after.q" "$x/alpm.log" >"$x/changed"; then
        sed 's/^/      /' "$x/changed" >&2
        die "$id: installing its packages changed packages of the base or a requirement (above); an extension may only add packages"
    fi
    if ! ext_upper_removals "$x/upper" >"$x/removed"; then
        sed 's/^/      /' "$x/removed" >&2
        die "$id: installing its packages removed paths under usr/ of the base or a requirement (above); an extension may only add files"
    fi
    LC_ALL=C comm -13 "$x/before.q" "$x/after.q" >"$x/packages.txt"
    ext_local_entries "$x/packages.txt" >"$x/local.added"
    while IFS= read -r e; do
        [[ -d $x/db/local/$e ]] || die "$id: pacman lists $e, but its package database has no entry $e"
    done <"$x/local.added"
    mapfile -t new < <(cut -d' ' -f1 "$x/packages.txt")
    if (( ${#new[@]} )); then
        ext_pacman "$x/db" -Qlq -- "${new[@]}" >"$x/owned"
    else
        : >"$x/owned"
    fi
    info "$(wc -l <"$x/packages.txt") package(s): $(cut -d' ' -f1 "$x/packages.txt" | tr '\n' ' ')"

    # The upper layer as plain files: no trusted.overlay.* (an image must
    # not carry them; ext_upper_removals has read the opaque markers).
    rsync -aHAX --filter='-x trusted.overlay.*' --filter='-x system.*' "$x/upper/" "$x/tree/"
    rm -rf "$x/upper" "$x/work"

    if ! ext_prune_tree "$x/tree" "$x/owned" "${lowers[@]}" >"$x/pruned"; then
        die "$id: its packages install files outside usr/ (an extension image holds usr/ alone), or their install removed files under usr/ (above)"
    fi
    if [[ -s $x/pruned ]]; then
        n=$(wc -l <"$x/pruned")
        info "left out, as what scriptlets and hooks left behind ($n):"
        head -n 20 "$x/pruned" | sed 's/^removed /      /'
        (( n <= 20 )) || info "  ... and $((n - 20)) more ($x/pruned)"
    fi
    while read -r p inputs; do
        warn "$id: a hook redid /$p, which the image leaves out, so the base's copy stays; it does not know the files the extension ships in /$inputs/"
    done < <(ext_stale_caches "$x/tree" "$x/pruned")
}

# Adds fetch[], strip[], files/ and usr/lib/vos/ext/ID/ to $x/tree.
# Usage: ext_fill ID
ext_fill() {
    local id=$1 x=$EXT_WORK/$1 t=$EXT_WORK/$1/tree desc=$EXT_SRC/$1/extension.json
    local own=$EXT_WORK/$1/tree/usr/lib/vos/ext/$1 lics=$EXT_WORK/$1/tree/usr/share/licenses/$1
    local n i url sha ex lf dest lic f p
    n=$(jq '.fetch // [] | length' "$desc")
    for ((i = 0; i < n; i++)); do
        IFS=$'\x1f' read -r url sha ex lf dest lic < <(jq -r --argjson i "$i" \
            '.fetch[$i] | [.url, .sha256, (.extract // ""), (.license_file // ""), .dest, .license]
                        | join("\u001f")' "$desc")
        [[ $url == https://* && $sha =~ ^[0-9a-f]{64}$ ]] || die "$id: fetch[$i] needs an https url and a sha256"
        ext_rel_ok "$dest" || die "$id: fetch[$i].dest '$dest' is not a clean relative path"
        [[ -z $ex ]] || ext_rel_ok "$ex" || die "$id: fetch[$i].extract '$ex' is not a clean relative path"
        [[ -z $lf ]] || ext_rel_ok "$lf" || die "$id: fetch[$i].license_file '$lf' is not a clean relative path"
        f=$EXT_FETCH/$sha
        if [[ ! -s $f ]] || ! printf '%s  %s\n' "$sha" "$f" | sha256sum -c --quiet >/dev/null 2>&1; then
            info "downloading $url"
            curl -fsSL --proto '=https' --proto-redir '=https' --retry 3 --connect-timeout 30 \
                -o "$f.part" "$url" || die "$id: cannot download $url"
            mv "$f.part" "$f"
        fi
        if ! printf '%s  %s\n' "$sha" "$f" | sha256sum -c --quiet; then
            rm -f "$f"
            die "$id: $url is not the file its descriptor pins (sha256 $sha)"
        fi
        touch "$f"
        ext_place_fetch "$f" "$ex" "$own/$dest" "$lf" "$lics" || die "$id: fetch[$i] ($url) cannot be put in the image"
        mkdir -p "$lics"
        {
            printf '%s: %s\n  from %s\n  sha256 %s%s\n' "$dest" "$lic" "$url" "$sha" "${ex:+, member $ex}"
            [[ -z $lf ]] || printf '  licence text in %s, member %s\n' "${lf##*/}" "$lf"
        } >>"$lics/fetched.txt"
        info "fetched $dest ($lic${lf:+, licence text ${lf##*/}})"
    done

    while IFS= read -r p; do
        if ! { ext_rel_ok "$p" && [[ $p == usr/* ]]; }; then
            die "$id: strip '$p' is not a path under usr/"
        fi
        [[ -e $t/$p || -L $t/$p ]] || die "$id: strip names $p, which its packages do not ship (any more)"
        rm -rf "${t:?}/$p"
        info "stripped $p"
    done < <(jq -r '.strip // [] | .[]' "$desc")

    if [[ -d $EXT_SRC/$id/files ]]; then
        cp -a --no-preserve=ownership,xattr "$EXT_SRC/$id/files/." "$t/"
    fi

    install -Dm0644 "$desc" "$own/extension.json"
    install -Dm0644 "$x/packages.txt" "$own/packages.txt"
    jq -r '[.module_options // [] | .[] | "\(.module) \(.param)"] | unique | .[]' "$desc" >"$own/module-options"
    chmod 0644 "$own/module-options"
    chmod 0755 "$t"
}

# Makes $x/tree into ext-ID.raw in $EXT_STAGE.
# Usage: ext_mkfs ID
ext_mkfs() {
    local id=$1 x=$EXT_WORK/$1 img=$EXT_STAGE/ext-$1.raw uuid
    uuid=$(ext_uuid "$id") || die "uuidgen cannot derive $id's filesystem UUID"
    rm -f "$img.tmp"
    mkfs.erofs "${EXT_MKFS[@]}" -U "$uuid" --workers="$(nproc)" --quiet "$img.tmp" "$x/tree" ||
        die "$id: mkfs.erofs failed"
    mv "$img.tmp" "$img"
    rm -rf "$x/tree"
}

# Writes the catalog and the manifest entries from $EXT_STAGE, checks them
# against the images, and puts the catalog and descriptors into the rootfs.
ext_catalog() {
    local id f ids
    step "Writing the extension catalog"
    "$VOS" ext catalog --stage "$EXT_STAGE" --out "$EXT_CATALOG" || die "vos ext catalog failed"
    [[ -s $EXT_CATALOG/extensions.list ]] || die "vos ext catalog wrote no extensions.list"

    # The manifest entries describe exactly the images made here.
    ids=$(jq -cn '$ARGS.positional' --args "${EXT_IDS[@]}")
    jq -e --argjson ids "$ids" 'type == "object" and keys == ($ids | sort)' "$EXT_CATALOG/extensions.json" >/dev/null ||
        die "vos ext catalog's extensions.json does not list exactly ${EXT_IDS[*]:-no extension}"
    for id in "${EXT_IDS[@]}"; do
        f=$EXT_STAGE/ext-$id.raw
        jq -e --arg id "$id" --arg name "ext-$id.raw" --argjson size "$(stat -c %s "$f")" \
              --arg sha "$(sha256sum <"$f" | cut -d' ' -f1)" --arg fsv "${EXT_FSVERITY[$id]}" \
              --arg key "$(<"$EXT_STAGE/$id.key")" \
            '.[$id] | .name == $name and .size == $size and .sha256 == $sha
                      and .fsverity == $fsv and .key == $key' "$EXT_CATALOG/extensions.json" >/dev/null ||
            die "the manifest entry of $id does not describe ext-$id.raw as built"
        grep -q "^ext $id " "$EXT_CATALOG/extensions.list" || die "extensions.list does not list $id"
        [[ -s $EXT_CATALOG/descriptors/$id.json ]] || die "vos ext catalog wrote no descriptor for $id"
        # check-tree ran before these were in the rootfs; no image may hide them.
        for f in usr/lib/vos/extensions.list usr/share/vos/extensions; do
            if [[ -e $EXT_WORK/$id/mnt/$f || -L $EXT_WORK/$id/mnt/$f ]]; then
                die "extension $id ships /$f, which would hide the image's own"
            fi
        done
    done

    install -Dm0644 "$EXT_CATALOG/extensions.list" "$ROOT/usr/lib/vos/extensions.list"
    rm -rf "$ROOT/usr/share/vos/extensions"
    install -d -m0755 "$ROOT/usr/share/vos/extensions"
    for id in "${EXT_IDS[@]}"; do
        install -m0644 "$EXT_CATALOG/descriptors/$id.json" "$ROOT/usr/share/vos/extensions/$id.json"
    done
    sed -n 's/^ext /    /p' "$EXT_CATALOG/extensions.list"
}
