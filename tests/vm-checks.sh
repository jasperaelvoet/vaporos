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
#   web                                          the control center's pages only
#   extensions [--mode M] [--mounted ID]...      the extension store and merge
#
# Every check prints one line, "VOS-CHECK <ok|warn|FAIL> <name> <detail>", and
# the run ends with "VOS-CHECKS-DONE pass=N warn=N fail=N". dev.sh reads those
# lines back from the serial log; the exit status is 0 only if nothing failed.
# It runs as root on the VM and uses only what the image already has (bash,
# coreutils, util-linux, procps, curl, systemd, getcap, nft): no jq, no python.
set -uo pipefail # no -e: one failed check must not stop the others

# Overridable only so the checks themselves can be tested off the VM.
API=${VOS_API:-http://127.0.0.1/api/v1}
WEB=${VOS_WEB:-${API%/api/v1}}
ESP=${VOS_ESP:-/efi}

# The control center's pages (docs/CONTRACTS.md "Pages"). The redesigned
# UI ("next") replaces the legacy one at a single switch commit; check_web
# tells which one a build serves from the markup of GET /.
WEB_CSP="default-src 'self'; img-src 'self' data:; frame-ancestors 'none'"
WEB_PAGES_LEGACY="/ /pair /streaming /display /storage /updates /power /advanced /login /setup"
WEB_PAGES_NEXT="/ /devices /screen /system /system/updates /system/power /system/storage /system/settings /system/logs /system/about /login /setup"
# The legacy URLs the next UI still answers, as FROM=TO: 303 to TO, with the
# query kept (before TO's #fragment).
WEB_OLD_URLS="/pair=/devices#pair /streaming=/screen#stream /display=/screen /storage=/system/storage /updates=/system/updates /power=/system/power /advanced=/system/settings"
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

# page PATH [curl args...]: GET a page without following redirects. The body
# lands in $tmp/body, the headers in $tmp/headers; prints the status code.
page() {
    local path=$1
    shift
    : >"$tmp/body"
    : >"$tmp/headers"
    curl -sS -m 15 -o "$tmp/body" -D "$tmp/headers" -w '%{http_code}' "$@" "$WEB$path" 2>/dev/null ||
        true
}

# header NAME: that header's value in $tmp/headers (the first one).
header() {
    grep -i "^$1:" "$tmp/headers" | head -n1 | cut -d: -f2- | sed 's/^ *//' | tr -d '\r'
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

# Steam's first-run setup lists networks through NetworkManager, as vapor.
check_network() {
    local svc state perms rc
    svc=$(systemctl is-active NetworkManager.service systemd-networkd.service | tr '\n' ,)
    if [[ $svc == active,inactive, ]]; then ok nm "NetworkManager runs, networkd does not"; else bad nm "NetworkManager,networkd: $svc"; fi
    state=$(nmcli -t -f STATE general 2>/dev/null)
    if [[ $state == connected* ]]; then ok nm-state "NetworkManager is $state"; else bad nm-state "NetworkManager is '${state:-unreachable}'"; fi
    perms=$(runuser -u vapor -- nmcli -t general permissions 2>/dev/null)
    if grep -qx 'org.freedesktop.NetworkManager.wifi.scan:yes' <<<"$perms" &&
       grep -qx 'org.freedesktop.NetworkManager.settings.modify.system:yes' <<<"$perms"; then
        ok nm-polkit "vapor may scan and save networks"
    else
        bad nm-polkit "vapor's NetworkManager permissions: $(tr '\n' ' ' <<<"$perms")"
    fi
    /usr/bin/steamos-update check >/dev/null 2>&1
    rc=$?
    if (( rc == 7 )); then ok steamos-update "reports no update"; else bad steamos-update "exited $rc, expected 7"; fi
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
    # boot-complete.target only exists on boots systemd-boot is counting
    # (after an update); a fresh install boots an uncounted entry.
    if systemctl is-active --quiet boot-complete.target; then
        ok boot-complete "boot-complete.target reached (vos-health passed)"
    elif [[ ! -e /sys/firmware/efi/efivars/LoaderBootCountPath-4a67b082-0a4c-41cf-b6c7-440b29bb8c4f ]] &&
         systemctl is-active --quiet vos-health.service; then
        ok boot-complete "uncounted boot; vos-health passed"
    else
        bad boot-complete "boot-complete.target is $(systemctl is-active boot-complete.target); vos-health is $(systemctl is-active vos-health.service)"
    fi
    if [[ -s /var/lib/vos/health-ok ]]; then ok health-ok "$(tr -d '\n' </var/lib/vos/health-ok)"; else bad health-ok "no /var/lib/vos/health-ok"; fi
    # On an uncounted boot vos health never fails its unit (there is nothing
    # to fall back to), so "active" above also covers a failed check. Its
    # verdict is in this boot's journal: "health: ok" or "health: degraded (...)".
    local verdict
    verdict=$(journalctl -b -u vos-health.service -o cat --no-pager 2>/dev/null |
        grep -oE '^health: (ok$|degraded.*|FAILED.*)' | tail -n 1)
    case $verdict in
        'health: ok') ok health "vos health: ok" ;;
        '') warn health "no verdict from vos health in this boot's journal" ;;
        *) bad health "vos health on this boot: ${verdict#health: }" ;;
    esac
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

# Every page of the UI set this build serves answers 200 with the CSP, the
# assets the home page links answer, and (in the next UI) the legacy URLs
# redirect to their new pages. Pages are Public: no session needed.
check_web() {
    local code ui pages p csp type failed=0 n=0 u from to want loc asset
    code=$(page /)
    if [[ $code != 200 ]]; then
        bad web "GET / -> $code, expected 200"
        return
    fi
    cp "$tmp/body" "$tmp/home"
    if grep -q 'data-page="home"' "$tmp/home"; then
        ui=next pages=$WEB_PAGES_NEXT
    elif grep -q 'data-page="dashboard"' "$tmp/home"; then
        ui=legacy pages=$WEB_PAGES_LEGACY
    else
        bad web "GET / is neither UI's home page: $(tr -s ' \n' ' ' <"$tmp/home" | cut -c1-200)"
        return
    fi

    for p in $pages; do
        n=$((n + 1))
        code=$(page "$p")
        csp=$(header Content-Security-Policy)
        type=$(header Content-Type)
        if [[ $code != 200 || $csp != "$WEB_CSP" || $type != text/html* ]]; then
            bad web-page "GET $p -> $code, type '${type:-none}', CSP '${csp:-none}'"
            failed=$((failed + 1))
        fi
    done
    ((failed)) || ok web-pages "$n pages of the $ui UI answer 200 with the CSP"

    failed=0 n=0
    for asset in $(grep -oE '(href|src)="/static/[^"]+"' "$tmp/home" | sed 's/^[a-z]*="//; s/"$//' | sort -u); do
        n=$((n + 1))
        code=$(page "$asset")
        if [[ $code != 200 ]]; then
            bad web-asset "GET $asset -> $code"
            failed=$((failed + 1))
        fi
    done
    if ((n == 0)); then
        bad web-assets "GET / links no /static/ assets"
    elif ((failed == 0)); then
        ok web-assets "the $n assets the home page links answer 200"
    fi

    code=$(page /vos-check-no-such-page)
    if [[ $code == 404 ]]; then ok web-404 "an unknown page -> 404"; else bad web-404 "GET /vos-check-no-such-page -> $code, expected 404"; fi

    [[ $ui == next ]] || return 0
    failed=0 n=0
    for u in $WEB_OLD_URLS; do
        n=$((n + 1))
        from=${u%%=*} to=${u#*=}
        want=${to%%#*}?vos-check=1
        [[ $to != *#* ]] || want+="#${to#*#}"
        code=$(page "$from?vos-check=1")
        loc=$(header Location)
        if [[ $code != 303 || $loc != "$want" ]]; then
            bad web-old-url "GET $from?vos-check=1 -> $code to '${loc:-nowhere}', expected 303 to $want"
            failed=$((failed + 1))
            continue
        fi
        code=$(page "$from?vos-check=1" -L)
        if [[ $code != 200 ]]; then
            bad web-old-url "GET $from, followed to ${to%%#*} -> $code, expected 200"
            failed=$((failed + 1))
        fi
    done
    ((failed)) || ok web-old-urls "$n legacy URLs answer 303 to their new page (query kept), which answers 200"
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

# No keypress, on a local keyboard or a Moonlight client's (Sunshine's
# virtual keyboard), reboots or suspends the box.
check_no_reboot_keys() {
    local cad burst sysrq key have
    cad=$(systemctl is-enabled ctrl-alt-del.target 2>/dev/null)
    burst=$(systemctl show --property=CtrlAltDelBurstAction --value 2>/dev/null)
    if [[ $cad == masked && $burst == none ]]; then
        ok ctrl-alt-del "ctrl-alt-del.target is masked, CtrlAltDelBurstAction=none"
    else
        bad ctrl-alt-del "ctrl-alt-del.target is '${cad:-?}', CtrlAltDelBurstAction '${burst:-?}'"
    fi
    sysrq=$(cat /proc/sys/kernel/sysrq 2>/dev/null)
    if [[ $sysrq == 0 ]]; then ok sysrq "kernel.sysrq = 0"; else bad sysrq "kernel.sysrq = '${sysrq:-?}', expected 0"; fi
    for key in HandleRebootKey HandleSuspendKey HandleHibernateKey; do
        have=$(busctl get-property org.freedesktop.login1 /org/freedesktop/login1 \
            org.freedesktop.login1.Manager "$key" 2>/dev/null)
        if [[ $have == 's "ignore"' ]]; then
            ok "logind-$key" "$key=ignore"
        else
            bad "logind-$key" "logind $key is '${have:-?}', expected ignore"
        fi
    done
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

# Extensions (docs/CONTRACTS.md "Extensions"): what the initramfs merged, and
# that every mounted image is sealed (fs-verity), measured as the read-only
# catalog says and refuses writes, even from root.
check_extensions() {
    local want_mode=$1 report mode id line sha fsv img have mounted
    shift
    report=$(tr -d ' \t\r\n' </run/vos/extensions.json 2>/dev/null)
    if [[ -z $report ]]; then
        bad ext-report "/run/vos/extensions.json is missing or empty"
        return
    fi
    mode=$(json_str mode <<<"$report")
    if [[ -z $want_mode || $mode == "$want_mode" ]]; then
        ok ext-report "mode $mode, set $(json_str set <<<"$report"), reason '$(json_str reason <<<"$report")'"
    else
        bad ext-report "mode '$mode', expected $want_mode: $(cut -c1-300 <<<"$report")"
    fi
    if grep -q '^dispatcher ' /usr/lib/vos/extensions.list 2>/dev/null; then
        ok ext-catalog "$(grep -c '^ext ' /usr/lib/vos/extensions.list) extension(s) in /usr/lib/vos/extensions.list"
    else
        bad ext-catalog "/usr/lib/vos/extensions.list is missing or has no dispatcher line"
    fi
    for unit in systemd-sysext.service systemd-confext.service; do
        have=$(systemctl is-enabled "$unit" 2>/dev/null)
        if [[ $have == masked ]]; then ok "ext-$unit" "masked"; else bad "ext-$unit" "is '${have:-?}', expected masked"; fi
    done
    mounted=${report#*\"mounted\":[}
    mounted=${mounted%%]*}
    for id in "$@"; do
        if [[ $mounted != *"\"id\":\"$id\""* ]]; then
            bad "ext-$id" "not mounted: $(cut -c1-300 <<<"$report")"
            continue
        fi
        line=$(grep "^ext $id " /usr/lib/vos/extensions.list)
        sha=$(cut -d' ' -f3 <<<"$line")
        fsv=$(cut -d' ' -f5 <<<"$line")
        img=/var/lib/vos/ext/images/$sha.raw
        have=$(fsverity measure "$img" 2>/dev/null | cut -d' ' -f1)
        if [[ $have == "sha256:$fsv" ]]; then
            ok "ext-$id-sealed" "$img measures sha256:${fsv:0:16}..."
        else
            bad "ext-$id-sealed" "fsverity measure $img: '${have:-failed}', catalog says sha256:$fsv"
        fi
        if { : >>"$img"; } 2>/dev/null; then
            bad "ext-$id-immutable" "root could open $img for writing"
        else
            ok "ext-$id-immutable" "root cannot open the image for writing"
        fi
        if [[ -d /usr/lib/vos/ext/$id ]]; then
            ok "ext-$id-usr" "/usr/lib/vos/ext/$id is in the merged /usr"
        else
            bad "ext-$id-usr" "/usr/lib/vos/ext/$id is missing from /usr"
        fi
    done
    if (($#)); then
        have=$(findmnt -no FSTYPE /usr | tail -n1)
        if [[ $have == overlay ]]; then ok ext-usr-overlay "/usr is an overlay"; else bad ext-usr-overlay "/usr is '$have', expected overlay"; fi
    fi
    if [[ " $* " == *" proton "* ]]; then
        if [[ -f /usr/share/steam/compatibilitytools.d/proton-cachyos-slr/compatibilitytool.vdf ]]; then
            ok ext-proton-tool "proton-cachyos-slr is in /usr/share/steam/compatibilitytools.d"
        else
            bad ext-proton-tool "no /usr/share/steam/compatibilitytools.d/proton-cachyos-slr/compatibilitytool.vdf"
        fi
        have=$(stat -c %a /dev/ntsync 2>/dev/null)
        if [[ $have == 666 ]]; then ok ext-ntsync "/dev/ntsync is 0666"; else warn ext-ntsync "/dev/ntsync mode '${have:-missing}'"; fi
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
       vm-checks.sh web
       vm-checks.sh extensions [--mode M] [--mounted ID]...
USAGE
    exit 2
}

group=${1:-}
[[ $# -gt 0 ]] && shift
slot="" version="" password="" failed="" blessed=0 mode="" mounted=()
while (($#)); do
    case $1 in
        --slot) slot=${2:-}; shift 2 ;;
        --version) version=${2:-}; shift 2 ;;
        --password) password=${2:-}; shift 2 ;;
        --failed) failed=${2:-}; shift 2 ;;
        --blessed) blessed=1; shift ;;
        --mode) mode=${2:-}; shift 2 ;;
        --mounted) mounted+=("${2:-}"); shift 2 ;;
        *) usage ;;
    esac
done
[[ $group == web || $group == extensions || -n $slot && -n $version ]] || usage

case $group in
    system)
        [[ -n $password ]] || usage
        check_booted "$slot" "$version"
        check_layout
        check_units
        check_cachyos
        check_network
        check_no_terminal
        check_health
        check_ping "$version"
        check_api "$password"
        check_web
        check_hardening
        check_no_reboot_keys
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
    web)
        check_web
        ;;
    extensions)
        check_extensions "$mode" "${mounted[@]}"
        ;;
    *) usage ;;
esac

echo "VOS-CHECKS-DONE pass=$pass warn=$warns fail=$fails"
((fails == 0))
