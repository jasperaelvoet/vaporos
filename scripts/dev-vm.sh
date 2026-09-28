#!/usr/bin/env bash
#
# Boot the built ISO in a throwaway VM on a Proxmox host and put its console
# on this desktop.
#
#   ./scripts/dev-vm.sh fetch     download the newest ISO built by CI
#   ./scripts/dev-vm.sh up        upload the ISO, (re)create the VM, boot it
#   ./scripts/dev-vm.sh console   reopen the console
#   ./scripts/dev-vm.sh status    show VM state
#   ./scripts/dev-vm.sh down      stop the VM
#   ./scripts/dev-vm.sh destroy   stop and delete the VM and its disks
#
set -euo pipefail
cd "$(dirname "$0")/.."

PVE_HOST=${PVE_HOST:-192.168.1.2}
PVE_USER=${PVE_USER:-root}
PVE_NODE=${PVE_NODE:-proxmox}
VMID=${VMID:-9000}
VM_NAME=${VM_NAME:-watervaporos-dev}
ISO_STORAGE=${ISO_STORAGE:-local}
DISK_STORAGE=${DISK_STORAGE:-local-lvm}
BRIDGE=${BRIDGE:-vmbr0}
MEM=${MEM:-4096}
CORES=${CORES:-4}
DISK_SIZE=${DISK_SIZE:-32}
OUT=${OUT:-out}

pve() { ssh "$PVE_USER@$PVE_HOST" "$@"; }
say() { printf '\e[1;34m==>\e[0m \e[1m%s\e[0m\n' "$*"; }
die() { printf '\e[1;31merror:\e[0m %s\n' "$*" >&2; exit 1; }

local_iso() {
    local iso
    iso=$(ls -t "$OUT"/*.iso 2>/dev/null | head -1) || true
    [[ -n $iso ]] || die "no ISO in $OUT/ -- run '$0 fetch' or ./scripts/build-iso.sh"
    printf '%s' "$iso"
}

# Refuse to touch a VMID that is not ours, so a typo cannot delete a real VM.
assert_ours() {
    local name
    name=$(pve "qm config $VMID 2>/dev/null | sed -n 's/^name: //p'") || true
    [[ -z $name || $name == "$VM_NAME" ]] ||
        die "VM $VMID is '$name', not '$VM_NAME' -- refusing to touch it"
}

cmd_fetch() {
    command -v gh >/dev/null || die "gh CLI not installed"
    say "Downloading newest ISO artifact from CI"
    mkdir -p "$OUT"
    local run
    run=$(gh run list --workflow build --status success --limit 1 --json databaseId -q '.[0].databaseId')
    [[ -n $run ]] || die "no successful build run yet"
    gh run download "$run" --name watervaporos-iso --dir "$OUT"
    ls -lh "$OUT"/*.iso
}

cmd_up() {
    local iso remote_iso
    iso=$(local_iso)
    remote_iso="$(basename "$iso")"
    assert_ours

    say "Uploading $remote_iso to $PVE_HOST:$ISO_STORAGE"
    # Only re-upload when the remote copy differs; these are large files.
    local want have
    want=$(wc -c < "$iso" | tr -d ' ')
    have=$(pve "stat -c %s /var/lib/vz/template/iso/$remote_iso 2>/dev/null" || echo 0)
    if [[ $want == "$have" ]]; then
        say "Already present on the host, skipping upload"
    else
        scp -q "$iso" "$PVE_USER@$PVE_HOST:/var/lib/vz/template/iso/$remote_iso"
    fi

    if pve "qm status $VMID >/dev/null 2>&1"; then
        say "Removing previous VM $VMID"
        pve "qm stop $VMID >/dev/null 2>&1 || true; qm destroy $VMID --purge"
    fi

    say "Creating VM $VMID ($VM_NAME)"
    # UEFI is mandatory: bootc installs a systemd-boot ESP and will not come up
    # under SeaBIOS. pre-enrolled-keys=0 keeps Secure Boot from rejecting the
    # unsigned Arch kernel.
    pve "qm create $VMID \
            --name $VM_NAME \
            --machine q35 \
            --bios ovmf \
            --efidisk0 $DISK_STORAGE:1,efitype=4m,pre-enrolled-keys=0 \
            --cpu host \
            --cores $CORES \
            --memory $MEM \
            --scsihw virtio-scsi-single \
            --scsi0 $DISK_STORAGE:$DISK_SIZE,discard=on,ssd=1 \
            --ide2 $ISO_STORAGE:iso/$remote_iso,media=cdrom \
            --boot order='ide2;scsi0' \
            --net0 virtio,bridge=$BRIDGE \
            --vga std \
            --ostype l26 \
            --agent 1"

    say "Starting VM"
    pve "qm start $VMID"
    cmd_console
}

cmd_console() {
    # Proxmox's own noVNC console. It renders the firmware and bootloader too,
    # which a serial console would miss, and needs nothing installed locally.
    local url="https://$PVE_HOST:8006/?console=kvm&novnc=1&vmid=$VMID&node=$PVE_NODE&resize=off"
    say "Opening console: $url"
    open "$url" 2>/dev/null || printf '%s\n' "$url"
}

cmd_status()  { pve "qm status $VMID; qm config $VMID"; }
cmd_down()    { assert_ours; pve "qm stop $VMID"; }
cmd_destroy() { assert_ours; pve "qm stop $VMID >/dev/null 2>&1 || true; qm destroy $VMID --purge"; }

case "${1:-up}" in
    fetch)   cmd_fetch ;;
    up)      cmd_up ;;
    console) cmd_console ;;
    status)  cmd_status ;;
    down)    cmd_down ;;
    destroy) cmd_destroy ;;
    *)       die "unknown command: $1" ;;
esac
