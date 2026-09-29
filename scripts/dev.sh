#!/usr/bin/env bash
#
# The VaporOS dev loop. Builds happen on this machine; the Proxmox host only
# runs the VM and serves the built image to it.
#
#   build     build out/ if anything changed since the last build
#   dev       build if anything changed, then bring the dev VM to that build:
#             create + install it, or `vos update` + reboot it. The default.
#   shell     interactive serial shell in the VM (leave with Ctrl-O)
#   console   open the VM's display in a native Screen Sharing window
#   log       follow the VM's serial console
#   reset     wipe the dev VM and install it from scratch
#   test      end-to-end check on a separate throwaway VM
#   status | down | destroy
#   send TXT | expect REGEX [TIMEOUT]    drive the serial console by hand
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
TEST_VMID=${TEST_VMID:-9001}
ISO_STORAGE=${ISO_STORAGE:-local}
ISO_DIR=${ISO_DIR:-/var/lib/vz/template/iso}
DISK_STORAGE=${DISK_STORAGE:-local-lvm}
BRIDGE=${BRIDGE:-vmbr0}
MEM=${MEM:-2048}
CORES=${CORES:-4}
DISK_SIZE=${DISK_SIZE:-32}
SERVE_DIR=${SERVE_DIR:-/var/lib/vz/vos-dev}
SERVE_PORT=${SERVE_PORT:-8000}
CONSOLE=${CONSOLE:-1}
# The dev user the automatic install creates.
DEV_USER=vapor
DEV_PASS=vapor

ISO_NAME=vaporos-dev.iso

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

# ------------------------------------------------------------- preflight ----

preflight_pve() {
    ssh -o BatchMode=yes -o ConnectTimeout=5 -o LogLevel=ERROR \
        "$PVE_USER@$PVE_HOST" true 2>/dev/null ||
        die "cannot ssh to $PVE_USER@$PVE_HOST without a password. Run once:  ssh-copy-id $PVE_USER@$PVE_HOST"
    pve "command -v qm >/dev/null && command -v python3 >/dev/null" ||
        die "$PVE_HOST is not a Proxmox host with python3"
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
inputs_hash() {
    local files
    files=$(find rootfs build packages.txt -type f ! -name .DS_Store | sort)
    { tr '\n' '\0' <<<"$files" | xargs -0 stat -f '%p %N'
      tr '\n' '\0' <<<"$files" | xargs -0 shasum -a 256; } | shasum -a 256 | cut -c1-16
}

have_build() { [[ -f out/manifest.env && -f out/root.erofs && -n $(ls out/*.iso 2>/dev/null) ]]; }

build_version() { sed -n 's/^VOS_VERSION=//p' out/manifest.env; }

build_if_needed() {
    local want
    want=$(inputs_hash)
    if have_build && [[ -f out/.inputs && $(<out/.inputs) == "$want" ]]; then
        ok "Build $(build_version) is current"
        return
    fi
    preflight_docker
    say "Building"
    rm -f out/.inputs
    ./scripts/build.sh
    echo "$want" >out/.inputs
}

local_iso() {
    local iso
    iso=$(ls -t out/*.iso 2>/dev/null | head -1) || true
    [[ -n $iso ]] || die "no ISO in out/"
    printf '%s' "$iso"
}

# rsync with a delta against what is already there: consecutive builds share
# most of their bytes, so this usually sends a fraction of the file.
push() { rsync -t --inplace --partial "$1" "$PVE_USER@$PVE_HOST:$2"; }

upload_iso() {
    local iso
    iso=$(local_iso)
    say "Uploading $(basename "$iso") to $PVE_HOST"
    # Older tooling kept one versioned ISO per build.
    pve "rm -f $ISO_DIR/watervaporos-*.iso $ISO_DIR/vaporos-2*.iso"
    push "$iso" "$ISO_DIR/$ISO_NAME"
}

# Put the update payload on the Proxmox host and serve it, so the VM can
# always reach it no matter which network or firewall this machine is on.
stage_update() {
    say "Staging $(build_version) for update"
    pve "mkdir -p $SERVE_DIR"
    local f
    for f in root.erofs vmlinuz initramfs.img manifest.env; do
        push "out/$f" "$SERVE_DIR/$f"
    done
    pve "systemctl is-active --quiet vos-dev-http ||
         systemd-run --quiet --collect --unit vos-dev-http -p WorkingDirectory=$SERVE_DIR \
             python3 -m http.server --bind $PVE_HOST $SERVE_PORT"
}

# ---------------------------------------------------------------- serial ----

start_broker() {
    pve "systemctl stop vos-serial-$VMID 2>/dev/null; rm -rf $REMOTE_DIR; mkdir -p $REMOTE_DIR"
    scp -q scripts/serial.py "$PVE_USER@$PVE_HOST:$REMOTE_DIR/serial.py"
    pve "systemd-run --quiet --collect --unit vos-serial-$VMID \
            python3 $REMOTE_DIR/serial.py broker /var/run/qemu-server/$VMID.serial0 $LOG $FIFO"
}

ensure_broker() {
    if pve "systemctl is-active --quiet vos-serial-$VMID"; then
        # The running broker keeps its code; the helpers must match this checkout.
        scp -q scripts/serial.py "$PVE_USER@$PVE_HOST:$REMOTE_DIR/serial.py"
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

# Get to a shell prompt on the serial console, logging in if needed. Prints
# "shell" (installed system, logged in) or "live" (the ISO's root shell).
serial_shell() {
    local timeout=${1:-20} m tok=$RANDOM
    mark_
    send_ ""
    m=$(match_ "($VM_HOSTNAME login:|$DEV_USER@$VM_HOSTNAME|root@vapor-live)" "$timeout" 2>/dev/null) || return 1
    case $m in
        root@vapor-live) echo live; return ;;
        *login:)
            send_ "$DEV_USER";  expect_ 'Password:' 30 2>/dev/null || return 1
            send_ "$DEV_PASS";  expect_ "$DEV_USER@$VM_HOSTNAME" 60 2>/dev/null || return 1 ;;
    esac
    # The quotes keep the typed-in command from matching its own output.
    send_ "echo VOS\"\"-PING-$tok"
    expect_ "VOS-PING-$tok" 20 2>/dev/null || return 1
    echo shell
}

vm_version() {
    send_ 'echo VOS""-VER=$(. /etc/os-release; echo $IMAGE_VERSION)'
    match_ 'VOS-VER=([0-9.]+)' 20 2>/dev/null
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

# Create the VM from the ISO, install to its disk and boot the installed
# system to a login prompt. Entirely over the serial console.
vm_install() {
    assert_ours
    upload_iso
    if vm_exists; then
        say "Removing previous VM $VMID"
        vm_destroy
    fi

    say "Creating VM $VMID ($VM_NAME)"
    # UEFI only (VaporOS boots through systemd-boot). Secure Boot keys are
    # not pre-enrolled because the Arch kernel is unsigned.
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
    expect_ 'root@vapor-live' 180 || fail "live ISO never reached a shell"
    say "Installing"
    send_ "vos install --disk /dev/sda --hostname $VM_HOSTNAME --user $DEV_USER --password $DEV_PASS --yes; echo VOS-INSTALL-RC=\$?"
    rc_is_zero VOS-INSTALL-RC 600 || fail "install failed"

    say "Rebooting into the installed system"
    pve "qm set $VMID --ide2 none,media=cdrom >/dev/null"
    send_ "systemctl reboot"
    expect_ "$VM_HOSTNAME login:" 240 || fail "installed system never reached a login prompt"
}

# Write the staged build to the idle slot and reboot into it.
vm_update() {
    say "Updating the VM to $(build_version)"
    send_ "echo $DEV_PASS | sudo -S -p '' vos update --from http://$PVE_HOST:$SERVE_PORT; echo VOS-UPD-RC=\$?"
    rc_is_zero VOS-UPD-RC 600 || fail "vos update failed"
    say "Rebooting into $(build_version)"
    send_ "echo $DEV_PASS | sudo -S -p '' systemctl reboot"
    expect_ "$VM_HOSTNAME login:" 240 || fail "updated system never reached a login prompt"
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
    else
        if ! vm_running; then
            say "Starting VM $VMID"
            pve "qm start $VMID"
            start_broker
            wait=240
        else
            ensure_broker
        fi
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
        vm_update
        serial_shell 60 >/dev/null || fail "could not log in after the update"
        have=$(vm_version) || fail "could not read the VM's version"
        [[ $have == "$want" ]] || fail "VM booted $have, expected $want (did it fall back to the old slot?)"
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
    serial_shell 60 >/dev/null || fail "no shell after a fresh install"
    ok "VM $VMID runs VaporOS $(vm_version)"
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

cmd_test() {
    local user=$DEV_USER pass=$DEV_PASS
    preflight_pve
    build_if_needed
    use_vm "$TEST_VMID" vaporos-test vaportest
    vm_install
    ok "live ISO booted and installed"

    serial_shell 60 >/dev/null || fail "could not log in"
    ok "installed system booted, logged in as $user"

    say "Checking the running system"
    send_ "echo $pass | sudo -S true 2>/dev/null; echo; findmnt -no FSTYPE,OPTIONS / | cut -c1-40; echo CHECK-ROOT=\$(findmnt -no FSTYPE /)"
    expect_ 'CHECK-ROOT=erofs' 30 || fail "/ is not the erofs image"
    ok "/ is the read-only erofs image"
    send_ "sudo touch /usr/bin/x 2>/dev/null; echo CHECK-RO=\$?"
    expect_ 'CHECK-RO=1' 30 || fail "/usr was writable"
    ok "/usr refuses writes"
    send_ "echo CHECK-ETC=\$(findmnt -no FSTYPE /etc) CHECK-VAR=\$(findmnt -no SOURCE /var | head -1)"
    expect_ 'CHECK-ETC=overlay CHECK-VAR=/dev/sda4' 30 || fail "/etc or /var not wired to the data partition"
    ok "/etc overlay and /var on the data partition"
    send_ "systemctl is-system-running --wait; echo CHECK-STATE=\$(systemctl is-system-running)"
    expect_ 'CHECK-STATE=(running|degraded)' 120 || true
    send_ "systemctl --failed --no-legend | cat; echo CHECK-FAILED=\$(systemctl --failed --no-legend | wc -l)"
    expect_ 'CHECK-FAILED=0' 30 || warn "some units failed (see: make log)"
    send_ "sudo vos status"
    expect_ 'slot a:.*running' 30 || fail "vos status did not report slot a running"
    ok "vos status reports slot a"

    say "Removing test VM $VMID"
    vm_destroy
    echo
    ok "End-to-end test passed."
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
