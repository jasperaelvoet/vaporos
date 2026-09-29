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
