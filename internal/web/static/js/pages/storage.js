// Storage: adopt other drives as Steam libraries, or stop using them.
import {
  boot, api, onReconnect, byId, h, fill, icon, badge, toast, busy, confirmDialog, bytes, percent,
} from '../lib.js';

// Filesystems Linux games run from reliably. Windows filesystems still mount,
// but Proton trips over their permissions, so adopting one asks first.
const WINDOWS_FS = new Set(['ntfs', 'ntfs3', 'exfat', 'vfat', 'fat32']);

let disks = [];

async function load() {
  try {
    disks = (await api('GET', '/storage')).disks || [];
  } catch (e) {
    toast(`Couldn't load drives: ${e.message}`, 'error');
    return;
  }
  render();
}

const title = (d) => d.label || d.model || d.path;

function usage(d) {
  if (!d.size || d.free == null || !d.mounted_at) return null;
  const pct = percent(((d.size - d.free) / d.size) * 100);
  const bar = h('progress', { class: `usage-bar${pct >= 95 ? ' full' : pct >= 85 ? ' high' : ''}`, max: '100', 'aria-label': `${title(d)} used` });
  bar.value = pct;
  return h('div', { class: 'usage' }, bar, h('span', { class: 'list-sub', text: `${bytes(d.free)} free of ${bytes(d.size)}` }));
}

function item(d, actions, iconName = 'drive') {
  const facts = [d.fstype, d.mounted_at ? '' : bytes(d.size), d.model && d.label ? d.model : '', d.path].filter(Boolean);
  const tags = [];
  if (d.steam_library) tags.push(badge('Steam library', 'ok'));
  if (d.adopted) tags.push(badge('Mounted for Steam', 'accent'));
  if (d.is_system) tags.push(badge('VaporOS'));
  return h('li', { class: 'list-item top wrap-sm' },
    h('span', { class: 'list-icon' }, icon(iconName)),
    h('div', { class: 'list-main' },
      h('span', { class: 'list-title', text: title(d) }),
      h('span', { class: 'list-sub', text: facts.join(' · ') }),
      d.mounted_at ? h('span', { class: 'list-sub mono', text: d.mounted_at }) : null,
      usage(d),
      tags.length ? h('span', { class: 'chips' }, tags) : null),
    actions.length ? h('div', { class: 'list-actions' }, actions) : null);
}

function adoptButton(d) {
  const b = h('button', { class: 'btn btn-primary btn-sm', type: 'button', 'aria-label': `Use ${title(d)} for games` }, icon('plus'), 'Use for games');
  b.addEventListener('click', async () => {
    if (WINDOWS_FS.has(String(d.fstype).toLowerCase())) {
      const ok = await confirmDialog({
        title: `${title(d)} is a Windows drive`,
        body: `Its filesystem (${d.fstype}) works for storing files, but many games won't start from it. Use it anyway?`,
        confirm: 'Use anyway',
      });
      if (!ok) return;
    }
    await busy(b, async () => {
      await api('POST', '/storage/libraries', { uuid: d.uuid });
      await load();
      const now = disks.find((x) => x.uuid === d.uuid);
      const where = now && now.mounted_at ? now.mounted_at : `/var/mnt/${d.label || d.uuid}`;
      toast(`${title(d)} is ready. In Steam, add ${where} under Settings → Storage.`, 'ok');
    });
  });
  return b;
}

function removeButton(d) {
  const b = h('button', { class: 'btn btn-danger btn-sm', type: 'button', 'aria-label': `Stop using ${title(d)}` }, icon('close'), 'Stop using');
  b.addEventListener('click', async () => {
    const ok = await confirmDialog({
      title: `Stop using ${title(d)}?`,
      body: "VaporOS stops mounting it. The games on it stay on the drive, but Steam can't see them until you add it again.",
      confirm: 'Stop using',
      danger: true,
    });
    if (!ok) return;
    await busy(b, async () => {
      await api('DELETE', `/storage/libraries/${encodeURIComponent(d.uuid)}`);
      toast(`${title(d)} is no longer used for games.`, 'ok');
      await load();
    });
  });
  return b;
}

function render() {
  const libs = disks.filter((d) => d.adopted);
  const others = disks.filter((d) => !d.adopted && !d.is_system && d.fstype);
  const system = disks.filter((d) => d.is_system && d.fstype);

  fill(byId('libraries'), libs.map((d) => item(d, [removeButton(d)], 'library')));
  byId('libraries').hidden = !libs.length;
  byId('libraries-empty').hidden = libs.length > 0;

  // Steam libraries first: those are what people came here for.
  others.sort((a, b) => Number(b.steam_library) - Number(a.steam_library) || title(a).localeCompare(title(b)));
  fill(byId('others'), others.map((d) => item(d, d.uuid ? [adoptButton(d)] : [])));
  byId('others').hidden = !others.length;
  byId('others-empty').hidden = others.length > 0;

  fill(byId('system-disks'), system.length
    ? system.map((d) => item(d, [], 'shield'))
    : h('li', { class: 'list-item muted', text: 'Not reported.' }));
}

async function main() {
  await boot('storage');
  const refresh = byId('storage-refresh');
  refresh.addEventListener('click', () => busy(refresh, load));
  onReconnect(load);
  await load();
}

main();
