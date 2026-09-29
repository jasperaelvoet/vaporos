#!/usr/bin/env bash
#
# Install a VaporOS ISO in a throwaway QEMU VM through the web installer, boot
# the result, and check that it answers. This is what a user does, minus the
# phone: no keyboard and no serial shell (a release image has none), only the
# serial announcements from docs/CONTRACTS.md ("Serial lines") and the HTTP API
# through a forwarded port. CI runs it on every build (.github/workflows);
# it also runs on any Linux box with qemu-system-x86_64, qemu-img and OVMF.
#
#   tests/qemu-smoke.sh ISO VERSION [WORKDIR]
#
# Environment:
#   ACCEL=kvm|tcg   default: kvm when /dev/kvm is usable. TCG needs QEMU >= 7.2
#                   (x86-64-v3 instructions) and is very slow.
#   SCALE=N         multiplies every timeout (default 1 with KVM, 8 with TCG)
#   PORT=8080       host port forwarded to the VM's port 80
#   OVMF_CODE, OVMF_VARS   firmware images, found automatically
#
# WORKDIR (default $TMPDIR/vaporos-vm-smoke) ends up with serial.log,
# qemu-*.log and screens/*.png, which is what to look at when this fails.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
SERIAL_PY=$here/../scripts/serial.py

ISO=${1:?usage: $0 ISO VERSION [WORKDIR]}
VERSION=${2:?usage: $0 ISO VERSION [WORKDIR]}
WORK=${3:-${TMPDIR:-/tmp}/vaporos-vm-smoke}
PORT=${PORT:-8080}
DISK=/dev/vda # virtio-blk, the first disk
HOSTNAME_=vapor-ci
PASSWORD=vapor-ci-$RANDOM$RANDOM

if [[ -z ${ACCEL:-} ]]; then
    if [[ -r /dev/kvm && -w /dev/kvm ]]; then ACCEL=kvm; else ACCEL=tcg; fi
fi
if [[ -z ${SCALE:-} ]]; then
    if [[ $ACCEL == kvm ]]; then SCALE=1; else SCALE=8; fi
fi

mkdir -p "$WORK/screens"
WORK=$(cd "$WORK" && pwd)
LOG=$WORK/serial.log
QMP=$WORK/qmp.sock
API=http://127.0.0.1:$PORT/api/v1
QEMU_PID=""
BOOTS=0
T0=$SECONDS

# Both write to stderr: they are also called inside $(...), and set -e ends
# the script when such a call dies.
log() { printf '[%4ds] %s\n' $((SECONDS - T0)) "$*" >&2; }
# Die with the end of the serial log, the usual place the reason shows up.
die() {
    {
        printf '::error::%s\n' "$*"
        echo "---- last lines of $LOG"
        tail -c 4000 "$LOG" 2>/dev/null | tr -d '\r' | tail -n 40 || true
    } >&2
    exit 1
}

# ------------------------------------------------------------------ vm ----

find_ovmf() {
    local pair code vars
    # Pairs of (code, vars) with matching sizes; no Secure Boot, since the
    # VaporOS kernel is unsigned.
    for pair in \
        /usr/share/OVMF/OVMF_CODE_4M.fd:/usr/share/OVMF/OVMF_VARS_4M.fd \
        /usr/share/OVMF/OVMF_CODE.fd:/usr/share/OVMF/OVMF_VARS.fd \
        /usr/share/edk2/x64/OVMF_CODE.4m.fd:/usr/share/edk2/x64/OVMF_VARS.4m.fd \
        /usr/share/edk2/ovmf/OVMF_CODE.fd:/usr/share/edk2/ovmf/OVMF_VARS.fd \
        /usr/share/qemu/edk2-x86_64-code.fd:/usr/share/qemu/edk2-i386-vars.fd; do
        code=${pair%%:*} vars=${pair#*:}
        if [[ -f $code && -f $vars ]]; then
            OVMF_CODE=${OVMF_CODE:-$code} OVMF_VARS=${OVMF_VARS:-$vars}
            return
        fi
    done
    [[ -n ${OVMF_CODE:-} && -n ${OVMF_VARS:-} ]] || die "no OVMF firmware found (install ovmf, or set OVMF_CODE and OVMF_VARS)"
}

# start_vm iso|disk: boot from the ISO (first run) or from the disk. Both runs
# share the disk, the UEFI variables and one serial log; -no-reboot makes
# every guest reboot end QEMU, so each boot is a run the script controls.
start_vm() {
    local from=$1 cpu=host args=()
    [[ $ACCEL == kvm ]] || cpu=max
    BOOTS=$((BOOTS + 1))
    args=(
        -name vaporos-smoke -machine q35,accel="$ACCEL" -cpu "$cpu"
        -smp "$(nproc)" -m 4096
        -drive if=pflash,format=raw,readonly=on,file="$OVMF_CODE"
        -drive if=pflash,format=raw,file="$WORK/vars.fd"
        -drive if=none,id=hd,file="$WORK/disk.qcow2",format=qcow2,discard=unmap
        -device virtio-blk-pci,drive=hd,bootindex=1
        -netdev user,id=net0,hostfwd=tcp:127.0.0.1:"$PORT"-:80
        -device virtio-net-pci,netdev=net0
        -chardev file,id=ser0,path="$LOG",append=on -serial chardev:ser0
        -vga std -display none
        -qmp unix:"$QMP",server=on,wait=off
        -no-reboot
    )
    if [[ $from == iso ]]; then
        args+=(-drive if=none,id=cd,file="$ISO",media=cdrom,readonly=on
            -device ide-cd,drive=cd,bus=ide.0,bootindex=0)
    fi
    rm -f "$QMP"
    qemu-system-x86_64 "${args[@]}" >"$WORK/qemu-$BOOTS.log" 2>&1 &
    QEMU_PID=$!
    log "QEMU started ($from, $ACCEL, pid $QEMU_PID)"
}

alive() { [[ -n $QEMU_PID ]] && kill -0 "$QEMU_PID" 2>/dev/null; }

stop_vm() {
    alive || return 0
    python3 "$here/qmp.py" "$QMP" quit >/dev/null 2>&1 || true
    for _ in $(seq 20); do alive || break; sleep 0.5; done
    alive && kill -9 "$QEMU_PID" 2>/dev/null
    wait "$QEMU_PID" 2>/dev/null || true
    QEMU_PID=""
}
trap stop_vm EXIT

# Wait for the guest to end QEMU with a reboot or poweroff.
wait_exit() {
    local deadline=$((SECONDS + $1))
    while alive && ((SECONDS < deadline)); do sleep 1; done
    ! alive
}

# wait_serial REGEX SECONDS: like serial.py expect, but gives up at once when
# QEMU exits (a crash, or a reboot nobody asked for).
wait_serial() {
    local regex=$1 deadline=$((SECONDS + $2 * SCALE)) out
    while ((SECONDS < deadline)); do
        if out=$(python3 "$SERIAL_PY" expect "$LOG" "$regex" 10 2>/dev/null); then
            printf '%s' "$out"
            return 0
        fi
        if ! alive; then
            # One last look: the line may have landed just before it exited.
            python3 "$SERIAL_PY" expect "$LOG" "$regex" 0 2>/dev/null && return 0
            die "QEMU exited while waiting for /$regex/ (see $WORK/qemu-$BOOTS.log)"
        fi
    done
    die "timed out after $(($2 * SCALE))s waiting for /$regex/"
}

# Save the display to screens/NAME.png (best effort).
screendump() {
    local ppm=$WORK/screens/$1.ppm
    alive || return 0
    if python3 "$here/qmp.py" "$QMP" screendump "{\"filename\": \"$ppm\"}" >/dev/null 2>&1 &&
        python3 "$here/ppm2png.py" "$ppm" "${ppm%.ppm}.png" 2>/dev/null; then
        rm -f "$ppm"
        log "screen saved: screens/$1.png"
    fi
    return 0
}

# ----------------------------------------------------------------- http ----

# http METHOD PATH [curl args...]: the body lands in $WORK/body, the status
# code is printed ("000" when nothing answered).
http() {
    local method=$1 path=$2
    shift 2
    : >"$WORK/body"
    curl -sS -m 30 -X "$method" -o "$WORK/body" -w '%{http_code}' "$@" "$API$path" 2>/dev/null || true
}

# json FIELD: a top-level field of $WORK/body, or "" (true/false as JSON).
json() {
    python3 -c 'import json, sys
try:
    v = json.load(open(sys.argv[1])).get(sys.argv[2], "")
except Exception:
    v = ""
print(json.dumps(v) if isinstance(v, bool) else v)' "$WORK/body" "$1"
}

expect_ping() {
    local mode=$1 code
    code=$(http GET /ping)
    [[ $code == 200 ]] || die "GET /ping -> $code $(cat "$WORK/body")"
    [[ $(json ok) == true && $(json mode) == "$mode" && $(json version) == "$VERSION" ]] ||
        die "GET /ping: expected mode $mode, version $VERSION; got $(cat "$WORK/body")"
    log "GET /ping: ok, mode $mode, version $VERSION"
}

# -------------------------------------------------------------------- run ----

command -v qemu-system-x86_64 >/dev/null || die "qemu-system-x86_64 is not installed"
command -v qemu-img >/dev/null || die "qemu-img is not installed"
[[ -f $ISO ]] || die "no ISO at $ISO"
find_ovmf
log "ISO $(basename "$ISO"), expecting version $VERSION; firmware $OVMF_CODE; accel $ACCEL, timeouts x$SCALE"

rm -f "$LOG" "$LOG.pos" "$WORK"/qemu-*.log "$WORK/disk.qcow2"
cp "$OVMF_VARS" "$WORK/vars.fd"
qemu-img create -q -f qcow2 "$WORK/disk.qcow2" 40G

# 1. The live ISO announces the installer: its address and setup code.
start_vm iso
m=$(wait_serial 'VOS-READY mode=installer version=(\S+) ip=(\S+) code=(\S+)' 600)
read -r live_version ip code <<<"$m"
log "installer up: version $live_version, guest ip $ip, setup code $code"
[[ $live_version == "$VERSION" ]] || die "the ISO runs $live_version, expected $VERSION"
expect_ping installer
screendump 1-installer

if [[ $code != - ]]; then
    c=$(http GET /install/probe -H 'X-VOS-Setup: not-the-code')
    [[ $c == 403 ]] || die "a wrong setup code got $c from GET /install/probe, expected 403"
    log "a wrong setup code is refused (403)"
fi
c=$(http GET /install/probe -H "X-VOS-Setup: $code")
[[ $c == 200 ]] || die "GET /install/probe -> $c $(cat "$WORK/body")"
grep -q "\"$DISK\"" "$WORK/body" || die "the installer does not offer $DISK: $(cat "$WORK/body")"

# 2. Install through the API, as the web wizard does.
body=$(printf '{"disk":"%s","mode":"erase","hostname":"%s","password":"%s","timezone":"UTC","libraries":[],"source":""}' \
    "$DISK" "$HOSTNAME_" "$PASSWORD")
c=$(http POST /install -H "X-VOS-Setup: $code" -H 'Content-Type: application/json' -d "$body")
[[ $c == 200 ]] || die "POST /install -> $c $(cat "$WORK/body")"
log "install started (job $(json job))"
state=$(wait_serial 'VOS-INSTALL state=(\S+)' 1800)
http GET /install/status -H "X-VOS-Setup: $code" >/dev/null
[[ $state == done ]] || die "install $state: $(cat "$WORK/body")"
log "install done"

# 3. Reboot through the API; -no-reboot turns that into QEMU exiting.
c=$(http POST /install/reboot -H "X-VOS-Setup: $code")
[[ $c == 200 ]] || log "POST /install/reboot -> $c; stopping the VM instead"
wait_exit $((180 * SCALE)) || { log "the installer did not reboot; stopping the VM"; stop_vm; }
QEMU_PID=""

# 4. The installed system boots from the disk alone and passes its health
#    check (a failed one reboots, which ends QEMU here).
start_vm disk
v=$(wait_serial 'VOS-READY mode=os version=(\S+)' 600)
[[ $v == "$VERSION" ]] || die "the installed system runs $v, expected $VERSION"
log "installed system up: version $v"
expect_ping os
screendump 2-os

c=$(http POST /auth/login -H 'Content-Type: application/json' -d "{\"password\":\"$PASSWORD\"}" -c "$WORK/cookies")
[[ $c == 200 && -n $(json csrf) ]] || die "POST /auth/login with the install password -> $c $(cat "$WORK/body")"
c=$(http GET /auth/me -b "$WORK/cookies")
[[ $c == 200 && $(json authenticated) == true ]] || die "GET /auth/me after login -> $c $(cat "$WORK/body")"
log "logged in with the password set during the install"

log "watching the health check (a failure reboots the VM)"
sleep $((45 * SCALE))
alive || die "the installed system rebooted on its own: its health check probably failed"
expect_ping os
screendump 3-os-idle

log "PASS: installed $VERSION through the web installer, booted it and logged in"
if [[ -n ${GITHUB_STEP_SUMMARY:-} ]]; then
    printf '### VM smoke test (%s)\nInstalled `%s` on `%s` through the web installer, booted it from disk, logged in. %ss.\n' \
        "$ACCEL" "$VERSION" "$DISK" $((SECONDS - T0)) >>"$GITHUB_STEP_SUMMARY"
fi
