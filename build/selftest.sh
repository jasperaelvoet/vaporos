#!/usr/bin/env bash
#
# Tests for the build's own logic (build/lib.sh and the firewall), which a
# normal build only ever exercises on its happy path. Runs in seconds in the
# vos-builder container; --privileged because nft -c needs CAP_NET_ADMIN:
#
#   docker run --rm --privileged -v "$PWD:/src:ro" vos-builder /src/build/selftest.sh
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=build/lib.sh
. "$here/lib.sh"
firewall=$here/../rootfs/usr/lib/vos/vos-firewall
rules=$here/../rootfs/usr/lib/vos/nftables.nft

tmp=$(mktemp -d)
trap 'umount "$tmp/fs" 2>/dev/null || true; rm -rf "$tmp"' EXIT
failures=0

result() { # result OK NAME
    if (( $1 )); then
        printf 'ok    %s\n' "$2"
    else
        printf 'FAIL  %s\n' "$2"
        sed 's/^/      | /' "$tmp/out"
        failures=$((failures + 1))
    fi
}
skip() { printf 'skip  %s\n' "$1"; }

# expect pass|fail NAME CMD...: run CMD, keep its output in $tmp/out for the
# has/lacks checks that follow.
expect() {
    local want=$1 name=$2 got=pass
    shift 2
    "$@" >"$tmp/out" 2>&1 || got=fail
    result "$([[ $got == "$want" ]] && echo 1 || echo 0)" "$name (expected to $want)"
}
# has|lacks NAME STRING: the last expect's output contains / lacks STRING.
has()   { result "$(grep -qF -- "$2" "$tmp/out" && echo 1 || echo 0)" "$1"; }
lacks() { result "$(grep -qF -- "$2" "$tmp/out" && echo 0 || echo 1)" "$1"; }

# ------------------------------------------------------ check_pacstrap_log --
log() { cat >"$tmp/pacstrap.log"; }

# What pacstrap really printed for sunshine 2026.928 (trimmed).
sunshine_ok='installing which...
installing sunshine...
Running in chroot, ignoring request.
modprobe: FATAL: Module uinput not found in directory /lib/modules/7.0.14-14-pve
modprobe: FATAL: Module uhid not found in directory /lib/modules/7.0.14-14-pve
modprobe: FATAL: Module uhid not found in directory /lib/modules/7.0.14-14-pve
error: command failed to execute correctly
installing steam...'
hooks_ok=':: Running post-transaction hooks...
( 1/2) Reloading system manager configuration...
  Skipped: Running in chroot.
( 2/2) Updating icon theme caches...'

log <<<"$sunshine_ok
$hooks_ok"
expect pass "pacstrap: the sunshine modprobe failure is allowed" check_pacstrap_log "$tmp/pacstrap.log"

log <<<"installing base...
installing steam...
$hooks_ok"
expect pass "pacstrap: a clean log passes" check_pacstrap_log "$tmp/pacstrap.log"

log <<<"$sunshine_ok
installing foo...
foo: something broke
error: command failed to execute correctly
$hooks_ok"
expect fail "pacstrap: any other package's failure fails" check_pacstrap_log "$tmp/pacstrap.log"
has "pacstrap: ... and names that package" "the scriptlet of foo failed"

log <<<'installing sunshine...
setcap: failed to set capabilities
error: command failed to execute correctly'
expect fail "pacstrap: sunshine failing for another reason fails" check_pacstrap_log "$tmp/pacstrap.log"

log <<<"$sunshine_ok
:: Running post-transaction hooks...
( 1/2) Creating system user accounts...
Failed to create user
error: command failed to execute correctly"
expect fail "pacstrap: a failing hook fails" check_pacstrap_log "$tmp/pacstrap.log"
has "pacstrap: ... and names the hook" 'hook "Creating system user accounts..."'

log <<<'installing glibc...
Fatal glibc error: CPU does not support x86-64-v3'
expect fail "pacstrap: a binary the CPU cannot run fails" check_pacstrap_log "$tmp/pacstrap.log"

# -------------------------------------------------------- signing helpers --
openssl genpkey -algorithm ed25519 -out "$tmp/key.pem" 2>/dev/null
openssl pkey -in "$tmp/key.pem" -pubout -outform DER | tail -c 32 | base64 -w0 >"$tmp/key.pub"
openssl genpkey -algorithm ed25519 -out "$tmp/other.pem" 2>/dev/null
openssl pkey -in "$tmp/other.pem" -pubout -outform DER | tail -c 32 | base64 -w0 >"$tmp/other.pub"
printf '{"schema":1,"version":"20260929.123456"}\n' >"$tmp/manifest.json"
openssl pkeyutl -sign -inkey "$tmp/key.pem" -rawin -in "$tmp/manifest.json" | base64 -w0 >"$tmp/manifest.json.sig"

expect pass "pubkey: 32 bytes of base64 is a key" is_ed25519_pubkey "$tmp/key.pub"
head -c 31 /dev/urandom | base64 >"$tmp/short.pub"
expect fail "pubkey: 31 bytes is not" is_ed25519_pubkey "$tmp/short.pub"
{ cat "$tmp/key.pub"; echo; cat "$tmp/key.pub"; echo; } >"$tmp/two.pub"
expect fail "pubkey: two lines is not" is_ed25519_pubkey "$tmp/two.pub"

expect pass "signature: verifies with the signing key" \
    verify_ed25519 "$tmp/key.pub" "$tmp/manifest.json" "$tmp/manifest.json.sig"
expect fail "signature: fails with another key" \
    verify_ed25519 "$tmp/other.pub" "$tmp/manifest.json" "$tmp/manifest.json.sig"
sed 's/123456/123457/' "$tmp/manifest.json" >"$tmp/tampered.json"
expect fail "signature: fails for a changed manifest" \
    verify_ed25519 "$tmp/key.pub" "$tmp/tampered.json" "$tmp/manifest.json.sig"
echo 'bm90IGEgc2lnbmF0dXJl' >"$tmp/garbage.sig"
expect fail "signature: fails for garbage" \
    verify_ed25519 "$tmp/key.pub" "$tmp/manifest.json" "$tmp/garbage.sig"

# --------------------------------------------------------- effective_sysctl --
root=$tmp/sysctl-root
sysrq() { effective_sysctl "$root" kernel.sysrq >"$tmp/out" || echo "exit $?" >"$tmp/out"; }
sysrq_is() { result "$([[ $(<"$tmp/out") == "$2" ]] && echo 1 || echo 0)" "$1 (got '$(<"$tmp/out")')"; }

sysrq; sysrq_is "sysctl: no sysctl.d at all" ""
mkdir -p "$root/usr/lib/sysctl.d"
printf 'kernel.sysrq = 16\n' >"$root/usr/lib/sysctl.d/50-default.conf"
sysrq; sysrq_is "sysctl: no /etc/sysctl.d" 16
mkdir -p "$root/etc/sysctl.d"
rm "$root/usr/lib/sysctl.d/50-default.conf"
sysrq; sysrq_is "sysctl: nothing sets it" ""
printf 'kernel.sysrq = 16\n' >"$root/usr/lib/sysctl.d/50-default.conf"
printf '# CachyOS\nkernel.sysrq = 1\nvm.swappiness = 100\n' >"$root/usr/lib/sysctl.d/70-cachyos-settings.conf"
sysrq; sysrq_is "sysctl: the last file in name order wins" 1
printf 'kernel.sysrq = 0\n' >"$root/usr/lib/sysctl.d/99-vos.conf"
sysrq; sysrq_is "sysctl: 99-vos.conf sorts last" 0
printf 'kernel/sysrq=1\n' >"$root/usr/lib/sysctl.d/99-zz.conf"
sysrq; sysrq_is "sysctl: a later file with a / key still counts" 1
rm "$root/usr/lib/sysctl.d/99-zz.conf"
printf -- '-kernel.sysrq = 438\n' >"$root/etc/sysctl.d/99-vos.conf"
sysrq; sysrq_is "sysctl: /etc replaces the same-named /usr/lib file" 438
rm "$root/etc/sysctl.d/99-vos.conf"
ln -s /dev/null "$root/etc/sysctl.d/99-vos.conf"
sysrq; sysrq_is "sysctl: a link to /dev/null in /etc masks it" 1
rm "$root/etc/sysctl.d/99-vos.conf"
printf 'kernel.sysrq_other = 5\nkernel.sysrq = 0   \n' >"$root/usr/lib/sysctl.d/99-vos.conf"
sysrq; sysrq_is "sysctl: other keys and trailing blanks" 0
printf 'kernel.sysrq = 1' >"$root/usr/lib/sysctl.d/80-no-newline.conf"
printf 'kernel.sysrq = 0' >"$root/usr/lib/sysctl.d/99-vos.conf"
sysrq; sysrq_is "sysctl: files without a final newline" 0

# --------------------------------------------------------------- extensions --
ext=$tmp/ext
# is NAME WANT CMD...: CMD succeeds and prints exactly WANT (lines joined by
# spaces).
is() {
    local name=$1 want=$2 got
    shift 2
    if got=$("$@" 2>"$tmp/err"); then got=$(tr '\n' ' ' <<<"$got" | sed 's/ $//'); else got="failed: $(<"$tmp/err")"; fi
    printf '%s\n' "$got" >"$tmp/out"
    result "$([[ $got == "$want" ]] && echo 1 || echo 0)" "$name"
}
# there|gone NAME PATH: PATH exists / does not.
there() { result "$([[ -e $2 || -L $2 ]] && echo 1 || echo 0)" "$1"; }
gone()  { result "$([[ -e $2 || -L $2 ]] && echo 0 || echo 1)" "$1"; }
# desc DIR ID [REQUIRES...]: a descriptor with just an id and its requires.
desc() {
    local dir=$1 id=$2
    shift 2
    mkdir -p "$dir/$id"
    jq -n --arg id "$id" '{schema: 1, id: $id, requires: $ARGS.positional}' --args "$@" >"$dir/$id/extension.json"
}

is "ext uuid: version 5 of vaporos-ext-<id> in the URL namespace" \
    0a3cfb5e-021f-52ad-a66b-847fbcf11f82 ext_uuid proton
is "ext uuid: ... another for another id" 80963052-b85a-57d8-bf38-f84dfcaab930 ext_uuid coolercontrol

for p in usr/lib/vos/ext/x/file usr a/.b/c; do
    expect pass "ext path: '$p' is clean" ext_rel_ok "$p"
done
for p in "" /usr/x ../x usr/../x usr//x usr/./x usr/ . ..; do
    expect fail "ext path: '$p' is not" ext_rel_ok "$p"
done

desc "$ext/a" zeta
desc "$ext/a" alpha zeta
desc "$ext/a" mid
desc "$ext/a" top mid alpha
is "ext order: requirements first, otherwise by id" "zeta alpha mid top" ext_order "$ext/a"
is "ext closure: everything required, in that order" "zeta alpha mid" ext_closure "$ext/a" top
is "ext closure: ... one level" "zeta" ext_closure "$ext/a" alpha
is "ext closure: ... none" "" ext_closure "$ext/a" zeta
desc "$ext/dash" ab
desc "$ext/dash" a-b
is "ext order: ids in byte order, as the catalog sorts them" "a-b ab" ext_order "$ext/dash"
is "ext order: no extensions/ at all" "" ext_order "$ext/none"
desc "$ext/cycle" a b
desc "$ext/cycle" b c
desc "$ext/cycle" c a
expect fail "ext order: a requires cycle fails" ext_order "$ext/cycle"
has "ext order: ... and says so" "requires cycle"
desc "$ext/missing" a nope
expect fail "ext order: a missing requirement fails" ext_order "$ext/missing"
has "ext order: ... and names it" '"nope"'
desc "$ext/misnamed" a
jq '.id = "b"' "$ext/misnamed/a/extension.json" >"$tmp/x.json" && mv "$tmp/x.json" "$ext/misnamed/a/extension.json"
expect fail "ext order: an id that is not its directory's name fails" ext_order "$ext/misnamed"
mkdir -p "$ext/empty/a"
expect fail "ext order: a directory without extension.json fails" ext_order "$ext/empty"

files=$ext/files
mkdir -p "$files/usr/share/x"
echo one >"$files/usr/share/x/f"
h=$(ext_tree_hash "$files")
treehash() { # treehash NAME same|differs
    local now
    now=$(ext_tree_hash "$files")
    result "$([[ ($2 == same && $now == "$h") || ($2 == differs && $now != "$h") ]] && echo 1 || echo 0)" "$1"
    h=$now
}
treehash "ext tree hash: stable" same
echo two >"$files/usr/share/x/f"
treehash "ext tree hash: content" differs
chmod 0755 "$files/usr/share/x/f"
treehash "ext tree hash: mode" differs
mv "$files/usr/share/x/f" "$files/usr/share/x/g"
treehash "ext tree hash: name" differs
ln -s g "$files/usr/share/x/link"
treehash "ext tree hash: a new symlink" differs
ln -sfn f "$files/usr/share/x/link"
treehash "ext tree hash: its target" differs
mkdir -p "$ext/emptydir"
result "$([[ $(ext_tree_hash "$ext/emptydir") == "$(ext_tree_hash "$ext/nonexistent")" ]] && echo 1 || echo 0)" \
    "ext tree hash: no files/ is an empty files/"

d=$ext/a/zeta/extension.json
printf 'cachyos foo 1-1 foo-1-1-x86_64.pkg.tar.zst %064d\n' 0 >"$ext/pkgs"
echo 'script v1' >"$ext/script"
k=$(ext_input_key "$d" "$files" "$ext/pkgs" "-zzstd -T0" "$ext/script")
result "$([[ $k =~ ^[0-9a-f]{64}$ ]] && echo 1 || echo 0)" "ext key: 64 hex digits ($k)"
key() { # key NAME same|differs [ARGS to ext_input_key, else the defaults]
    local name=$1 want=$2 now
    shift 2
    (( $# )) || set -- "$d" "$files" "$ext/pkgs" "-zzstd -T0" "$ext/script"
    now=$(ext_input_key "$@") || now=failed
    result "$([[ ($want == same && $now == "$k") || ($want == differs && $now != "$k" && $now != failed) ]] && echo 1 || echo 0)" "$name"
}
key "ext key: the same inputs, the same key" same
key "ext key: the mkfs flags" differs "$d" "$files" "$ext/pkgs" "-zzstd,level=9 -T0" "$ext/script"
key "ext key: another script" differs "$d" "$files" "$ext/pkgs" "-zzstd -T0" "$ext/pkgs"
cp "$d" "$tmp/desc.bak"
jq '.name = "Zeta"' "$tmp/desc.bak" >"$d"
key "ext key: the descriptor" differs
cp "$tmp/desc.bak" "$d"
printf 'cachyos foo 1-2 foo-1-2-x86_64.pkg.tar.zst %064d\n' 0 >"$ext/pkgs"
key "ext key: the resolved packages" differs
printf 'cachyos foo 1-1 foo-1-1-x86_64.pkg.tar.zst %064d\n' 0 >"$ext/pkgs"
echo three >"$files/usr/share/x/g"
key "ext key: files/" differs
echo two >"$files/usr/share/x/g"
echo 'script v2' >"$ext/script"
key "ext key: a script's content" differs
echo 'script v1' >"$ext/script"
key "ext key: ... and back" same
expect fail "ext key: a missing script fails" ext_input_key "$d" "$files" "$ext/pkgs" "" "$ext/nonexistent"

# The vectors of internal/extensions/fsverity, from fsverity-utils itself.
if command -v fsverity >/dev/null; then
    printf a >"$ext/a.bin"
    (set +o pipefail; yes abcdefg | head -c 4097) >"$ext/4097.bin"
    is "fsverity: one byte" bce75948b9e7510293f8f2720412af9697c1479281323f3f220623fb8e94b557 fsverity_hex "$ext/a.bin"
    is "fsverity: a block and a byte" a590eea2232d95d6603f3ec229eb650ead55860e31c05cb3e19251e8a30fe156 \
        fsverity_hex "$ext/4097.bin"
else
    echo "fsverity-utils is not installed; build/Dockerfile installs it" >"$tmp/out"
    result 0 "fsverity: the digest of fsverity-utils"
fi

# What an extension's install did to the packages that were there.
pk=$ext/pkgs-q
mkdir -p "$pk"
printf 'glibc 2.42-1\nlib32-foo 1-1\nmesa 1:25.2-1\n' >"$pk/before"
printf 'glibc 2.42-1\nlib32-foo 1-1\nmesa 1:25.2-1\nproton 10-1\n' >"$pk/after"
printf '%s\n' '[2026-10-02T10:00:00+0000] [PACMAN] Running '\''pacman -S -- proton'\''' \
    '[2026-10-02T10:00:01+0000] [ALPM] transaction started' \
    '[2026-10-02T10:00:02+0000] [ALPM] installed proton (10-1)' \
    '[2026-10-02T10:00:03+0000] [ALPM] transaction completed' >"$pk/alpm.log"
expect pass "ext packages: an install that only adds" ext_pkg_changes "$pk/before" "$pk/after" "$pk/alpm.log"
printf 'glibc 2.42-1\nlib32-foo 1-1\nmesa 1:25.3-1\nproton 10-1\n' >"$pk/upgraded"
expect fail "ext packages: an upgrade fails" ext_pkg_changes "$pk/before" "$pk/upgraded"
has "ext packages: ... and says from what to what" "mesa: 1:25.2-1 -> 1:25.3-1"
printf 'glibc 2.42-0\nlib32-foo 1-1\nmesa 1:25.2-1\n' >"$pk/downgraded"
expect fail "ext packages: a downgrade fails" ext_pkg_changes "$pk/before" "$pk/downgraded"
printf 'glibc 2.42-1\nmesa 1:25.2-1\nproton 10-1\n' >"$pk/removed"
expect fail "ext packages: a removed (or replaced) package fails" ext_pkg_changes "$pk/before" "$pk/removed"
has "ext packages: ... and names it" "lib32-foo 1-1: removed"
cp "$pk/alpm.log" "$pk/reinstalled.log"
echo '[2026-10-02T10:00:02+0000] [ALPM] reinstalled glibc (2.42-1)' >>"$pk/reinstalled.log"
expect fail "ext packages: a reinstall, which keeps the version, fails" \
    ext_pkg_changes "$pk/before" "$pk/after" "$pk/reinstalled.log"
has "ext packages: ... from pacman's log" "reinstalled glibc (2.42-1)"
is "ext db: entries of packages" "foo-1:2.0-1 lib32-bar-3-2" ext_local_entries <(printf 'foo 1:2.0-1\nlib32-bar 3-2\n')

db=$ext/db/local
mkdir -p "$db/foo-1.0-1" "$db/foo-bar-2-1" "$db/lib32-foo-1.0-1" "$db/mesa-1:25.2-1"
echo 9 >"$db/ALPM_DB_VERSION"
is "ext db: one entry a package" "" ext_local_dupes "$db"
mkdir -p "$db/foo-1.1-1" "$db/mesa-1:25.3-1"
is "ext db: two versions of a package" "foo mesa" ext_local_dupes "$db"

# A cached image, and whether this build may use it in place of a new one.
ca=$ext/cache
mkdir -p "$ca/db/foo-1.0-1" "$ca/db/lib32-foo-2-1" "$ca/db/bar-3-1"
for e in foo-1.0-1 lib32-foo-2-1 bar-3-1; do echo "%NAME%" >"$ca/db/$e/desc"; done
c=$ca/key
echo image >"$c.raw"
sha256sum <"$c.raw" | cut -d' ' -f1 >"$c.sha256"
echo '{}' >"$c.build.json"
printf 'foo 1.0-1\nlib32-foo 2-1\n' >"$c.packages.txt"
tar -C "$ca/db" -cf "$c.local.tar" foo-1.0-1 lib32-foo-2-1
cp "$c.local.tar" "$ca/good.tar"
printf 'glibc\nmesa\nproton\n' >"$ca/have"
expect pass "ext cache: a complete entry" ext_cache_check "$c" "$ca/have"
for f in raw sha256 build.json packages.txt local.tar; do
    mv "$c.$f" "$ca/aside"
    expect fail "ext cache: an entry without .$f fails" ext_cache_check "$c" "$ca/have"
    has "ext cache: ... and says so" "it has no .$f"
    mv "$ca/aside" "$c.$f"
done
expect fail "ext cache: no list of the database's packages fails" ext_cache_check "$c" "$ca/nonexistent"
has "ext cache: ... and says so" "there is no list of the packages its database has"
echo other >"$c.raw"
expect fail "ext cache: an image its sha256 does not name fails" ext_cache_check "$c" "$ca/have"
has "ext cache: ... and says so" "not the one its sha256 names"
echo image >"$c.raw"
tar -C "$ca/db" -cf "$c.local.tar" foo-1.0-1 lib32-foo-2-1 bar-3-1
expect fail "ext cache: a database entry of another package fails" ext_cache_check "$c" "$ca/have"
has "ext cache: ... and says so" "not those of its packages"
tar -C "$ca/db" -cf "$c.local.tar" foo-1.0-1
expect fail "ext cache: a package without its database entry fails" ext_cache_check "$c" "$ca/have"
echo garbage >"$c.local.tar"
expect fail "ext cache: a database archive tar cannot read fails" ext_cache_check "$c" "$ca/have"
cp "$ca/good.tar" "$c.local.tar"
printf 'glibc\nlib32-foo\nmesa\n' >"$ca/have-lib32"
expect fail "ext cache: a package the database has already fails" ext_cache_check "$c" "$ca/have-lib32"
has "ext cache: ... and names it" "has already: lib32-foo"
expect pass "ext cache: ... and the complete entry again" ext_cache_check "$c" "$ca/have"
: >"$c.packages.txt"
tar -C "$ca/db" -cf "$c.local.tar" -T /dev/null
expect pass "ext cache: an image without packages" ext_cache_check "$c" "$ca/have"
# With no packages there are no entries to compare, and tar lists nothing
# from an archive it cannot read: only its exit status tells, so
# ext_cache_check sets pipefail for it whatever its caller has.
without_pipefail() { (set +o pipefail; "$@"); }
echo garbage >"$c.local.tar"
expect fail "ext cache: an archive tar cannot read, of an image without packages, fails" \
    without_pipefail ext_cache_check "$c" "$ca/have"
has "ext cache: ... and says so" "not those of its packages"

# status NAME WANT CMD...: CMD exits with WANT; its output is in $tmp/out.
status() {
    local name=$1 want=$2 got=0
    shift 2
    "$@" >"$tmp/out" 2>&1 || got=$?
    result "$([[ $got == "$want" ]] && echo 1 || echo 0)" "$name (exit $got, want $want)"
}

# Whiteouts and trusted.* xattrs need a filesystem of their own: the
# container's root is an overlay, which hides both.
fs=$tmp/fs
mkdir -p "$fs"
mount -t tmpfs tmpfs "$fs" 2>/dev/null || true
up=$fs/upper
mkdir -p "$up/usr/share/kept" "$up/usr/share/redone" "$up/usr/lib" "$up/usr/bin" "$up/etc"
echo new >"$up/usr/share/kept/file"
# Links in an upper layer mostly point into the layers below it, or at
# absolute paths of the image: on the builder they dangle.
ln -s libfoo.so.1 "$up/usr/lib/libfoo.so"
ln -s /usr/lib/vos-selftest/nonexistent "$up/usr/bin/tool"
status "ext removals: an upper layer that only adds, dangling links included" 0 ext_upper_removals "$up"
if mknod "$up/usr/share/gone" c 0 0 2>/dev/null && [[ -c $up/usr/share/gone ]] &&
        mknod "$up/etc/gone" c 0 0; then
    status "ext removals: a whiteout under usr/ fails" 1 ext_upper_removals "$up"
    has "ext removals: ... and names it" "removed /usr/share/gone (a whiteout)"
    lacks "ext removals: ... (not one outside usr/)" "/etc/gone"
    rm "$up/usr/share/gone"
else
    skip "ext removals: whiteouts (mknod)"
fi
mkdir -p "$fs/opaque"
if setfattr -n trusted.overlay.opaque -v y "$fs/opaque" 2>/dev/null; then
    ln -s "$fs/opaque" "$up/usr/share/elsewhere"
    status "ext removals: a link to an opaque directory is no removal" 0 ext_upper_removals "$up"
    setfattr -n trusted.overlay.opaque -v y "$up/usr/share/redone"
    status "ext removals: an opaque directory under usr/ fails" 1 ext_upper_removals "$up"
    has "ext removals: ... and names it" "replaced /usr/share/redone (an opaque directory"
    lacks "ext removals: ... (not the others)" "/usr/share/kept"
    lacks "ext removals: ... (nor the link)" "/usr/share/elsewhere"
else
    skip "ext removals: opaque directories (trusted.* xattrs)"
fi
getfattr() { echo "getfattr: cannot do that" >&2; return 1; }
status "ext removals: xattrs it cannot read fail apart" 2 ext_upper_removals "$up"
unset -f getfattr
has "ext removals: ... and say so" "cannot read the xattrs under"
lacks "ext removals: ... (no removal named)" "replaced /"

# An extension's package layer: what its packages own, and what their
# scriptlets and hooks left behind, in and outside usr/.
pr=$fs/prune
mkdir -p "$pr/lower/usr/share/icons/hicolor" "$pr/lower/usr/lib/copied" "$pr/lower/etc" \
    "$pr/lower/usr/share/glib-2.0/schemas" "$pr/lower/usr/share/mime" \
    "$pr/tree/usr/bin" "$pr/tree/usr/share/icons/hicolor/48x48/apps" "$pr/tree/usr/lib/copied" \
    "$pr/tree/usr/share/glib-2.0/schemas" "$pr/tree/usr/share/mime" \
    "$pr/tree/usr/share/tool/empty" "$pr/tree/etc" "$pr/tree/var/log"
for f in usr/share/icons/hicolor/icon-theme.cache usr/share/glib-2.0/schemas/gschemas.compiled \
         usr/share/mime/mime.cache; do
    echo base >"$pr/lower/$f"
    echo redone >"$pr/tree/$f"
done
echo tool >"$pr/tree/usr/bin/tool"
echo icon >"$pr/tree/usr/share/icons/hicolor/48x48/apps/tool.png"
echo schema >"$pr/tree/usr/share/glib-2.0/schemas/org.tool.gschema.xml"
echo cache >"$pr/tree/usr/lib/newcache"
echo ld >"$pr/tree/etc/ld.so.cache"
echo log >"$pr/tree/var/log/pacman.log"
echo dot >"$pr/tree/.hidden"
printf '%s\n' /usr/ /usr/bin/ /usr/bin/tool /usr/share/icons/hicolor/48x48/apps/tool.png \
    /usr/share/glib-2.0/schemas/org.tool.gschema.xml /usr/share/tool/ /usr/share/tool/empty/ /etc/ >"$pr/owned"
printf '/etc/tool.conf\n/usr/bin/tool\n' >"$pr/payload"
expect fail "ext prune: a packaged file outside usr/ fails" ext_prune_tree "$pr/tree" "$pr/payload" "$pr/lower"
has "ext prune: ... and names it" "/etc/tool.conf"
there "ext prune: ... and changes nothing" "$pr/tree/etc/ld.so.cache"
if mknod "$pr/tree/usr/lib/gone" c 0 0 2>/dev/null && [[ -c $pr/tree/usr/lib/gone ]]; then
    expect fail "ext prune: a whiteout in usr/ fails" ext_prune_tree "$pr/tree" "$pr/owned" "$pr/lower"
    has "ext prune: ... and names it" "/usr/lib/gone"
    there "ext prune: ... and changes nothing" "$pr/tree/etc/ld.so.cache"
    there "ext prune: ... (the whiteout stays too)" "$pr/tree/usr/lib/gone"
    rm "$pr/tree/usr/lib/gone"
else
    skip "ext prune: whiteouts (mknod)"
fi
expect pass "ext prune: scriptlet and hook leftovers" ext_prune_tree "$pr/tree" "$pr/owned" "$pr/lower"
cp "$tmp/out" "$pr/pruned"
gone "ext prune: ... nothing outside usr/ (etc)" "$pr/tree/etc"
gone "ext prune: ... (var)" "$pr/tree/var"
gone "ext prune: ... (a dotfile)" "$pr/tree/.hidden"
has "ext prune: ... each removal listed" "removed etc/ld.so.cache"
gone "ext prune: ... a lower layer's file it does not own" "$pr/tree/usr/share/icons/hicolor/icon-theme.cache"
has "ext prune: ... listed" "removed usr/share/icons/hicolor/icon-theme.cache"
gone "ext prune: ... an empty directory a lower layer has" "$pr/tree/usr/lib/copied"
there "ext prune: ... keeps what it owns" "$pr/tree/usr/bin/tool"
there "ext prune: ... (in a directory the base has)" "$pr/tree/usr/share/icons/hicolor/48x48/apps/tool.png"
there "ext prune: ... an empty directory of its own" "$pr/tree/usr/share/tool/empty"
there "ext prune: ... a new file it does not own (check-tree judges it)" "$pr/tree/usr/lib/newcache"
stale() { ext_stale_caches "$@" | LC_ALL=C sort; }
is "ext stale caches: those whose inputs the extension ships (mime.cache: none)" \
    "usr/share/glib-2.0/schemas/gschemas.compiled usr/share/glib-2.0/schemas usr/share/icons/hicolor/icon-theme.cache usr/share/icons/hicolor" \
    stale "$pr/tree" "$pr/pruned"

# A fetch[] download into the image, and its licence text.
fe=$ext/fetch
mkdir -p "$fe/src/tool-1.0/bin" "$fe/src/other"
echo binary >"$fe/src/tool-1.0/bin/tool"
echo 'MIT License' >"$fe/src/tool-1.0/LICENSE"
echo 'Apache License' >"$fe/src/other/LICENSE"
: >"$fe/src/tool-1.0/empty"
tar -C "$fe/src" -czf "$fe/tool.tar.gz" tool-1.0 other
echo plain >"$fe/plain.exe"
own=$fe/img/usr/lib/vos/ext/x
lics=$fe/img/usr/share/licenses/x
expect pass "ext fetch: a member of an archive" ext_place_fetch "$fe/tool.tar.gz" tool-1.0/bin/tool "$own/bin/tool"
result "$([[ $(<"$own/bin/tool") == binary && $(stat -c %a "$own/bin/tool") == 644 ]] && echo 1 || echo 0)" \
    "ext fetch: ... in place, mode 0644"
expect pass "ext fetch: a file as it is" ext_place_fetch "$fe/plain.exe" "" "$own/plain.exe"
result "$([[ $(<"$own/plain.exe") == plain ]] && echo 1 || echo 0)" "ext fetch: ... in place"
expect fail "ext fetch: a member the archive lacks fails" ext_place_fetch "$fe/tool.tar.gz" tool-1.0/nope "$own/nope"
has "ext fetch: ... and names it" "no member tool-1.0/nope"
expect fail "ext fetch: an empty member fails" ext_place_fetch "$fe/tool.tar.gz" tool-1.0/empty "$own/empty"
expect pass "ext fetch: with its licence text" \
    ext_place_fetch "$fe/tool.tar.gz" tool-1.0/bin/tool "$own/bin/tool" tool-1.0/LICENSE "$lics"
result "$([[ $(<"$lics/LICENSE") == 'MIT License' ]] && echo 1 || echo 0)" "ext fetch: ... under its base name"
expect pass "ext fetch: the same licence text again" \
    ext_place_fetch "$fe/tool.tar.gz" tool-1.0/bin/tool "$own/bin/tool2" tool-1.0/LICENSE "$lics"
expect fail "ext fetch: another licence text of that name fails" \
    ext_place_fetch "$fe/tool.tar.gz" tool-1.0/bin/tool "$own/bin/tool3" other/LICENSE "$lics"
result "$([[ $(<"$lics/LICENSE") == 'MIT License' ]] && echo 1 || echo 0)" "ext fetch: ... and keeps the first"
expect fail "ext fetch: a licence file the archive lacks fails" \
    ext_place_fetch "$fe/tool.tar.gz" tool-1.0/bin/tool "$own/bin/tool" tool-1.0/COPYING "$lics"
has "ext fetch: ... and names it" "no licence file tool-1.0/COPYING"
expect fail "ext fetch: a licence file needs an archive" \
    ext_place_fetch "$fe/plain.exe" "" "$own/plain.exe" LICENSE "$lics"
expect fail "ext fetch: a licence file named fetched.txt fails" \
    ext_place_fetch "$fe/tool.tar.gz" tool-1.0/bin/tool "$own/bin/tool" tool-1.0/fetched.txt "$lics"

# ------------------------------------------------------------------- iso.sh --
iso=$tmp/iso
mkdir -p "$iso/in"
for f in root.erofs:1024 vmlinuz:256 initramfs.img:512 systemd-bootx64.efi:64; do
    head -c "$(( ${f#*:} * 1024 ))" /dev/urandom >"$iso/in/${f%%:*}"
done
printf '{"version":"20260929.123456","cmdline":"quiet loglevel=3 console=ttyS0,115200"}\n' >"$iso/in/manifest.json"
# on_iso ISO: the files under /vos on ISO, one per line, in $tmp/out.
on_iso() {
    rm -rf "$iso/x"
    xorriso -osirrox on -indev "$1" -extract /vos "$iso/x" >/dev/null 2>&1 &&
        ls "$iso/x" >"$tmp/out"
}
# (The container has no cmp.)
same() { [[ $(sha256sum <"$1") == "$(sha256sum <"$2")" ]]; }

expect pass "iso: an unsigned ISO" bash "$here/iso.sh" "$iso/in" "$iso/unsigned.iso"
expect pass "iso: ... has /vos" on_iso "$iso/unsigned.iso"
result "$([[ $(tr '\n' ' ' <"$tmp/out") == 'initramfs.img manifest.json root.erofs vmlinuz ' ]] && echo 1 || echo 0)" \
    "iso: ... with exactly the OS files, no signature and no boot loader"
result "$(same "$iso/in/root.erofs" "$iso/x/root.erofs" && echo 1 || echo 0)" "iso: ... and root.erofs intact"

printf 'c2lnbmF0dXJl\n' >"$iso/in/manifest.json.sig"
expect pass "iso: a signed ISO" bash "$here/iso.sh" "$iso/in" "$iso/signed.iso"
expect pass "iso: ... has /vos" on_iso "$iso/signed.iso"
has "iso: ... with manifest.json.sig" manifest.json.sig
result "$(same "$iso/in/manifest.json.sig" "$iso/x/manifest.json.sig" && echo 1 || echo 0)" "iso: ... the very signature"

cp "$iso/in/manifest.json" "$iso/manifest.good"
printf '{"version":"20260929.123456","cmdline":"quiet console=tty0 console=ttyS0"}\n' >"$iso/in/manifest.json"
expect fail "iso: a console on tty0 is refused" bash "$here/iso.sh" "$iso/in" "$iso/tty0.iso"
printf '{"version":"20260929.123456"}\n' >"$iso/in/manifest.json"
expect fail "iso: a manifest without cmdline is refused" bash "$here/iso.sh" "$iso/in" "$iso/nocmdline.iso"
cp "$iso/manifest.good" "$iso/in/manifest.json"
rm "$iso/in/systemd-bootx64.efi"
expect fail "iso: no boot loader, no ISO" bash "$here/iso.sh" "$iso/in" "$iso/noefi.iso"
has "iso: ... and it says what is missing" systemd-bootx64.efi

# ---------------------------------------------------------------- firewall --
render() { # render CONFIG_JSON -> the ruleset vos-firewall would load
    printf '%s\n' "$1" >"$tmp/config.json"
    VOS_NFT_RULES=$rules VOS_CONFIG=$tmp/config.json VOS_EXT_PORTS=$tmp/no-ports bash "$firewall" --print
}
# render_ports CONFIG_JSON PORTS: the same with /var/lib/vos/ext/ports
# holding PORTS (printf %b: \n, \r and \t as escapes).
render_ports() {
    printf '%s\n' "$1" >"$tmp/config.json"
    printf '%b' "$2" >"$tmp/ports"
    VOS_NFT_RULES=$rules VOS_CONFIG=$tmp/config.json VOS_EXT_PORTS=$tmp/ports bash "$firewall" --print
}

expect pass "firewall: loads (nft -c) closed, open and with a broken config" check_firewall "$firewall" "$rules"

expect pass "firewall: default config" render '{}'
lacks "firewall: ... opens nothing optional" 'add rule inet vos optional'

# Sunshine's admin UI/API (47990) stays closed whatever config.json says.
# (Only rule lines count: the ruleset's comments mention the port.)
for cfg in '{}' '{"ssh":{"enabled":true},"web":{"https":true,"allow_public":true}}'; do
    render "$cfg" | grep -v '^[[:space:]]*#' >"$tmp/out"
    lacks "firewall: 47990 is never opened ($cfg)" '47990'
done

expect pass "firewall: ssh enabled" render '{"ssh":{"enabled":true}}'
has "firewall: ... opens 22 to the LAN (v4)" 'add rule inet vos optional ip saddr @lan4 tcp dport 22 accept'
has "firewall: ... opens 22 to the LAN (v6)" 'add rule inet vos optional ip6 saddr @lan6 tcp dport 22 accept'
lacks "firewall: ... and nothing else" 'dport 443'

expect pass "firewall: https" render '{"web":{"https":true}}'
has "firewall: ... opens 443 to the LAN" 'ip saddr @lan4 tcp dport 443 accept'
lacks "firewall: ... but not 22" 'dport 22'

expect pass "firewall: public web + ssh" render '{"ssh":{"enabled":true},"web":{"allow_public":true}}'
has "firewall: ... opens 80 to everyone" 'add rule inet vos optional tcp dport 80 accept'
has "firewall: ... opens 22 to everyone" 'add rule inet vos optional tcp dport 22 accept'

expect pass "firewall: a string \"true\" is not true" render '{"ssh":{"enabled":"true"}}'
lacks "firewall: ... so 22 stays closed" 'dport 22'

expect pass "firewall: a missing config.json" \
    env VOS_NFT_RULES="$rules" VOS_CONFIG="$tmp/nonexistent.json" VOS_EXT_PORTS="$tmp/no-ports" bash "$firewall" --print
lacks "firewall: ... opens nothing optional" 'add rule inet vos optional'

# Extensions' ports (vosd's /var/lib/vos/ext/ports): the LAN sets only,
# whatever web.allow_public says, and all or nothing.
expect pass "firewall: extension ports with web.allow_public" \
    render_ports '{"web":{"allow_public":true}}' 'tcp 11987\nudp 27015\n'
has "firewall: ... opens 11987 to the LAN (v4)" 'add rule inet vos optional ip saddr @lan4 tcp dport 11987 accept'
has "firewall: ... opens 11987 to the LAN (v6)" 'add rule inet vos optional ip6 saddr @lan6 tcp dport 11987 accept'
has "firewall: ... and udp 27015" 'add rule inet vos optional ip saddr @lan4 udp dport 27015 accept'
lacks "firewall: ... never to everyone" 'add rule inet vos optional tcp dport 11987'
has "firewall: ... while 80 is public" 'add rule inet vos optional tcp dport 80 accept'

expect pass "firewall: a last line without a newline" render_ports '{}' 'tcp 11987'
has "firewall: ... still counts" 'ip saddr @lan4 tcp dport 11987 accept'
lacks "firewall: ... a port without an upstream keeps loopback as it is" 'add rule inet vos upstream'

# A proxied port's loopback upstream: only root (vosd) may connect to it.
expect pass "firewall: a proxied port with its upstream" \
    render_ports '{"web":{"allow_public":true}}' 'tcp 11987 upstream 11985\nudp 27015\n'
has "firewall: ... opens 11987 to the LAN" 'add rule inet vos optional ip saddr @lan4 tcp dport 11987 accept'
has "firewall: ... resets anyone but root on 11985" \
    'add rule inet vos upstream tcp dport 11985 meta skuid != 0 reject with tcp reset'
lacks "firewall: ... never opens the upstream" 'dport 11985 accept'
has "firewall: ... from loopback's output" 'oif lo jump upstream'

# Steam's debugger (31911, vos-gamescope.service): the upstream chain's own
# rule in nftables.nft, so it holds whatever config.json and the ports file
# say, and never comes from vos-firewall.
upstream_chain() { # upstream_chain CONFIG_JSON PORTS: chain upstream's body as written
    # awk reads to the end: exiting early would SIGPIPE vos-firewall.
    render_ports "$1" "$2" | awk '/^[[:space:]]*chain upstream \{/ { on = 1; next }
        on && /^[[:space:]]*\}/ { on = 0 } on'
}
for c in 'the default config={}=' \
            'everything open={"ssh":{"enabled":true},"web":{"https":true,"allow_public":true}}=tcp 11987 upstream 11985\nudp 27015\n' \
            'a ports file that opens nothing={}=tcp 11987\ntcp 22\n'; do
    IFS='=' read -r name cfg ports <<<"$c"
    expect pass "firewall: Steam's debugger with $name" upstream_chain "$cfg" "$ports"
    has "firewall: ... resets anyone but root on 31911" \
        'tcp dport 31911 meta skuid != 0 reject with tcp reset comment "steam devtools"'
done
expect pass "firewall: the whole ruleset, everything open" \
    render_ports '{"ssh":{"enabled":true},"web":{"https":true,"allow_public":true}}' 'tcp 11987 upstream 11985\n'
lacks "firewall: ... never accepts 31911" 'dport 31911 accept'
lacks "firewall: ... and adds no rule of its own for it" 'add rule inet vos upstream tcp dport 31911'

for bad in 'a line that is not a port=tcp 11987 upstream 11985\nhttp 8080\n' \
           'Sunshine'"'"'s admin port=tcp 11987 upstream 11985\ntcp 47990\n' \
           'a port below 1024=tcp 11987 upstream 11985\ntcp 22\n' \
           'a port of 1023=tcp 11987 upstream 11985\ntcp 1023\n' \
           'a port above 65535=tcp 65536\n' \
           'a leading zero=tcp 011987\n' \
           'a trailing space=tcp 11987 \n' \
           'a CRLF line=tcp 11987\r\n' \
           'an empty line=tcp 11987\n\n' \
           'an upstream on udp=tcp 11987 upstream 11985\nudp 27015 upstream 27016\n' \
           'Sunshine'"'"'s admin port as an upstream=tcp 11987 upstream 47990\n' \
           'Steam'"'"'s debugger port=tcp 11987 upstream 11985\ntcp 31911\n' \
           'Steam'"'"'s debugger port as an upstream=tcp 11987 upstream 31911\n' \
           'an upstream below 1024=tcp 11987 upstream 80\n' \
           'an upstream above 65535=tcp 11987 upstream 65536\n' \
           'an upstream with a leading zero=tcp 11987 upstream 011985\n' \
           'an upstream without a port=tcp 11987 upstream\n' \
           'two spaces before an upstream=tcp 11987  upstream 11985\n' \
           'a trailing space after an upstream=tcp 11987 upstream 11985 \n'; do
    expect pass "firewall: a ports file with ${bad%%=*}" render_ports '{"web":{"allow_public":true}}' "${bad#*=}"
    lacks "firewall: ... opens none of it" 'add rule inet vos optional ip saddr'
    lacks "firewall: ... and adds no upstream rule" 'add rule inet vos upstream'
done
many=$(for ((p = 20000; p < 20065; p++)); do printf 'tcp %d\\n' "$p"; done)
expect pass "firewall: a ports file of 65 ports" render_ports '{}' "$many"
lacks "firewall: ... opens none of them" 'dport 20000'

echo
if (( failures )); then
    echo "$failures test(s) failed"
    exit 1
fi
echo "all tests passed"
