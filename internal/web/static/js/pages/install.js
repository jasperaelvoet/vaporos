// The web installer (live ISO). Steps: setup code (only when needed) →
// drive → name and password → games and time zone → confirm → progress →
// done → restart, after which the page finds the installed system at
// http://<hostname>.local (or the same address) and moves there.
import {
  boot, api, on, connectEvents, byId, $, $$, h, fill, icon, badge, kv, setText, showError, onSubmit,
  invalid, busy, sleep, initSetupCode, useSetupCode, normalizeCode, bytes, hostnameError,
  passwordError, percent, phaseLabel, ApiError,
} from '../lib.js';

// The smallest drive that can hold the A/B layout: 512 MiB ESP, two slots of
// at least 8 GiB and 8 GiB of data (CONTRACTS.md "Disk layout"). The probe
// says what the image being installed really needs (min_size); this floor
// is only for a probe that does not.
const MIN_DISK_FLOOR = 512 * 2 ** 20 + 2 * 8 * 2 ** 30 + 8 * 2 ** 30;
const INPUT_STEPS = ['disk', 'account', 'extras', 'confirm'];
const HOST_KEY = 'vos-install-host';

const w = {
  step: '',
  probe: null,
  disk: null,
  mode: 'erase',
  modeChosen: false, // the visitor picked erase/repair themselves
  hostname: 'vapor', // '' in repair: the installed system keeps its own
  password: '',
  timezone: '',
  libraries: new Set(), // UUIDs to adopt
  librariesSeen: new Set(), // UUIDs already offered once
  polling: false,
  finished: false,
};

// ---------- navigation ----------

function go(step) {
  w.step = step;
  for (const s of $$('.step', byId('wizard'))) s.hidden = s.dataset.step !== step;
  const idx = INPUT_STEPS.indexOf(step);
  const stepper = byId('stepper');
  stepper.classList.toggle('finished', idx < 0);
  for (const li of $$('li', stepper)) {
    const i = INPUT_STEPS.indexOf(li.dataset.step);
    li.dataset.state = i < idx ? 'done' : i === idx ? 'current' : '';
    if (i === idx) li.setAttribute('aria-current', 'step');
    else li.removeAttribute('aria-current');
  }
  byId('wizard').scrollIntoView({ block: 'start' });
  const heading = $(`.step[data-step="${step}"] h2`);
  if (heading) heading.focus({ preventScroll: true });
}

function back() {
  const i = INPUT_STEPS.indexOf(w.step);
  if (i > 0) go(INPUT_STEPS[i - 1]);
}

// ---------- step: setup code ----------

// verify checks the current code by asking for the install status, which
// needs Setup access. It returns the status, or null on a wrong code.
async function verify() {
  try {
    return await api('GET', '/install/status');
  } catch (e) {
    if (e instanceof ApiError && e.status === 403) return null;
    throw e;
  }
}

async function submitCode() {
  const field = byId('setup-code');
  const code = normalizeCode(field.value);
  field.value = code;
  useSetupCode(code);
  showError('code-error', '');
  if (!(await verify())) {
    showError('code-error', "That code isn't right. Check the screen connected to the PC.");
    field.select();
    return;
  }
  // Reload through /setup?code= so the server can also set the setup
  // cookie, which the live progress stream needs.
  location.replace(`/setup?code=${encodeURIComponent(code)}`);
}

// ---------- step: drive ----------

const TRANSPORTS = { nvme: 'NVMe', sata: 'SATA', ata: 'SATA', usb: 'USB', mmc: 'SD card', scsi: 'SCSI', virtio: 'Virtual' };

// askForCode goes back to the code step when a Setup call is refused
// mid-wizard: the code waiver for a PC without a monitor ends as soon as
// one is plugged in. An install already running carries on regardless.
function askForCode(e) {
  if (!(e instanceof ApiError && e.status === 403)) return false;
  w.finished = true; // stops polling; the reload after the code resumes it
  window.removeEventListener('beforeunload', leaveGuard);
  go('code');
  showError('code-error', "Enter the setup code shown on the PC's screen to continue.");
  return true;
}

async function probe(btn = null) {
  const run = async () => {
    try {
      w.probe = await api('GET', '/install/probe');
    } catch (e) {
      if (askForCode(e)) return false;
      throw e;
    }
    renderGPU();
    renderDisks();
    return true;
  };
  return btn ? busy(btn, run) : run();
}

function usableDisks() {
  return ((w.probe && w.probe.disks) || []).filter((d) => !d.is_live);
}

function minDisk() {
  const n = Number(w.probe && w.probe.min_size);
  return Number.isFinite(n) && n > 0 ? n : MIN_DISK_FLOOR;
}

const gib = (n) => `${(n / 2 ** 30).toFixed(1).replace(/\.0$/, '')} GiB`;

function renderGPU() {
  const gpu = (w.probe && w.probe.gpu) || {};
  const note = byId('gpu-note');
  note.hidden = false;
  if (gpu.supported) {
    note.className = 'banner banner-ok';
    fill(byId('gpu-text'), h('strong', { text: gpu.name || 'Graphics card found' }), ' is supported for streaming.');
  } else {
    note.className = 'banner banner-warn';
    fill(byId('gpu-text'), h('strong', { text: gpu.name ? `${gpu.name} isn't supported yet.` : 'No supported graphics card found.' }),
      ' VaporOS installs, but streaming needs an AMD Radeon GPU.');
  }
}

function renderDisks() {
  const disks = usableDisks();
  const min = minDisk();
  const fits = disks.filter((d) => Number(d.size) >= min);
  if (w.disk && !fits.some((d) => d.path === w.disk.path)) w.disk = null;
  if (!w.disk && fits.length === 1) w.disk = fits[0];
  setText('disk-min', `${gib(min)} (about ${bytes(min)})`);
  fill(byId('disk-list'), disks.map((d) => {
    const tooSmall = Number(d.size) < min;
    const input = h('input', { type: 'radio', name: 'disk', value: d.path, disabled: tooSmall });
    input.checked = !!w.disk && w.disk.path === d.path;
    input.addEventListener('change', () => {
      w.disk = d;
      showError('disk-error', '');
    });
    const chips = [];
    if (d.has_vaporos) chips.push(badge('VaporOS installed', 'accent'));
    for (const lib of d.steam_libraries || []) chips.push(badge(`Steam library${lib.label ? `: ${lib.label}` : ''}`, 'ok'));
    if (d.removable) chips.push(badge('Removable'));
    if (tooSmall) chips.push(badge(`Too small (needs ${gib(min)})`, 'warn'));
    const facts = [bytes(d.size), TRANSPORTS[String(d.transport || '').toLowerCase()] || d.transport, d.path].filter(Boolean);
    return h('label', { class: 'choice' }, input,
      h('span', { class: 'choice-body' },
        h('span', { class: 'choice-icon' }, icon(d.transport === 'usb' ? 'usb' : 'drive')),
        h('span', { class: 'choice-main' },
          h('strong', { text: d.model || d.path }),
          h('span', { class: 'muted', text: facts.join(' · ') }),
          chips.length ? h('span', { class: 'chips' }, chips) : null)));
  }));
  byId('disk-empty').hidden = disks.length > 0;
}

function diskNext() {
  if (!w.disk) {
    showError('disk-error', 'Choose the drive to install on.');
    return;
  }
  const hasVapor = !!w.disk.has_vaporos;
  byId('mode-field').hidden = !hasVapor;
  // Repair is the safe default on a drive that already has VaporOS.
  let mode = 'erase';
  if (hasVapor) mode = w.modeChosen && w.mode === 'erase' ? 'erase' : 'repair';
  for (const r of $$('input[name="mode"]')) r.checked = r.value === mode;
  w.mode = mode;
  updateModeFields();
  go('account');
}

// ---------- step: name and password ----------

// updateModeFields adapts the form to erase or repair. A repair keeps the
// installed system's name and time zone: the wizard sends neither, and the
// installer leaves /etc/hostname and /etc/localtime alone.
function updateModeFields() {
  const repair = w.mode === 'repair';
  byId('password').required = !repair;
  byId('password2').required = !repair;
  setText('password-hint', repair
    ? 'Leave empty to keep the current password, or set a new one (at least 8 characters).'
    : "At least 8 characters. You'll use it to sign in to this page.");
  byId('repair-keeps').hidden = !repair;
  byId('hostname-field').hidden = repair;
  byId('hostname').required = !repair;
  byId('timezone-field').hidden = repair;
}

function updateHostPreview() {
  const field = byId('hostname');
  const v = field.value.trim().toLowerCase();
  if (v !== field.value) field.value = v;
  setText('host-preview', `http://${v || 'vapor'}.local`);
}

function submitAccount() {
  const repair = w.mode === 'repair';
  const host = byId('hostname');
  const p1 = byId('password');
  const p2 = byId('password2');
  if (!repair) {
    const herr = hostnameError(host.value.trim());
    if (herr) return invalid(host, herr);
  }
  const perr = passwordError(p1.value, p2.value, { optional: repair });
  if (perr) return invalid(p1.value.length < 8 ? p1 : p2, perr);
  w.hostname = repair ? '' : host.value.trim();
  w.password = p1.value;
  renderExtras();
  go('extras');
  return true;
}

// ---------- step: games and time ----------

// guessTimezone prefers what the live system knows, but the ISO has no
// zone of its own and reports UTC; the browser's zone is then the better
// guess, as long as the list offers it.
function guessTimezone(sel) {
  const offered = (tz) => !!tz && [...sel.options].some((o) => o.value === tz);
  const probed = (w.probe && w.probe.timezone) || '';
  if (probed && probed !== 'UTC') return probed;
  let browser = '';
  try {
    browser = Intl.DateTimeFormat().resolvedOptions().timeZone || '';
  } catch { /* no Intl */ }
  if (offered(browser)) return browser;
  return probed || 'UTC';
}

function renderExtras() {
  const sel = byId('timezone');
  const tz = w.timezone || guessTimezone(sel);
  if (![...sel.options].some((o) => o.value === tz)) {
    sel.prepend(h('option', { value: tz, text: tz.replace(/_/g, ' ') }));
  }
  sel.value = tz;

  // Libraries on the target drive are about to be overwritten or are
  // already VaporOS's own, so only other drives are offered.
  const libs = [];
  for (const d of usableDisks()) {
    if (d.path === w.disk.path) continue;
    for (const lib of d.steam_libraries || []) libs.push({ ...lib, model: d.model, size: d.size });
  }
  // Every library starts checked the first time it is offered; after that
  // the visitor's choice sticks, even when they go back and pick another drive.
  for (const lib of libs) {
    if (w.librariesSeen.has(lib.uuid)) continue;
    w.librariesSeen.add(lib.uuid);
    w.libraries.add(lib.uuid);
  }
  fill(byId('library-list'), libs.map((lib) => {
    const input = h('input', { type: 'checkbox', value: lib.uuid });
    input.checked = w.libraries.has(lib.uuid);
    input.addEventListener('change', () => {
      if (input.checked) w.libraries.add(lib.uuid);
      else w.libraries.delete(lib.uuid);
    });
    return h('label', { class: 'choice' }, input,
      h('span', { class: 'choice-body' },
        h('span', { class: 'choice-main' },
          h('strong', { text: lib.label || lib.path || lib.uuid }),
          h('span', { class: 'muted', text: [lib.model, lib.size ? bytes(lib.size) : '', lib.path].filter(Boolean).join(' · ') }))));
  }));
  byId('library-empty').hidden = libs.length > 0;
}

function selectedLibraries() {
  const offered = new Set($$('#library-list input').map((i) => i.value));
  return [...w.libraries].filter((u) => offered.has(u));
}

// libraryFolder is where an adopted library appears on the installed
// system: /var/mnt/<label> (made safe the way install/target.go's
// libraryMountpoint does it, else the UUID) plus the folder inside it.
function libraryFolder(lib) {
  const name = String(lib.label || '').replace(/[^A-Za-z0-9._-]/g, '_').replace(/^\.+/, '');
  const inside = !lib.path || lib.path === '/' ? '' : `/${String(lib.path).replace(/^\/+/, '')}`;
  return `/var/mnt/${name || lib.uuid}${inside}`;
}

function selectedLibraryFolders() {
  const chosen = new Set(selectedLibraries());
  const out = [];
  for (const d of usableDisks()) {
    for (const lib of d.steam_libraries || []) {
      if (chosen.has(lib.uuid)) out.push(libraryFolder(lib));
    }
  }
  return [...new Set(out)];
}

// ---------- step: confirm ----------

function renderSummary() {
  const d = w.disk;
  const erase = w.mode === 'erase';
  const libs = selectedLibraries();
  const labels = $$('#library-list .choice').filter((c) => c.querySelector('input').checked).map((c) => c.querySelector('strong').textContent);
  kv(byId('summary'), [
    ['Drive', `${d.model || d.path} (${bytes(d.size)})`],
    ['Install', erase ? 'Erase the drive and install' : 'Repair: keep games and settings'],
    ['Address', w.hostname ? `http://${w.hostname}.local` : 'Unchanged'],
    ['Password', w.password ? 'New password set' : 'Unchanged'],
    ['Time zone', w.timezone ? w.timezone.replace(/_/g, ' ') : 'Unchanged'],
    ['Steam libraries', libs.length ? labels.join(', ') : 'None'],
  ]);
  byId('erase-warning').hidden = !erase;
  byId('erase-field').hidden = !erase;
  setText('erase-disk', `${d.model || d.path} (${bytes(d.size)})`);
  byId('erase-confirm').value = '';
  showError('install-error', '');
  updateInstallButton();
}

function eraseConfirmed() {
  return byId('erase-confirm').value.trim().toUpperCase() === 'ERASE';
}

function updateInstallButton() {
  byId('install-btn').disabled = w.mode === 'erase' && !eraseConfirmed();
}

async function submitInstall() {
  if (w.mode === 'erase' && !eraseConfirmed()) return;
  showError('install-error', '');
  const repair = w.mode === 'repair';
  // Empty means "keep": a repair never renames the PC or moves its clock.
  const body = {
    disk: w.disk.path,
    mode: w.mode,
    hostname: repair ? '' : w.hostname,
    password: w.password,
    timezone: repair ? '' : w.timezone,
    libraries: selectedLibraries(),
    source: '',
  };
  try {
    await api('POST', '/install', body);
  } catch (e) {
    if (askForCode(e)) return;
    // 409: an install is already running (another tab); follow it.
    if (!(e instanceof ApiError) || e.status !== 409) {
      showError('install-error', e.message);
      return;
    }
  }
  try {
    if (w.hostname) sessionStorage.setItem(HOST_KEY, w.hostname);
    else sessionStorage.removeItem(HOST_KEY);
  } catch { /* ignore */ }
  startProgress();
}

// ---------- progress ----------

function leaveGuard(e) {
  e.preventDefault();
  e.returnValue = '';
}

function startProgress() {
  w.finished = false;
  byId('progress-failed').hidden = true;
  byId('progress-retry').hidden = true;
  const bar = byId('progress-bar');
  bar.removeAttribute('value'); // indeterminate until the first report
  setText('progress-step', 'Starting');
  setText('progress-pct', '');
  setText('progress-message', '');
  go('progress');
  window.addEventListener('beforeunload', leaveGuard);
  poll();
}

// poll asks /install/status every 2 s; install.progress events make the bar
// move sooner, but polling alone is enough when the stream is unavailable.
async function poll() {
  if (w.polling) return;
  w.polling = true;
  while (!w.finished) {
    try {
      apply(await api('GET', '/install/status'));
    } catch (e) {
      if (askForCode(e)) break;
      /* otherwise transient; keep polling */
    }
    if (!w.finished) await sleep(2000);
  }
  w.polling = false;
}

// apply shows one progress report: an install.progress event or a status.
function apply(s) {
  if (!s || w.finished || w.step !== 'progress') return;
  if (s.state === 'idle') return;
  if (s.step) setText('progress-step', phaseLabel(s.step));
  if (s.percent != null) {
    const pct = percent(s.percent);
    byId('progress-bar').value = pct;
    setText('progress-pct', `${pct}%`);
  }
  if (s.message) setText('progress-message', s.message);
  if (s.state === 'done') finish();
  else if (s.state === 'failed') fail(s.error || s.message);
}

function finish() {
  w.finished = true;
  window.removeEventListener('beforeunload', leaveGuard);
  byId('progress-bar').value = 100;
  // VaporOS mounts adopted libraries but does not register them with Steam.
  const folders = selectedLibraryFolders();
  byId('done-libraries').hidden = folders.length === 0;
  fill(byId('done-library-paths'), folders.map((f, i) => [i ? (i === folders.length - 1 ? ' and ' : ', ') : '', h('span', { class: 'mono', text: f })]));
  go('done');
}

function fail(msg) {
  w.finished = true;
  window.removeEventListener('beforeunload', leaveGuard);
  setText('progress-error', msg || 'No details were reported.');
  byId('progress-failed').hidden = false;
  byId('progress-retry').hidden = false;
}

// ---------- restart ----------

async function reboot() {
  const btn = byId('reboot-btn');
  const ok = await busy(btn, async () => {
    await api('POST', '/install/reboot', {});
    return true;
  });
  if (!ok) return;
  go('restart');
  waitForSystem();
}

const hostURL = (ip) => (ip.includes(':') ? `http://[${ip}]/` : `http://${ip}/`);

async function reachable(url) {
  const ctl = new AbortController();
  const t = setTimeout(() => ctl.abort(), 4000);
  try {
    // An opaque answer is enough: it means something serves VaporOS's API.
    await fetch(`${url}api/v1/ping`, { mode: 'no-cors', cache: 'no-store', signal: ctl.signal });
    return true;
  } catch {
    return false;
  } finally {
    clearTimeout(t);
  }
}

async function samePing() {
  try {
    const r = await fetch('/api/v1/ping', { cache: 'no-store' });
    return r.ok ? await r.json() : null;
  } catch {
    return null;
  }
}

// waitForSystem follows the machine through its restart. The installed
// system usually gets the same IP, so this origin answering with mode "os"
// is the common case; otherwise http://<hostname>.local is tried once the
// installer has gone away (before that the name may belong to another box).
// A repair keeps the installed name, which the wizard may not know; then
// only this address is watched.
async function waitForSystem() {
  const name = w.hostname || (w.disk && w.disk.hostname) || '';
  const target = name ? `http://${name}.local/` : '';
  byId('restart-or').hidden = !target;
  if (target) {
    const link = byId('restart-link');
    link.href = target;
    link.textContent = `http://${name}.local`;
  }
  const ips = ((w.probe && w.probe.ips) || []).filter((ip) => ip && ip !== location.hostname);
  if (ips.length) fill(byId('restart-alt'), ', or ', h('a', { href: hostURL(ips[0]), text: hostURL(ips[0]).replace(/\/$/, '') }));
  const start = Date.now();
  let down = false;
  for (;;) {
    await sleep(2500);
    const here = await samePing();
    if (here && here.mode === 'os') {
      location.replace('/');
      return;
    }
    if (!here && !down) {
      down = true;
      setText('restart-status', 'VaporOS is starting from the drive. This takes about a minute.');
    }
    if (target && (down || Date.now() - start > 30000) && (await reachable(target))) {
      setText('restart-status', 'VaporOS is up. Opening it…');
      location.replace(target);
      return;
    }
    if (Date.now() - start > 4 * 60e3) {
      // The installed system may have got another address from the router.
      setText('restart-status', "This is taking longer than usual. Check that the USB stick is out and the PC restarted, then open the address shown on the PC's screen or in your router's list of devices.");
    }
  }
}

// ---------- wiring ----------

function wire() {
  onSubmit(byId('code-form'), submitCode);
  const rescan = byId('disk-rescan');
  rescan.addEventListener('click', () => probe(rescan));
  byId('disk-next').addEventListener('click', diskNext);
  for (const b of $$('[data-back]')) b.addEventListener('click', back);
  for (const r of $$('input[name="mode"]')) {
    r.addEventListener('change', () => {
      w.mode = r.value;
      w.modeChosen = true;
      updateModeFields();
    });
  }
  byId('hostname').addEventListener('input', updateHostPreview);
  onSubmit(byId('account-form'), async () => submitAccount());
  onSubmit(byId('extras-form'), async () => {
    w.timezone = w.mode === 'repair' ? '' : byId('timezone').value;
    renderSummary();
    go('confirm');
  });
  byId('erase-confirm').addEventListener('input', updateInstallButton);
  onSubmit(byId('confirm-form'), submitInstall);
  byId('retry-btn').addEventListener('click', () => {
    renderSummary();
    go('confirm');
  });
  byId('reboot-btn').addEventListener('click', reboot);
  on('install.progress', apply);
}

async function main() {
  await boot('setup', { auth: false });
  const code = initSetupCode();
  wire();
  updateHostPreview();
  try {
    w.hostname = sessionStorage.getItem(HOST_KEY) || w.hostname;
  } catch { /* ignore */ }

  let status;
  try {
    status = await verify();
  } catch (e) {
    fill(byId('disk-list'));
    go('disk');
    showError('disk-error', e.message);
    return;
  }
  if (!status) {
    go('code');
    if (code) showError('code-error', "That code isn't right. Check the screen connected to the PC.");
    return;
  }
  connectEvents({ auth: false });
  // A reload in the middle of an install resumes where it was.
  if (status.state === 'running') {
    startProgress();
    apply(status);
    return;
  }
  if (status.state === 'done') {
    go('done');
    return;
  }
  try {
    if ((await probe()) === false) return; // back at the code step
  } catch (e) {
    showError('disk-error', e.message);
  }
  go('disk');
  if (status.state === 'failed' && status.error) showError('disk-error', `The last install failed: ${status.error}`);
}

main();
