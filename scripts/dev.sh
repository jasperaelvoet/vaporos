#!/usr/bin/env bash
#
# The VaporOS dev loop. Builds run in a Docker LXC on the Proxmox host (see
# scripts/build.sh), which also runs the VM and serves the built image to it.
#
#   build     build out/ if anything changed since the last build
#   dev       build if anything changed, then bring the dev VM to that build:
#             create + install it, or `vos update` + reboot it. The default.
#   shell     interactive serial shell in the VM (leave with Ctrl-O)
#   console   open the VM's display in a native Screen Sharing window
#   log       follow the VM's serial console
#   reset     wipe the dev VM and install it from scratch
#   test      reinstall the dev VM from scratch and check it end to end: web
#             install, API, hardening, update, rollback, health fallback
#   status | down | destroy
#   send TXT | expect REGEX [TIMEOUT]    drive the serial console by hand
#
# The install never types on the VM: the live ISO prints its address and setup
# code on the serial console (VOS-READY, docs/CONTRACTS.md), and the install
# goes through the web installer's API with that code, as a phone would. Dev
# images are debug builds, so the installed system has a root shell on its
# serial console; updates and checks use that.
#
# Nothing needs configuring. To override the defaults below, put them in a
# (gitignored) .dev.env at the repo root. CONSOLE=0 never opens a window.
#
set -euo pipefail
cd "$(dirname "$0")/.."
[[ -f .dev.env ]] && . ./.dev.env

PVE_HOST=${PVE_HOST:-192.168.1.2}
PVE_USER=${PVE_USER:-root}
VMID=${VMID:-9000}
VM_NAME=${VM_NAME:-vaporos-dev}
VM_HOSTNAME=${VM_HOSTNAME:-vosdev}
ISO_STORAGE=${ISO_STORAGE:-local}
ISO_DIR=${ISO_DIR:-/var/lib/vz/template/iso}
DISK_STORAGE=${DISK_STORAGE:-local-lvm}
BRIDGE=${BRIDGE:-vmbr0}
MEM=${MEM:-2048}
CORES=${CORES:-4}
DISK_SIZE=${DISK_SIZE:-48}
SERVE_DIR=${SERVE_DIR:-/var/lib/vz/vos-dev}
SERVE_PORT=${SERVE_PORT:-8000}
CONSOLE=${CONSOLE:-1}
TIMEZONE=${TIMEZONE:-Europe/Brussels}
# The web admin password the automatic install sets.
ADMIN_PASS=${ADMIN_PASS:-vapor-dev}

ISO_NAME=vaporos-dev.iso
# What `vos update --from http://…/` fetches, in upload order: the manifest
# goes last, so the VM never sees a new manifest beside an old root.erofs.
UPDATE_FILES=(root.erofs vmlinuz initramfs.img manifest.json.sig manifest.json)

# Set by `test`: stricter install checks, and screendumps into out/screens/.
TESTING=0
# Set by vm_install from the serial announcements.
VM_IP="" SETUP_CODE="" VM_VERSION=""

# Everything that depends on which VM we are driving.
use_vm() {
    VMID=$1 VM_NAME=$2 VM_HOSTNAME=$3
    REMOTE_DIR=/tmp/vos-$VMID
    LOG=$REMOTE_DIR/serial.log
    FIFO=$REMOTE_DIR/serial.in
}
use_vm "$VMID" "$VM_NAME" "$VM_HOSTNAME"

pve()  { ssh -o LogLevel=ERROR "$PVE_USER@$PVE_HOST" "$@"; }
say()  { printf '\e[1;34m==>\e[0m \e[1m%s\e[0m\n' "$*"; }
ok()   { printf '\e[1;32m==>\e[0m %s\n' "$*"; }
warn() { printf '\e[1;33m==>\e[0m %s\n' "$*"; }
die()  { printf '\e[1;31merror:\e[0m %s\n' "$*" >&2; exit 1; }

# Die, showing the end of the serial log so the reason is on screen.
fail() {
    printf '\e[2m' >&2
    pve "tail -c 2500 $LOG 2>/dev/null" | tr -d '\r' | tail -n 25 >&2 || true
    printf '\e[0m' >&2
    die "$*"
}

# Just enough JSON for vosd's flat replies (no jq on either end).
# json_str KEY: the first string value of KEY on stdin.
json_str() { grep -o "\"$1\" *: *\"[^\"]*\"" | head -n1 | sed 's/.*: *"//; s/"$//' || true; }
# json_lit KEY: the first number/true/false/null value of KEY on stdin.
json_lit() { grep -o "\"$1\" *: *[-a-z0-9.]*" | head -n1 | sed 's/.*: *//' || true; }

# ------------------------------------------------------------- preflight ----

preflight_pve() {
    ssh -o BatchMode=yes -o ConnectTimeout=5 -o LogLevel=ERROR \
        "$PVE_USER@$PVE_HOST" true 2>/dev/null ||
        die "cannot ssh to $PVE_USER@$PVE_HOST without a password. Run once:  ssh-copy-id $PVE_USER@$PVE_HOST"
    pve "command -v qm >/dev/null && command -v python3 >/dev/null && command -v curl >/dev/null" ||
        die "$PVE_HOST is not a Proxmox host with python3 and curl"
}

preflight_docker() {
    docker info >/dev/null 2>&1 && return
    command -v orb >/dev/null || die "Docker is not running (install OrbStack: brew install orbstack)"
    say "Starting OrbStack"
    orb start >/dev/null
    docker info >/dev/null 2>&1 || die "Docker is still not reachable after starting OrbStack"
}

# ----------------------------------------------------------------- build ----

# Everything the image is made from; a change to any of it means a rebuild.
# Go test files never end up in the image.
inputs_hash() {
    local files p paths=()
    for p in rootfs build packages.txt go.mod go.sum cmd internal keys; do
        [[ -e $p ]] && paths+=("$p")
    done
    files=$(find "${paths[@]}" -type f ! -name .DS_Store ! -name '*_test.go' | sort)
    { tr '\n' '\0' <<<"$files" | xargs -0 stat -f '%p %N'
      tr '\n' '\0' <<<"$files" | xargs -0 shasum -a 256; } | shasum -a 256 | cut -c1-16
}

have_build() { [[ -f out/manifest.json && -f out/root.erofs && -n $(ls out/*.iso 2>/dev/null) ]]; }

build_version() { json_str version <out/manifest.json; }

build_if_needed() {
    local want
    want=$(inputs_hash)
    if have_build && [[ -f out/.inputs && $(<out/.inputs) == "$want" ]]; then
        ok "Build $(build_version) is current"
        return
    fi
    [[ ${BUILDER:-pve} != local ]] || preflight_docker
    say "Building"
    rm -f out/.inputs
    ./scripts/build.sh
    echo "$want" >out/.inputs
}

# A second, newer build of the same tree, so `test` has something to update
# to. The version is the build time, passed explicitly so it always differs.
rebuild() {
    rm -f out/.inputs
    VERSION=$(date -u +%Y%m%d.%H%M%S) ./scripts/build.sh
    inputs_hash >out/.inputs
}

local_iso() {
    local iso
    iso=$(ls -t out/*.iso 2>/dev/null | head -1) || true
    [[ -n $iso ]] || die "no ISO in out/"
    printf '%s' "$iso"
}

# Put a file from out/ on the Proxmox host. A build made by the builder is
# already there, so copy it locally. Otherwise rsync with a delta against what
# is already there: consecutive builds share most of their bytes.
push() {
    if [[ -f out/.built-on ]]; then
        pve "cp $(<out/.built-on)/$(basename "$1") $2"
    else
        rsync -t --inplace --partial "$1" "$PVE_USER@$PVE_HOST:$2"
    fi
}

upload_iso() {
    local iso
    iso=$(local_iso)
    say "Uploading $(basename "$iso") to $PVE_HOST"
    # Older tooling kept one versioned ISO per build.
    pve "rm -f $ISO_DIR/watervaporos-*.iso $ISO_DIR/vaporos-2*.iso"
    push "$iso" "$ISO_DIR/$ISO_NAME"
}

# Serve SERVE_DIR over HTTP from the Proxmox host, so the VM can always reach
# it no matter which network or firewall this machine is on.
ensure_http() {
    pve "mkdir -p $SERVE_DIR && { systemctl is-active --quiet vos-dev-http ||
         systemd-run --quiet --collect --unit vos-dev-http -p WorkingDirectory=$SERVE_DIR \
             python3 -m http.server --bind $PVE_HOST $SERVE_PORT; }"
}

# Put the update payload next to the VM, for `vos update --from http://…/`.
stage_update() {
    local f
    [[ -f out/manifest.json.sig ]] ||
        die "out/manifest.json.sig is missing: the build did not sign its manifest (the dev key lives on the builder)"
    say "Staging $(build_version) for update"
    ensure_http
    # Older builds served manifest.env; a stale manifest must never be served.
    pve "rm -f $SERVE_DIR/manifest.env $SERVE_DIR/manifest.json $SERVE_DIR/manifest.json.sig"
    for f in "${UPDATE_FILES[@]}"; do
        push "out/$f" "$SERVE_DIR/$f"
    done
}

# ---------------------------------------------------------------- serial ----

# serial.py drives the console; ppm2png.py converts screendumps. Both run on
# the Proxmox host.
copy_helpers() {
    scp -q scripts/serial.py tests/ppm2png.py "$PVE_USER@$PVE_HOST:$REMOTE_DIR/"
}

start_broker() {
    pve "systemctl stop vos-serial-$VMID 2>/dev/null; rm -rf $REMOTE_DIR; mkdir -p $REMOTE_DIR"
    copy_helpers
    pve "systemd-run --quiet --collect --unit vos-serial-$VMID \
            python3 $REMOTE_DIR/serial.py broker /var/run/qemu-server/$VMID.serial0 $LOG $FIFO"
}

ensure_broker() {
    if pve "systemctl is-active --quiet vos-serial-$VMID"; then
        # The running broker keeps its code; the helpers must match this checkout.
        copy_helpers
    else
        start_broker
    fi
}

match_()  { pve "python3 $REMOTE_DIR/serial.py expect $LOG $(printf %q "$1") ${2:-300}"; }
expect_() { match_ "$@" >/dev/null; }
send_()   { pve "python3 $REMOTE_DIR/serial.py send $FIFO $(printf %q "$1")"; }
# Wait for "$1=<exit code>" and succeed only if it is 0, so a failure is
# reported at once instead of after the whole timeout.
rc_is_zero() { [[ $(match_ "$1=([0-9]+)" "$2") == 0 ]]; }
mark_()   { pve "python3 $REMOTE_DIR/serial.py mark $LOG"; }

# Get the root shell on the serial console (debug images log root in on
# ttyS0). Prints "os" (installed system) or "live" (the ISO). A VM that is
# still booting drops what is typed before its shell is up, so keep asking;
# Ctrl-C first clears any half-typed line.
serial_shell() {
    local timeout=${1:-20} tok=$RANDOM deadline m
    deadline=$((SECONDS + timeout))
    mark_
    while ((SECONDS < deadline)); do
        send_ $'\003'
        # The quotes keep the typed-in command from matching its own output.
        send_ "echo VOS\"\"-PING-$tok-\$(grep -qw vos.mode=live /proc/cmdline && echo live || echo os)"
        if m=$(match_ "VOS-PING-$tok-(live|os)" 5 2>/dev/null); then
            echo "$m"
            return 0
        fi
    done
    return 1
}

# The version of the running image, from the serial shell.
vm_version() {
    mark_
    send_ "grep -o '\"version\": *\"[^\"]*\"' /usr/lib/vos/image.json"
    match_ '"version": *"([0-9]+\.[0-9]+)"' 20 2>/dev/null
}

# Save what the VM's monitor shows to out/screens/NN-NAME.png (best effort):
# the record that the display only ever shows the logo, black or the welcome
# screen, and never a terminal.
screendump() {
    local name=$1 ppm=/tmp/vos-$VMID.ppm png=/tmp/vos-$VMID.png dest n
    mkdir -p out/screens
    n=$(find out/screens -type f | wc -l | tr -d ' ')
    dest=out/screens/$(printf '%02d' $((n + 1)))-$name
    if ! pve "rm -f $ppm $png; echo 'screendump $ppm' | qm monitor $VMID >/dev/null 2>&1
              for i in 1 2 3 4 5 6 7 8 9 10; do [ -s $ppm ] && break; sleep 0.5; done; [ -s $ppm ]"; then
        warn "screendump '$name' failed"
        return 0
    fi
    if pve "python3 $REMOTE_DIR/ppm2png.py $ppm $png"; then
        scp -q "$PVE_USER@$PVE_HOST:$png" "$dest.png" && ok "screen saved: $dest.png"
    else
        scp -q "$PVE_USER@$PVE_HOST:$ppm" "$dest.ppm" && ok "screen saved: $dest.ppm"
    fi
    return 0
}

# ------------------------------------------------------------------- vm -----

# Refuse to touch a VMID that is not ours, so a typo cannot delete a real VM.
assert_ours() {
    local name
    name=$(pve "qm config $VMID 2>/dev/null | sed -n 's/^name: //p'") || true
    [[ -z $name || $name == "$VM_NAME" ]] ||
        die "VM $VMID is '$name', not '$VM_NAME' -- refusing to touch it"
}

vm_exists()  { pve "qm status $VMID >/dev/null 2>&1"; }
vm_running() { pve "qm status $VMID 2>/dev/null | grep -q running"; }

vm_destroy() {
    assert_ours
    pve "systemctl stop vos-serial-$VMID 2>/dev/null; \
         if qm status $VMID >/dev/null 2>&1; then \
             qm stop $VMID --skiplock 1 >/dev/null 2>&1 || true; qm destroy $VMID --purge >/dev/null; \
         fi; rm -rf $REMOTE_DIR"
}

# installer_api METHOD PATH [JSON]: call the live ISO's installer API from the
# Proxmox host, which shares the VM's network, with the setup code. Prints the
# response body; fails, showing vosd's error, on an HTTP error.
installer_api() {
    local method=$1 path=$2 body=${3:-}
    pve "curl -sS --fail-with-body -m 30 -X $method http://$VM_IP/api/v1$path \
            -H $(printf %q "X-VOS-Setup: $SETUP_CODE") -H 'Content-Type: application/json' \
            ${body:+-d $(printf %q "$body")}"
}

# Follow the install over the API until it is done, showing each step once.
wait_install() {
    local deadline=$((SECONDS + 1200)) st state step last="" pct
    while ((SECONDS < deadline)); do
        if st=$(installer_api GET /install/status 2>/dev/null); then
            state=$(json_str state <<<"$st")
            step=$(json_str step <<<"$st")
            pct=$(json_lit percent <<<"$st")
            if [[ -n $step && $step != "$last" ]]; then
                printf '    %3s%%  %s\n' "${pct:-?}" "$step"
                last=$step
            fi
            case $state in
                done) return 0 ;;
                failed) fail "install failed: $(json_str error <<<"$st")" ;;
            esac
        fi
        sleep 3
    done
    fail "the install did not finish within 20 minutes"
}

# Create the VM from the ISO, install it through the web installer's API and
# boot the installed system until vosd announces it. Sets VM_VERSION.
vm_install() {
    local m code body want
    want=$(build_version)
    assert_ours
    upload_iso
    if vm_exists; then
        say "Removing previous VM $VMID"
        vm_destroy
    fi

    say "Creating VM $VMID ($VM_NAME)"
    # UEFI only (VaporOS boots through systemd-boot). Secure Boot keys are
    # not pre-enrolled because the kernel is unsigned.
    pve "qm create $VMID \
            --name $VM_NAME \
            --machine q35 --bios ovmf \
            --efidisk0 $DISK_STORAGE:1,efitype=4m,pre-enrolled-keys=0 \
            --cpu host --cores $CORES --memory $MEM \
            --scsihw virtio-scsi-single \
            --scsi0 $DISK_STORAGE:$DISK_SIZE,discard=on,ssd=1 \
            --ide2 $ISO_STORAGE:iso/$ISO_NAME,media=cdrom \
            --boot 'order=scsi0;ide2' \
            --net0 virtio,bridge=$BRIDGE \
            --serial0 socket --vga std \
            --ostype l26 >/dev/null"
    pve "qm start $VMID"
    start_broker

    say "Booting the live ISO"
    m=$(match_ 'VOS-READY mode=installer .*ip=([0-9]+\.[0-9]+\.[0-9]+\.[0-9]+) code=(\S+)' 300) ||
        fail "the live ISO never announced its installer (VOS-READY mode=installer)"
    read -r VM_IP SETUP_CODE <<<"$m"
    ok "installer is up at http://$VM_IP (setup code $SETUP_CODE)"

    if ((TESTING)); then
        screendump installer
        if [[ $SETUP_CODE != - ]]; then
            code=$(pve "curl -s -o /dev/null -w '%{http_code}' -m 15 \
                        -H 'X-VOS-Setup: not-the-code' http://$VM_IP/api/v1/install/probe") || true
            [[ $code == 403 ]] || fail "the installer accepted a wrong setup code (HTTP $code, expected 403)"
            ok "a wrong setup code is refused (403)"
        fi
    fi

    say "Installing through the web installer"
    body=$(printf '{"disk":"/dev/sda","mode":"erase","hostname":"%s","password":"%s","timezone":"%s","libraries":[],"source":""}' \
        "$VM_HOSTNAME" "$ADMIN_PASS" "$TIMEZONE")
    installer_api POST /install "$body" >/dev/null || fail "the installer refused the install"
    wait_install
    if ! expect_ 'VOS-INSTALL state=done' 30; then
        ((TESTING)) && fail "the install finished but vosd never printed 'VOS-INSTALL state=done'"
        warn "no 'VOS-INSTALL state=done' on the serial console"
    fi

    say "Rebooting into the installed system"
    pve "qm set $VMID --ide2 none,media=cdrom >/dev/null"
    mark_
    if ! installer_api POST /install/reboot >/dev/null; then
        warn "POST /install/reboot failed; resetting the VM instead"
        pve "qm reset $VMID"
    fi
    VM_VERSION=$(match_ 'VOS-READY mode=os version=(\S+)' 300) ||
        fail "the installed system never came up (VOS-READY mode=os)"
    [[ $VM_VERSION == "$want" ]] || fail "the installed system runs $VM_VERSION, expected $want"
    dev_settings
}

# The dev VM has no Wake-on-LAN, so an idle shutdown in the middle of a test
# would just switch it off: turn it off through the admin API, the way a
# user would in the web UI.
dev_settings() {
    local login power
    login=$(printf '{"password":"%s"}' "$ADMIN_PASS")
    power='{"idle_shutdown":false}'
    pve "set -e; jar=\$(mktemp); trap 'rm -f \$jar' EXIT
         curl -sS --fail-with-body -m 30 -c \$jar -H 'Content-Type: application/json' \
              -d $(printf %q "$login") http://$VM_IP/api/v1/auth/login >/dev/null
         csrf=\$(curl -sS -m 30 -b \$jar http://$VM_IP/api/v1/auth/me |
                python3 -c 'import json,sys; print(json.load(sys.stdin)[\"csrf\"])')
         curl -sS --fail-with-body -m 30 -b \$jar -X PUT -H \"X-VOS-CSRF: \$csrf\" \
              -H 'Content-Type: application/json' -d $(printf %q "$power") \
              http://$VM_IP/api/v1/power >/dev/null" ||
        warn "could not switch idle shutdown off; the VM may power itself off when idle"
}

# Reboot the VM from its serial shell and wait for vosd to announce the
# installed system again. Prints the version that came up.
vm_reboot() {
    mark_
    send_ "systemctl reboot"
    match_ 'VOS-READY mode=os version=(\S+)' 300
}

# Write the staged build to the idle slot with `vos update [FLAGS]`, reboot
# into it and check that it is the version that came up.
vm_update() {
    local want have
    want=$(build_version)
    say "Updating the VM to $want"
    send_ "vos update $* --from http://$PVE_HOST:$SERVE_PORT/; echo VOS-UPD-RC=\$?"
    rc_is_zero VOS-UPD-RC 900 || fail "vos update failed"
    say "Rebooting into $want"
    have=$(vm_reboot) || fail "the updated system never came up"
    [[ $have == "$want" ]] || fail "VM booted $have, expected $want (did it fall back to the old slot?)"
}

# Run one group of tests/vm-checks.sh in the VM, over the serial shell, and
# show every result. Any failed check fails the run.
vm_checks() {
    local start rc tok=$RANDOM status name detail n=0 url=http://$PVE_HOST:$SERVE_PORT/vm-checks.sh
    ensure_http
    scp -q tests/vm-checks.sh "$PVE_USER@$PVE_HOST:$SERVE_DIR/vm-checks.sh"
    serial_shell 60 >/dev/null || fail "no root shell on the serial console"
    start=$(pve "stat -c %s $LOG")
    # One line: whatever is typed goes to the shell exactly as written.
    send_ "curl -fsS -m 30 -o /tmp/vm-checks.sh $url && bash /tmp/vm-checks.sh$(printf ' %q' "$@"); echo VOS-CHECKS-RC-$tok=\$?"
    rc=$(match_ "VOS-CHECKS-RC-$tok=([0-9]+)" 600) || fail "the checks did not finish"
    while read -r _ status name detail; do
        n=$((n + 1))
        case $status in
            ok) ok "$name: $detail" ;;
            warn) warn "$name: $detail" ;;
            *) printf '\e[1;31m==> %s:\e[0m %s\n' "$name" "$detail" ;;
        esac
    done < <(pve "tail -c +$((start + 1)) $LOG" | tr -d '\r' | grep -ao 'VOS-CHECK [a-zA-Z]* .*' || true)
    ((n > 0)) || fail "vm-checks.sh did not run (exit $rc); can the VM reach http://$PVE_HOST:$SERVE_PORT/?"
    [[ $rc == 0 ]] || die "checks failed ($1)"
}

# ---------------------------------------------------------------- console ---

vnc_port() { echo $(( 5900 + VMID % 100 + 10 )); }

console_open() {
    pgrep -f "ssh .*-L 127.0.0.1:$(vnc_port):" >/dev/null && pgrep -x 'Screen Sharing' >/dev/null
}

# The VM's display in a native macOS Screen Sharing window: Proxmox already
# runs a VNC server per VM on a unix socket, so give it a one-off password and
# tunnel that socket over SSH. No browser, nothing to install.
cmd_console() {
    local port pw
    port=$(vnc_port)
    pw=$(openssl rand -hex 4)
    vm_running || die "VM $VMID is not running -- run 'make' first"
    pve "printf 'set_password vnc %s\nexpire_password vnc never\n' $pw | qm monitor $VMID >/dev/null"

    pkill -f "ssh .*-L 127.0.0.1:$port:" 2>/dev/null || true
    ssh -o LogLevel=ERROR -o ExitOnForwardFailure=yes -f -N \
        -L "127.0.0.1:$port:/var/run/qemu-server/$VMID.vnc" "$PVE_USER@$PVE_HOST"

    say "Opening the VM display (Screen Sharing, localhost:$port)"
    open "vnc://:$pw@127.0.0.1:$port"
}

# ---------------------------------------------------------------- commands --

cmd_dev() {
    local want state have fresh=0 wait=20
    preflight_pve
    build_if_needed
    want=$(build_version)
    assert_ours

    if ! vm_exists; then
        say "No dev VM yet"
        vm_install; fresh=1; wait=60
    elif ! vm_running; then
        say "Starting VM $VMID"
        pve "qm start $VMID"
        start_broker
        wait=240
    else
        ensure_broker
    fi

    # A VM just started needs time to boot; one that is up answers at once.
    if ! state=$(serial_shell $wait); then
        warn "VM $VMID does not answer on its serial console; reinstalling it"
        vm_install; fresh=1
        state=$(serial_shell 60) || fail "no shell after a fresh install"
    fi
    if [[ $state == live ]]; then
        warn "VM $VMID is sitting in the live ISO; reinstalling it"
        vm_install; fresh=1
        state=$(serial_shell 60) || fail "no shell after a fresh install"
    fi

    have=$(vm_version) || fail "could not read the VM's version"
    if [[ $have != "$want" ]]; then
        stage_update
        # --force: the dev VM runs whatever out/ holds, even an older build or
        # one that failed before. Signature and checksums are still checked.
        vm_update --force
        have=$want
    fi
    ok "VM $VMID runs VaporOS $have  (make shell / make console)"

    if [[ $CONSOLE != 0 ]] && { (( fresh )) || ! console_open; }; then
        cmd_console
    fi
}

cmd_reset() {
    preflight_pve
    build_if_needed
    vm_install
    ok "VM $VMID runs VaporOS $VM_VERSION"
    [[ $CONSOLE == 0 ]] || cmd_console
}

# QEMU's serial socket takes one client, so hand it over from the broker for
# the length of the session.
cmd_shell() {
    vm_running || die "VM $VMID is not running -- run 'make' first"
    pve "systemctl stop vos-serial-$VMID 2>/dev/null" || true
    trap start_broker EXIT
    printf '\e[2mserial console of VM %s -- press Enter for a prompt, Ctrl-O to leave\e[0m\n' "$VMID"
    ssh -t -o LogLevel=ERROR "$PVE_USER@$PVE_HOST" "qm terminal $VMID -iface serial0" || true
}

# Install build 1 through the web installer, check it, update to a fresh
# build 2, roll back to 1, then stage 2 again with its health check rigged to
# fail and watch systemd-boot's boot counting bring 1 back. Only ever one VM:
# the test reinstalls the dev VM and leaves it running.
cmd_test() {
    local v1 v2 have entry boots=0
    TESTING=1
    preflight_pve
    build_if_needed
    v1=$(build_version)
    rm -rf out/screens

    vm_install
    ok "installed $v1 through the web installer; it announced itself on serial"
    screendump os-ready

    say "Checking the installed system"
    vm_checks system --slot a --version "$v1" --password "$ADMIN_PASS"
    screendump os-idle

    say "Building a second image to update to"
    rebuild
    v2=$(build_version)
    [[ $v2 != "$v1" ]] || fail "the second build has the same version as the first ($v1)"
    stage_update
    # No --force: the normal acceptance rules (signature, newer, not failed) apply.
    vm_update
    vm_checks booted --slot b --version "$v2" --blessed
    ok "updated $v1 -> $v2 in slot b, and the new entry was blessed"

    say "Rolling back"
    send_ "vos rollback; echo VOS-RB-RC=\$?"
    rc_is_zero VOS-RB-RC 60 || fail "vos rollback failed"
    have=$(vm_reboot) || fail "the VM did not come back after the rollback"
    [[ $have == "$v1" ]] || fail "after the rollback the VM booted $have, expected $v1"
    vm_checks booted --slot a --version "$v1"
    ok "rolled back to $v1 in slot a"

    say "Staging $v2 again, with a health check that always fails"
    # --force because whether a rolled-back version may be staged again
    # without it is not what this part tests.
    send_ "vos update --force --from http://$PVE_HOST:$SERVE_PORT/; echo VOS-UPD-RC=\$?"
    rc_is_zero VOS-UPD-RC 900 || fail "vos update (staging $v2 again) failed"
    entry=/efi/loader/entries/vos-$v2+3.conf
    send_ "ls /efi/loader >/dev/null; sed -i '/^options/s/\$/ vos.health.fail=1/' $entry && grep -q vos.health.fail=1 $entry; echo VOS-HF-RC=\$?"
    rc_is_zero VOS-HF-RC 30 || fail "could not add vos.health.fail=1 to vos-$v2+3.conf"

    mark_
    send_ "systemctl reboot"
    while :; do
        have=$(match_ 'VOS-READY mode=os version=(\S+)' 600) ||
            fail "nothing came up while $v2 was failing its health check"
        [[ $have == "$v1" ]] && break
        [[ $have == "$v2" ]] || fail "unexpected version $have came up"
        boots=$((boots + 1))
        ((boots <= 3)) || fail "$v2 came up more than 3 times; boot counting did not give up on it"
        say "Boot $boots: $v2 is up, fails its health check and reboots"
    done
    ok "$v2 failed its health check and systemd-boot fell back to $v1"
    vm_checks fallback --slot a --version "$v1" --failed "$v2"
    screendump final

    echo
    ok "End-to-end test passed. VM $VMID runs VaporOS $v1 ($v2 is marked failed; 'make' installs it with --force)"
}

cmd_status() {
    preflight_pve
    have_build && echo "build:  $(build_version)" || echo "build:  none"
    if vm_exists; then
        pve "qm status $VMID --verbose | grep -E '^(status|name|uptime)'"
    else
        echo "vm:     none"
    fi
}

cmd_log()     { pve "tail -n +1 -f $LOG"; }
cmd_down()    { assert_ours; pve "qm stop $VMID"; }
cmd_destroy() { vm_destroy; ok "VM $VMID removed"; }

case "${1:-dev}" in
    dev)     cmd_dev ;;
    build)   build_if_needed ;;
    shell)   cmd_shell ;;
    console) cmd_console ;;
    log)     cmd_log ;;
    reset)   cmd_reset ;;
    test)    cmd_test ;;
    status)  cmd_status ;;
    down)    cmd_down ;;
    destroy) cmd_destroy ;;
    send)    shift; send_ "$*" ;;
    expect)  shift; match_ "$@" ;;
    *)       die "unknown command: $1 (see the top of $0)" ;;
esac
