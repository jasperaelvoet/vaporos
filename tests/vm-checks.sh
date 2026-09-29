#!/usr/bin/env bash
#
# Checks that run *inside* the dev VM during `make test`. Dev images are debug
# builds with a root shell on the serial console, so scripts/dev.sh serves this
# file from the Proxmox host and types, on that console:
#
#   curl -fsS -o /tmp/vm-checks.sh http://PVE:8000/vm-checks.sh && bash /tmp/vm-checks.sh GROUP ...
#
#   system   --slot S --version V --password P   the whole installed system
#   booted   --slot S --version V [--blessed]    after an update or a rollback
#   fallback --slot S --version V --failed F     after F failed its health check
#
# Every check prints one line, "VOS-CHECK <ok|warn|FAIL> <name> <detail>", and
# the run ends with "VOS-CHECKS-DONE pass=N warn=N fail=N". dev.sh reads those
# lines back from the serial log; the exit status is 0 only if nothing failed.
# It runs as root on the VM and uses only what the image already has (bash,
# coreutils, util-linux, procps, curl, systemd, getcap, nft): no jq, no python.
set -uo pipefail # no -e: one failed check must not stop the others

# Overridable only so the checks themselves can be tested off the VM.
API=${VOS_API:-http://127.0.0.1/api/v1}
ESP=${VOS_ESP:-/efi}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
pass=0 warns=0 fails=0

ok()   { echo "VOS-CHECK ok $1 ${*:2}";   pass=$((pass + 1)); }
warn() { echo "VOS-CHECK warn $1 ${*:2}"; warns=$((warns + 1)); }
bad()  { echo "VOS-CHECK FAIL $1 ${*:2}"; fails=$((fails + 1)); }

# The image has no jq. vosd writes compact JSON, but tolerate spaces anyway.
# json_str KEY: the first string value of KEY on stdin.
json_str() { grep -o "\"$1\" *: *\"[^\"]*\"" | head -n1 | sed 's/.*: *"//; s/"$//'; }
# json_lit KEY: the first number/true/false/null value of KEY on stdin.
json_lit() { grep -o "\"$1\" *: *[-a-z0-9.]*" | head -n1 | sed 's/.*: *//'; }
# A JSON string literal for $1.
json_quote() {
    local s=${1//\\/\\\\}
    s=${s//\"/\\\"}
    printf '"%s"' "$s"
}

# http METHOD PATH [curl args...]: the response body lands in $tmp/body and
# the status code is printed ("000" when vosd did not answer at all).
http() {
    local method=$1 path=$2
    shift 2
    : >"$tmp/body"
    curl -sS -m 15 -X "$method" -o "$tmp/body" -w '%{http_code}' "$@" "$API$path" 2>/dev/null ||
        true
}

# ---------------------------------------------------------------- checks ----

check_booted() {
    local want_slot=$1 want_version=$2 have out
    have=$(grep -o 'vos\.slot=[ab]' /proc/cmdline | cut -d= -f2)
    if [[ $have == "$want_slot" ]]; then
        ok slot "booted slot $have"
    else
        bad slot "booted slot '${have:-none}', expected $want_slot"
    fi
    have=$(json_str version </usr/lib/vos/image.json 2>/dev/null)
    if [[ $have == "$want_version" ]]; then
        ok version "image $have"
    else
        bad version "image '${have:-unknown}', expected $want_version"
    fi
    if out=$(vos status 2>&1) && [[ $out == *"$want_version"* ]]; then
        ok vos-status "vos status reports $want_version"
    else
        bad vos-status "vos status failed or does not mention $want_version: $(tr '\n' ' ' <<<"$out" | cut -c1-200)"
    fi
}

check_layout() {
    local fs data src
    fs=$(findmnt -no FSTYPE /)
    if [[ $fs == erofs ]]; then ok root "/ is the read-only erofs image"; else bad root "/ is '$fs', expected erofs"; fi

    if touch /usr/.vos-write-test 2>/dev/null; then
        rm -f /usr/.vos-write-test
        bad usr-ro "/usr accepted a write"
    else
        ok usr-ro "/usr refuses writes"
    fi

    fs=$(findmnt -no FSTYPE /etc)
    if [[ $fs == overlay ]]; then ok etc "/etc is an overlay"; else bad etc "/etc is '$fs', expected overlay"; fi

    # findmnt shows a bind mount's source as DEVICE[/subdir].
    data=$(readlink -f /dev/disk/by-partlabel/vos_data)
    src=$(findmnt -no SOURCE /var | head -n1)
    if [[ -n $data && ${src%%\[*} == "$data" ]]; then
        ok var "/var lives on vos_data ($data)"
    else
        bad var "/var comes from '$src', expected $data (vos_data)"
    fi
    if findmnt -no OPTIONS /state | grep -qw rw; then ok state "/state is mounted rw"; else bad state "/state is not mounted rw"; fi
}

check_units() {
    local state failed
    state=$(timeout 180 systemctl is-system-running --wait 2>/dev/null)
    failed=$(systemctl --failed --no-legend --plain 2>/dev/null | awk '{print $1}' | tr '\n' ' ')
    case $state in
        running) ok units "system is running, no failed units" ;;
        degraded) warn units "system is degraded; failed: ${failed:-?}" ;;
        *) warn units "system state '${state:-unknown}'; failed: ${failed:-none}" ;;
    esac
}

check_cachyos() {
    local k svc zram
    k=$(uname -r)
    if [[ $k == *cachyos* ]]; then ok kernel "CachyOS kernel $k"; else bad kernel "kernel $k is not linux-cachyos"; fi
    svc=$(systemctl is-active ananicy-cpp.service systemd-oomd.service | tr '\n' ,)
    zram=$(swapon --noheadings --show=NAME)
    if [[ $svc == active,active, && $zram == *zram* ]]; then
        ok cachyos "ananicy-cpp, systemd-oomd and zram swap are running"
    else
        bad cachyos "ananicy-cpp,systemd-oomd: $svc swap: '${zram:-none}'"
    fi
}

# The monitor must never show a console: no getty or shell may own tty1.
check_no_terminal() {
    local n
    if systemctl is-active --quiet getty@tty1.service || systemctl is-active --quiet autovt@tty1.service; then
        bad tty1 "a getty runs on tty1"
        return
    fi
    n=$(pgrep -c -x -t tty1 'agetty|login|bash|sh|zsh')
    if [[ ${n:-0} == 0 ]]; then
        ok tty1 "no getty or shell on tty1"
    else
        bad tty1 "$n login/shell processes on tty1: $(pgrep -a -t tty1 | tr '\n' ' ')"
    fi
}

check_health() {
    if systemctl is-active --quiet boot-complete.target; then
        ok boot-complete "boot-complete.target reached (vos-health passed)"
    else
        bad boot-complete "boot-complete.target is $(systemctl is-active boot-complete.target); vos-health: $(systemctl show -P Result vos-health.service)"
    fi
    if [[ -s /var/lib/vos/health-ok ]]; then ok health-ok "$(tr -d '\n' </var/lib/vos/health-ok)"; else bad health-ok "no /var/lib/vos/health-ok"; fi
}

# systemd-bless-boot drops the boot counter once boot-complete.target is
# reached: vos-V+3.conf (or +2-1) becomes vos-V.conf.
check_blessed() {
    local version=$1 i
    ls "$ESP/loader" >/dev/null 2>&1 # trigger the automount
    for ((i = 0; i < 60; i++)); do
        [[ -f $ESP/loader/entries/vos-$version.conf ]] && break
        sleep 1
    done
    if [[ -f $ESP/loader/entries/vos-$version.conf ]]; then
        ok blessed "entry vos-$version.conf lost its boot counter"
    else
        bad blessed "entry for $version was not blessed: $(ls "$ESP/loader/entries" 2>&1 | tr '\n' ' ')"
    fi
}

check_ping() {
    local version=$1 code body
    if systemctl is-active --quiet vosd.service; then ok vosd "vosd.service is active"; else bad vosd "vosd.service is $(systemctl is-active vosd.service)"; fi
    code=$(http GET /ping)
    body=$(<"$tmp/body")
    if [[ $code == 200 && $(json_lit ok <<<"$body") == true && $(json_str mode <<<"$body") == os &&
        $(json_str version <<<"$body") == "$version" ]]; then
        ok ping "GET /ping: mode os, version $version"
    else
        bad ping "GET /ping -> $code $body"
    fi
}

# Login, CSRF and the Host/source guards, the way a browser would meet them.
check_api() {
    local password=$1 code csrf channel settings jar=$tmp/cookies

    code=$(http GET /ping -H 'Host: vos-test.invalid')
    if [[ $code == 421 ]]; then ok host "unknown Host header -> 421"; else bad host "unknown Host header -> $code, expected 421"; fi

    code=$(http GET /system)
    if [[ $code == 401 ]]; then ok authed "GET /system without a session -> 401"; else bad authed "GET /system without a session -> $code, expected 401"; fi

    code=$(http POST /auth/login -H 'Content-Type: application/json' -d '{"password":"not-the-password"}')
    if [[ $code == 401 ]]; then ok bad-login "wrong password -> 401"; else bad bad-login "wrong password -> $code, expected 401"; fi

    code=$(http POST /auth/login -c "$jar" -H 'Content-Type: application/json' -d "{\"password\":$(json_quote "$password")}")
    csrf=$(json_str csrf <"$tmp/body")
    if [[ $code == 200 && -n $csrf ]]; then
        ok login "POST /auth/login -> session + csrf token"
    else
        bad login "POST /auth/login -> $code $(<"$tmp/body")"
        return
    fi

    code=$(http GET /auth/me -b "$jar")
    if [[ $code == 200 && $(json_lit authenticated <"$tmp/body") == true ]]; then
        ok auth-me "GET /auth/me with the session -> authenticated"
    else
        bad auth-me "GET /auth/me -> $code $(<"$tmp/body")"
    fi

    code=$(http GET /system -b "$jar")
    if [[ $code == 200 ]]; then ok system "GET /system with the session -> 200"; else bad system "GET /system -> $code $(<"$tmp/body")"; fi

    # Keep the test deterministic: the VM must not stage something from ghcr
    # on its own in the middle of the update tests. This is also the
    # authenticated, CSRF-carrying write path.
    code=$(http GET /update -b "$jar")
    channel=$(json_str channel <"$tmp/body")
    settings='{"auto":"off"}'
    [[ -z $channel ]] || settings="{\"channel\":$(json_quote "$channel"),\"auto\":\"off\"}"
    code=$(http PUT /update/settings -b "$jar" -H "X-VOS-CSRF: $csrf" -H 'Content-Type: application/json' -d "$settings")
    if [[ $code == 200 ]]; then ok auto-update "automatic updates switched off (with CSRF token)"; else warn auto-update "PUT /update/settings -> $code $(<"$tmp/body")"; fi

    # Last, because a broken guard would log the session out.
    code=$(http POST /auth/logout -b "$jar")
    if [[ $code == 403 ]]; then ok csrf "POST without X-VOS-CSRF -> 403"; else bad csrf "POST without X-VOS-CSRF -> $code, expected 403"; fi
}

check_hardening() {
    local caps rules
    caps=$(getcap /usr/bin/sunshine 2>/dev/null)
    if [[ $caps == *cap_sys_admin* ]]; then ok sunshine-caps "${caps#/usr/bin/sunshine }"; else bad sunshine-caps "getcap /usr/bin/sunshine: '${caps:-none}'"; fi

    rules=$(nft list ruleset 2>/dev/null)
    if grep -q 'hook input.*policy drop' <<<"$rules"; then
        ok firewall "nftables input policy is drop"
    else
        bad firewall "no nftables input chain with policy drop"
    fi
    if grep 47990 <<<"$rules" | grep -q accept; then
        bad port-47990 "a rule accepts Sunshine's admin port 47990: $(grep 47990 <<<"$rules" | head -n1)"
    else
        ok port-47990 "nothing accepts Sunshine's admin port 47990"
    fi
}

# After a staged version failed its health check on every try, the old slot
# is back and vosd has recorded the failure.
check_fallback() {
    local failed=$1 status re entries i
    re="\"failed\":\[[^]]*\"${failed//./\\.}\""
    # vosd does this bookkeeping when it starts; give it a moment.
    for ((i = 0; i < 30; i++)); do
        status=$(vos status --json 2>/dev/null | tr -d ' \t\r\n')
        [[ $status =~ $re ]] && break
        sleep 1
    done
    if [[ $status =~ $re ]]; then
        ok failed "vos status --json lists $failed in failed[]"
    else
        bad failed "$failed is not in failed[]: $(cut -c1-300 <<<"$status")"
    fi

    ls "$ESP/loader" >/dev/null 2>&1
    entries=$(ls "$ESP/loader/entries" 2>&1 | tr '\n' ' ')
    if [[ -f $ESP/loader/entries/vos-$failed+0-3.conf ]]; then
        ok tries "vos-$failed+0-3.conf: all three tries used"
    else
        bad tries "expected vos-$failed+0-3.conf; entries: $entries"
    fi
}

# ------------------------------------------------------------------ main ----

# Sourced rather than run: define the checks and stop here.
[[ ${BASH_SOURCE[0]} == "$0" ]] || return 0

usage() {
    cat >&2 <<'USAGE'
usage: vm-checks.sh system   --slot S --version V --password P
       vm-checks.sh booted   --slot S --version V [--blessed]
       vm-checks.sh fallback --slot S --version V --failed F
USAGE
    exit 2
}

group=${1:-}
[[ $# -gt 0 ]] && shift
slot="" version="" password="" failed="" blessed=0
while (($#)); do
    case $1 in
        --slot) slot=${2:-}; shift 2 ;;
        --version) version=${2:-}; shift 2 ;;
        --password) password=${2:-}; shift 2 ;;
        --failed) failed=${2:-}; shift 2 ;;
        --blessed) blessed=1; shift ;;
        *) usage ;;
    esac
done
[[ -n $slot && -n $version ]] || usage

case $group in
    system)
        [[ -n $password ]] || usage
        check_booted "$slot" "$version"
        check_layout
        check_units
        check_cachyos
        check_no_terminal
        check_health
        check_ping "$version"
        check_api "$password"
        check_hardening
        ;;
    booted)
        check_booted "$slot" "$version"
        check_layout
        check_health
        ((blessed)) && check_blessed "$version"
        check_ping "$version"
        ;;
    fallback)
        [[ -n $failed ]] || usage
        check_booted "$slot" "$version"
        check_fallback "$failed"
        check_ping "$version"
        ;;
    *) usage ;;
esac

echo "VOS-CHECKS-DONE pass=$pass warn=$warns fail=$fails"
((fails == 0))
