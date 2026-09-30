// pages/storage.js: System › Storage (spec-cc-screens §9). The system
// drive comes from the shell's GET /status; the drives from GET /storage,
// asked once, again on Rescan and passively after a reconnect (no events
// say a drive came or went). Use for games and Stop using ask first (C-ntfs,
// C-stop-using); a drive that can't hold games says why instead of showing
// no button (C10).

import { api, errorText } from '../core/api.js';
import { byId, cloneTpl, h, icon, part, setVar } from '../core/dom.js';
import { onReconnect } from '../core/live.js';
import { region } from '../ui/region.js';
import { notify, onStatus, shell } from '../ui/shell.js';

const fmt = import('../fmt.js');
const dialog = () => import('../ui/dialog.js');
const clipboard = () => import('../ui/clipboard.js');

// storage.LibraryFS: Steam games need Unix permissions and symlinks, so no
// exFAT or FAT. NTFS works through ntfs3, but many games won't start from
// it, so it asks first.
const LIBRARY_FS = /^(ext[234]|btrfs|xfs|f2fs|ntfs3?)$/;
const FS_NAMES = { exfat: 'exFAT', vfat: 'FAT', fat: 'FAT', msdos: 'FAT', iso9660: 'ISO 9660', udf: 'UDF', hfsplus: 'HFS+', apfs: 'APFS', swap: 'Swap', ntfs: 'NTFS', ntfs3: 'NTFS' };
const PENDING = "VaporOS adds it to Steam the next time Steam isn't running, at the latest after a restart. To use it now, add this folder in Steam: Settings → Storage → Add Drive.";
const UNREGISTERED = 'Add this folder once in Steam: Settings → Storage → Add Drive.';
const BTN = /* classes */ { use: 'btn small primary', useQuiet: 'btn small ghost', stop: 'btn small ghost' };

let F = null;
let disks = null;
let loading = null;

const title = (d) => d.label || d.model || d.path || 'This drive';
const fsName = (t) => FS_NAMES[String(t || '').toLowerCase()] || t;

// libraryPath is the folder to add in Steam: the mount point, or the
// library folder inside it (a Windows drive's SteamLibrary, say).
function libraryPath(d) {
  const dir = String(d.library_dir || '').replace(/^\/+|\/+$/g, '');
  return dir && dir !== '.' ? `${d.mounted_at}/${dir}` : d.mounted_at;
}

// meter fills a role=meter: used 0-1, warn at 85 %, danger (cold) at 95 %.
function meter(el, used, text) {
  const f = Math.min(1, Math.max(0, Number(used) || 0));
  setVar(el, '--v', f.toFixed(4));
  el.setAttribute('aria-valuenow', String(Math.round(f * 100)));
  el.setAttribute('aria-valuetext', text);
  el.dataset.level = f >= 0.95 ? 'danger' : f >= 0.85 ? 'warn' : 'ok';
}

// ---------------------------------------------------------- system drive

function renderSystem(sys) {
  const d = sys && sys.disk;
  const box = byId('sysdrive');
  const note = byId('sysdrive-note');
  if (!d || !(d.data_total > 0)) {
    byId('sysdrive-free').textContent = '–';
    byId('sysdrive-of').textContent = '';
    note.textContent = "Couldn't load the system drive's space.";
    note.hidden = false;
  } else {
    const used = 1 - d.data_free / d.data_total;
    const free = F.bytes(d.data_free);
    const total = F.bytes(d.data_total);
    // The number in mono, its unit in the UI face: "612 GB free of 960 GB".
    const [num, unit] = free.split(' ');
    byId('sysdrive-free').replaceChildren(num, h('span', { class: 'sto-free-unit', text: ` ${unit}` }));
    byId('sysdrive-of').textContent = ` free of ${total}`;
    meter(byId('sysdrive-meter'), used, `${free} free of ${total}`);
    note.textContent = used >= 0.95 ? 'Almost out of space. Move games to a game drive, or uninstall some in Steam.' : used >= 0.85 ? 'Space is running low.' : '';
    note.hidden = !note.textContent;
  }
  if (box.hasAttribute('aria-busy')) region(box).ready();
}

// ----------------------------------------------------------------- drives

function badges(d) {
  const out = [];
  const b = (text, tone = '') => out.push(h('li', { class: 'sto-badge', text, dataset: tone ? { tone } : null }));
  if (d.steam_library) b('Steam library');
  if (d.adopted) {
    if (d.missing) b('Not connected', 'cold');
    else if (d.mounted_at) b('Mounted');
    else b('Not mounted', 'hot');
    if (d.registered) b('In Steam', 'ok');
    else if (d.registration_pending) b('Added to Steam soon', 'hot');
  }
  if (d.is_system) b('VaporOS');
  return out;
}

// whyNot is a drive's reason for having no Use for games button.
function whyNot(d) {
  if (!d.uuid) return "Can't be used: it has no filesystem ID.";
  if (!LIBRARY_FS.test(String(d.fstype || '').toLowerCase())) return `${fsName(d.fstype)} can't hold Steam games. Use ext4, btrfs, xfs, f2fs or NTFS.`;
  return '';
}

// recommended: the one drive whose Use for games is white-hot (the first
// that can be used, Steam libraries first); the others are ghost keys.
function driveRow(d, game, i, recommended = false) {
  const li = cloneTpl('tpl-drive');
  li.dataset.uuid = d.uuid || '';
  // One drive glyph for all: every icon in any script is in every page's sprite.
  part(li, 'icon').append(icon('drive'));
  part(li, 'name').textContent = title(d);
  const facts = [fsName(d.fstype), d.mounted_at || !d.size ? '' : F.bytes(d.size), d.model && d.label ? d.model : '', d.path].filter(Boolean);
  part(li, 'facts').textContent = facts.join(' · ');
  part(li, 'facts').hidden = !facts.length;
  const bs = badges(d);
  part(li, 'badges').append(...bs);
  part(li, 'badges').hidden = !bs.length;
  if (d.mounted_at && d.size > 0 && d.free != null) {
    const text = `${F.bytes(d.free)} free of ${F.bytes(d.size)}`;
    const m = part(li, 'meter');
    m.setAttribute('aria-label', `${title(d)} used`);
    meter(m, (d.size - d.free) / d.size, text);
    part(li, 'usage-text').textContent = text;
    part(li, 'usage').hidden = false;
  }
  if (d.mounted_at) {
    const path = part(li, 'path');
    path.id = `folder-${game ? 'g' : 'o'}${i}`;
    const folder = d.adopted ? libraryPath(d) : d.mounted_at;
    // A long path may break after a slash, never inside a name.
    const segs = folder.split('/');
    path.replaceChildren(...segs.flatMap((seg, n) => (n < segs.length - 1 ? [seg, '/', h('wbr')] : [seg])));
    const copy = part(li, 'copy');
    copy.dataset.copyTarget = path.id;
    part(li, 'copy-name').textContent = ` the folder ${folder}`;
    part(li, 'folder').hidden = false;
  }
  const line = part(li, 'line');
  if (game && d.mounted_at && !d.registered) line.textContent = d.registration_pending ? PENDING : UNREGISTERED;
  else if (!game) line.textContent = whyNot(d);
  line.hidden = !line.textContent;
  const acts = part(li, 'actions');
  if (game) {
    acts.append(h('button', { class: BTN.stop, type: 'button', onclick: (e) => stopUsing(d, e.currentTarget) }, h('span', {}, 'Stop using', h('span', { class: 'sr-only', text: ` ${title(d)}` }))));
  } else if (!whyNot(d)) {
    // One span, so the button's gap never opens around the hidden name.
    acts.append(h('button', { class: recommended ? BTN.use : BTN.useQuiet, type: 'button', onclick: (e) => adopt(d, e.currentTarget) }, h('span', {}, 'Use', h('span', { class: 'sr-only', text: ` ${title(d)}` }), ' for games')));
  }
  acts.hidden = !acts.children.length;
  return li;
}

function renderDrives() {
  const games = disks.filter((d) => d.adopted);
  // Steam libraries first: those are what people came here for.
  const others = disks
    .filter((d) => !d.adopted && !d.is_system && d.fstype)
    .sort((a, b) => Number(b.steam_library) - Number(a.steam_library) || title(a).localeCompare(title(b)));
  byId('games').replaceChildren(...games.map((d, i) => driveRow(d, true, i)));
  byId('games').hidden = !games.length;
  byId('games-empty').hidden = games.length > 0;
  // One white-hot key per page (REDLINE): the drive to use first.
  const pick = others.find((d) => !whyNot(d));
  byId('others').replaceChildren(...others.map((d, i) => driveRow(d, false, i, d === pick)));
  byId('others').hidden = !others.length;
  byId('others-empty').hidden = others.length > 0;
  clipboard().then((m) => m.bindCopy(byId('drives')));
  renderSystemDisks();
}

// The running system's partitions, grouped by disk, then any other disk
// with VaporOS on it (an older install, internal/storage/lsblk.go).
function renderSystemDisks() {
  const groups = new Map();
  for (const d of disks.filter((x) => x.is_system)) {
    const key = `${d.mounted_at ? 'run' : 'other'}:${d.parent || d.path}`;
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key).push(d);
  }
  const rows = [...groups.entries()]
    .sort(([a], [b]) => (a < b ? 1 : a > b ? -1 : 0)) // the running one first
    .map(([key, parts]) => {
      const running = key.startsWith('run:');
      const model = parts.find((p) => p.model)?.model || '';
      const head = running ? 'System drive' : 'Another drive with VaporOS on it';
      const facts = [model, parts[0].parent || parts[0].path].filter(Boolean).join(' · ');
      return h('li', { class: 'sto-sysdisk' },
        h('h3', { class: 'sto-sysdisk-name', text: head }),
        facts && h('p', { class: 'sto-drive-facts', text: facts }),
        running
          ? h('ul', { class: 'sto-parts', role: 'list' }, parts.map((p) => h('li', { class: 'sto-part mono', text: [p.label || p.partlabel || p.path, fsName(p.fstype), F.bytes(p.size), p.mounted_at].filter(Boolean).join(' · ') })))
          : h('p', { class: 'sto-line', text: "VaporOS can't use it for games while that install is there." }));
    });
  byId('sysdisks').replaceChildren(...(rows.length ? rows : [h('li', { class: 'sto-sysdisk sto-note', text: 'Not reported.' })]));
}

// load asks GET /storage. rescan: the button shows it, the lists stay.
async function load({ rescan = false, passive = false, focus = '' } = {}) {
  if (loading) return loading;
  const btn = byId('rescan');
  if (rescan) {
    btn.setAttribute('aria-busy', 'true');
    btn.disabled = true;
    byId('rescan-label').textContent = 'Rescanning…';
  }
  loading = (async () => {
    try {
      const [res] = await Promise.all([api('GET', '/storage', undefined, { passive }), fmt.then((m) => (F = m))]);
      disks = Array.isArray(res.disks) ? res.disks : [];
      byId('drives-error').hidden = true;
      renderDrives();
      if (focus) refocus(focus);
    } catch (err) {
      byId('drives-error-text').textContent = `Couldn't list the drives. ${await errorText(err)}`;
      byId('drives-error').hidden = false;
      if (!disks) {
        for (const id of ['games', 'others']) byId(id).hidden = true;
      }
    } finally {
      byId('drives-looking').hidden = true;
      const box = byId('drives');
      if (box.hasAttribute('aria-busy')) region(box).ready();
      btn.removeAttribute('aria-busy');
      btn.disabled = false;
      byId('rescan-label').textContent = 'Rescan';
      loading = null;
    }
  })();
  return loading;
}

// After an action its row moves or goes: focus follows the drive, else
// the list it left.
function refocus(uuid) {
  const li = uuid && document.querySelector(`.sto-drive[data-uuid="${CSS.escape(uuid)}"]`);
  (li ? part(li, 'name') : byId('sto-games-title')).focus();
}

async function ask(id, vars) {
  return (await dialog()).confirmDialog({ id, vars });
}

async function act(btn, fn, vars) {
  btn.setAttribute('aria-busy', 'true');
  btn.disabled = true;
  try {
    await fn();
  } catch (err) {
    notify(await errorText(err, vars), { kind: 'error' });
    btn.removeAttribute('aria-busy');
    btn.disabled = false;
  }
}

async function adopt(d, btn) {
  const label = title(d);
  if (/^ntfs/i.test(d.fstype) && !(await ask('ntfs', { label }))) return;
  await act(btn, async () => {
    const res = await api('POST', '/storage/libraries', { uuid: d.uuid });
    notify(res.hint || `${label} is ready for games.`, { kind: 'ok' });
    await load({ focus: d.uuid });
  }, { label });
}

async function stopUsing(d, btn) {
  const label = title(d);
  if (!(await ask('stop-using', { label }))) return;
  await act(btn, async () => {
    try {
      await api('DELETE', `/storage/libraries/${encodeURIComponent(d.uuid)}`);
      notify(`${label} is no longer used for games.`, { kind: 'ok' });
    } catch (err) {
      if (err.status !== 404) throw err; // already gone: the list says so
    }
    await load({ focus: d.uuid });
  }, { label });
}

shell('storage').then(async () => {
  byId('rescan').addEventListener('click', () => load({ rescan: true }));
  byId('drives-retry').addEventListener('click', () => {
    byId('drives-error').hidden = true;
    byId('drives-looking').hidden = false;
    load();
  });
  onReconnect(() => load({ passive: true }));
  load();
  F = await fmt;
  onStatus((s) => {
    if (s.system || s.update) renderSystem(s.system);
  });
  // The shell's GET /status is in flight: share it to learn if it fails.
  api('GET', '/status', undefined, { share: true }).catch(() => renderSystem(null));
});
