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
