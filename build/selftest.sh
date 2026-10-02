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
trap 'rm -rf "$tmp"' EXIT
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

# An extension's package layer: what its packages own, and what their
# scriptlets and hooks left behind, in and outside usr/.
pr=$ext/prune
mkdir -p "$pr/lower/usr/share/icons/hicolor" "$pr/lower/usr/lib/copied" "$pr/lower/etc" \
    "$pr/tree/usr/bin" "$pr/tree/usr/share/icons/hicolor/48x48/apps" "$pr/tree/usr/lib/copied" \
    "$pr/tree/usr/share/tool/empty" "$pr/tree/etc" "$pr/tree/var/log"
echo base >"$pr/lower/usr/share/icons/hicolor/icon-theme.cache"
echo tool >"$pr/tree/usr/bin/tool"
echo icon >"$pr/tree/usr/share/icons/hicolor/48x48/apps/tool.png"
echo redone >"$pr/tree/usr/share/icons/hicolor/icon-theme.cache"
echo cache >"$pr/tree/usr/lib/newcache"
echo ld >"$pr/tree/etc/ld.so.cache"
echo log >"$pr/tree/var/log/pacman.log"
echo dot >"$pr/tree/.hidden"
whiteout=0
if mknod "$pr/tree/usr/lib/gone" c 0 0 2>/dev/null; then whiteout=1; fi
printf '%s\n' /usr/ /usr/bin/ /usr/bin/tool /usr/share/icons/hicolor/48x48/apps/tool.png \
    /usr/share/tool/ /usr/share/tool/empty/ /etc/ >"$pr/owned"
printf '/etc/tool.conf\n/usr/bin/tool\n' >"$pr/payload"
expect fail "ext prune: a packaged file outside usr/ fails" ext_prune_tree "$pr/tree" "$pr/payload" "$pr/lower"
has "ext prune: ... and names it" "/etc/tool.conf"
there "ext prune: ... and changes nothing" "$pr/tree/etc/ld.so.cache"
expect pass "ext prune: scriptlet and hook leftovers" ext_prune_tree "$pr/tree" "$pr/owned" "$pr/lower"
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
if (( whiteout )); then
    gone "ext prune: ... an overlay whiteout" "$pr/tree/usr/lib/gone"
fi

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
    VOS_NFT_RULES=$rules VOS_CONFIG=$tmp/config.json bash "$firewall" --print
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
    env VOS_NFT_RULES="$rules" VOS_CONFIG="$tmp/nonexistent.json" bash "$firewall" --print
lacks "firewall: ... opens nothing optional" 'add rule inet vos optional'

echo
if (( failures )); then
    echo "$failures test(s) failed"
    exit 1
fi
echo "all tests passed"
