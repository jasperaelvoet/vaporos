// pages/install-state.js: the installer wizard's state (spec-cc-screens
// §15), shared by pages/install.js (the steps) and pages/install-run.js
// (the install): what the probe found, what the visitor chose, and the
// derived facts both need. The choices live in this tab's sessionStorage,
// so a reload keeps them (C9); the password never does.

import { byId } from '../core/dom.js';
import { getJSON, setJSON } from '../core/store.js';
import { cleanHostname } from '../validate.js';

// The smallest drive the A/B layout fits on (512 MiB ESP, two 8 GiB slots,
// 8 GiB of data; CONTRACTS.md "Disk layout"), for a probe without min_size.
const MIN_DISK_FLOOR = 512 * 2 ** 20 + 3 * 8 * 2 ** 30;
const SAVED = 'vos-install-wizard';

export const TRANSPORTS = { nvme: 'NVMe', sata: 'SATA', ata: 'SATA', usb: 'USB', mmc: 'SD card', scsi: 'SCSI', virtio: 'Virtual' };

// w is the wizard.
export const w = {
  step: '',
  probe: null,
  disk: null, // the chosen drive from the probe
  diskPath: '', // its path, kept across a reload
  mode: 'erase',
  modeChosen: false, // the visitor picked erase or repair themselves
  hostname: 'vapor', // '' in repair: the installed system keeps its own
  password: '', // in this page only
  timezone: '',
  libraries: new Set(), // filesystem UUIDs to adopt
  librariesSeen: new Set(), // offered once already
  finished: false,
};

export function save() {
  setJSON('session', SAVED, {
    disk: w.disk ? w.disk.path : w.diskPath,
    mode: w.mode,
    modeChosen: w.modeChosen,
    hostname: byId('hostname').value,
    timezone: w.timezone,
    libraries: [...w.libraries],
    librariesSeen: [...w.librariesSeen],
  });
}

export function restore() {
  const s = getJSON('session', SAVED);
  if (!s || typeof s !== 'object') return;
  w.diskPath = typeof s.disk === 'string' ? s.disk : '';
  w.mode = s.mode === 'repair' ? 'repair' : 'erase';
  w.modeChosen = !!s.modeChosen;
  if (typeof s.hostname === 'string' && s.hostname) byId('hostname').value = s.hostname;
  w.hostname = w.mode === 'repair' ? '' : cleanHostname(byId('hostname').value);
  w.timezone = typeof s.timezone === 'string' ? s.timezone : '';
  w.libraries = new Set(Array.isArray(s.libraries) ? s.libraries : []);
  w.librariesSeen = new Set(Array.isArray(s.librariesSeen) ? s.librariesSeen : []);
}

// forget drops the choices, once the installed system has taken over.
export const forget = () => setJSON('session', SAVED, null);

// bytes is fmt.js's decimal size, here so the first paint needs no fmt.js.
export function bytes(n) {
  const units = ['B', 'kB', 'MB', 'GB', 'TB'];
  let i = 0;
  n = Number(n) || 0;
  while (n >= 1000 && i < units.length - 1) {
    n /= 1000;
    i += 1;
  }
  return `${n.toFixed(i > 0 && n < 10 ? 1 : 0)} ${units[i]}`;
}

export const gib = (n) => `${(n / 2 ** 30).toFixed(1).replace(/\.0$/, '')} GiB`;
export const usable = () => (w.probe?.disks || []).filter((d) => !d.is_live);
export const minSize = () => (Number(w.probe?.min_size) > 0 ? Number(w.probe.min_size) : MIN_DISK_FLOOR);
export const fits = (d) => Number(d.size) >= minSize();
export const diskLabel = (d) => `${d.model || d.path} (${bytes(d.size)})`;

// offeredLibraries are the Steam libraries on the other drives: the chosen
// drive's are about to be overwritten or are VaporOS's own.
export function offeredLibraries() {
  const out = [];
  for (const d of usable()) {
    if (d.path === w.disk?.path) continue;
    for (const lib of d.steam_libraries || []) out.push({ ...lib, model: d.model, size: d.size });
  }
  return out;
}

export const chosenLibraries = () => offeredLibraries().filter((lib) => w.libraries.has(lib.uuid));

// libraryFolder is where an adopted library appears on the installed
// system: /var/mnt/<label> (made safe as install/target.go does, else the
// UUID) plus the folder inside it.
function libraryFolder(lib) {
  const name = String(lib.label || '').replace(/[^A-Za-z0-9._-]/g, '_').replace(/^\.+/, '');
  const inside = !lib.path || lib.path === '/' ? '' : `/${String(lib.path).replace(/^\/+/, '')}`;
  return `/var/mnt/${name || lib.uuid}${inside}`;
}

export const folders = () => [...new Set(chosenLibraries().map(libraryFolder))];
