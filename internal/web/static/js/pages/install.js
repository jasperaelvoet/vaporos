// pages/install.js: the installer wizard on the live ISO (spec-cc-screens
// §15). Steps: the setup code (only while it is needed) → drive → name and
// password → games and time zone → confirm; pages/install-run.js takes the
// install from there (progress, done, the restart and the hand-off to the
// installed system); pages/install-state.js holds what both know. CI
// drives the same API with curl (tests/qemu-smoke.sh), so the requests stay
// exactly as they are.

import { ApiError, api, errorText, transport, url, useSetupCode } from '../core/api.js';
import { initSetupCode } from '../core/setup-code.js';
import { boot } from '../core/boot.js';
import { $$, byId, h, icon } from '../core/dom.js';
import { connect, on } from '../core/live.js';
import { cleanHostname, hostnameError, normalizeCode, passwordError } from '../validate.js';
import { region, settleMain } from '../ui/region.js';
import { bindReveal, fieldError, heat, hideReveals, retrySeconds, wait } from './entry-kit.js';
import { TRANSPORTS, bytes, chosenLibraries, diskLabel, fits, gib, minSize, offeredLibraries, restore, save, usable, w } from './install-state.js';

const INPUT = ['disk', 'account', 'extras', 'confirm'];
const WRONG_CODE = "That code isn't right. Check the screen connected to the PC.";

const wiz = byId('wizard');
const vf = byId('wiz-vf');
const disks = region(byId('disk-list'));

// ---------- steps

// go shows one step: the stepper marks it on the heat scale, the viewfinder
// shows who this is at the code and the install's heat after it, and the
// step's heading takes focus.
function go(step, { focus = true } = {}) {
  const i = INPUT.indexOf(step);
  wiz.dataset.dir = i >= 0 && i < INPUT.indexOf(w.step) ? 'back' : 'forward';
  w.step = step;
  wiz.dataset.step = step;
  for (const pane of $$('.wiz-pane', wiz)) pane.hidden = pane.dataset.step !== step;
  byId('wiz-head').hidden = i < 0;
  for (const li of $$('.wiz-seg', wiz)) {
    const j = INPUT.indexOf(li.dataset.step);
    const state = j < i ? 'done' : j === i ? 'current' : '';
    if (state) li.dataset.state = state;
    else delete li.dataset.state;
    if (state === 'current') li.setAttribute('aria-current', 'step');
    else li.removeAttribute('aria-current');
    li.querySelector('[data-part="done"]').textContent = state === 'done' ? ', done' : '';
  }
  if (i >= 0) byId('wiz-count').textContent = `Step ${i + 1} of ${INPUT.length}`;
  // Phones show the viewfinder at the code and once the install runs;
  // desktops keep it beside the steps, warming as the install nears.
  const setting = i >= 0 || step === 'code';
  vf.dataset.show = String(i < 0);
  vf.dataset.size = step === 'code' ? 'id' : 'hero';
  byId('vf-id').hidden = !setting;
  if (setting) {
    byId('vf-plates').hidden = true;
    byId('vf-big').hidden = true;
    byId('vf-needle').textContent = 'setting up';
    heat(vf, i >= 0 ? step : 'standby');
  }
  if (i >= 0) save();
  if (!focus) return;
  scrollTo({ top: 0 });
  const title = wiz.querySelector(`.wiz-pane[data-step="${step}"] .wiz-title`);
  if (title) title.focus({ preventScroll: true });
}

function back() {
  const i = INPUT.indexOf(w.step);
  if (i > 0) go(INPUT[i - 1]);
}

// ---------- the setup code

// verify asks for the install status, which needs the setup code: the
// status, or null when the code is wrong.
async function verify() {
  try {
    return await api('GET', '/install/status');
  } catch (err) {
    if (err instanceof ApiError && err.status === 403) return null;
    throw err;
  }
}

// askForCode is a Setup call refused mid-wizard: the waiver for a PC
// without a monitor ends as soon as one is plugged in. An install already
// running carries on; the reload after the code picks it up again.
function askForCode(err) {
  if (!(err instanceof ApiError && err.status === 403)) return false;
  w.finished = true;
  go('code');
  byId('code-error').textContent = "Enter the setup code shown on the PC's screen to continue.";
  return true;
}

async function submitCode(e) {
  e.preventDefault();
  const btn = byId('code-submit');
  const field = byId('setup-code');
  if (btn.disabled) return;
  byId('code-error').textContent = '';
  field.value = normalizeCode(field.value);
  if (!field.value) {
    fieldError(field, 'Enter the setup code from the screen.');
    field.focus();
    return;
  }
  useSetupCode(field.value);
  btn.disabled = true;
  btn.setAttribute('aria-busy', 'true');
  let status;
  try {
    status = await verify();
  } catch (err) {
    btn.removeAttribute('aria-busy');
    if (err instanceof ApiError && err.status === 429) {
      byId('code-error').textContent = err.retryAfter ? `Too many wrong codes. Try again in ${retrySeconds(err)} s.` : 'Too many wrong codes. Wait a few minutes and try again.';
      wait(btn, retrySeconds(err), 'Continue');
      return;
    }
    btn.disabled = false;
    byId('code-error').textContent = await errorText(err);
    return;
  }
  btn.removeAttribute('aria-busy');
  if (!status) {
    btn.disabled = false;
    fieldError(field, WRONG_CODE);
    heat(vf, 'cold');
    field.focus();
    field.select();
    return;
  }
  // Load /setup?code= so the server also sets the setup cookie, which the
  // progress stream needs (EventSource cannot send the header).
  location.replace(url(`/setup?code=${encodeURIComponent(field.value)}`));
}

// ---------- step 1: the drive

async function probe(btn) {
  const run = async () => {
    byId('disk-error').textContent = '';
    disks.loading();
    try {
      w.probe = await api('GET', '/install/probe');
    } catch (err) {
      if (askForCode(err)) return;
      const text = await errorText(err);
      disks.ready(() => h('div', { class: 'region-error', role: 'alert' }, h('p', { class: 'region-error-text', text: `Couldn't list the drives. ${text}` })));
      return;
    }
    renderProbe();
  };
  if (!btn) return run();
  if (btn.getAttribute('aria-busy') === 'true') return undefined;
  btn.setAttribute('aria-busy', 'true');
  try {
    return await run();
  } finally {
    btn.removeAttribute('aria-busy');
  }
}

function note(id, tone, parts) {
  const el = byId(id);
  el.hidden = !parts;
  if (!parts) return;
  if (tone) el.dataset.tone = tone;
  el.querySelector('p').replaceChildren(...parts);
}

function renderProbe() {
  const p = w.probe || {};
  const gpu = p.gpu || {};
  if (gpu.supported) {
    note('gpu-note', 'ok', [h('b', { text: gpu.name || 'The graphics card' }), ' is supported for streaming.']);
  } else {
    note('gpu-note', 'warn', [h('b', { text: gpu.name ? `${gpu.name} isn't supported yet.` : 'No supported graphics card found.' }), ' VaporOS installs, but streaming needs an AMD Radeon GPU.']);
  }
  // A7: the probe checks the image with the install's own trust rules, so
  // an unsigned or damaged stick stops here rather than at the very end.
  note('disk-fault', 'fault', p.source_error
    ? [h('b', { text: "This USB stick's copy of VaporOS can't be installed: " }), String(p.source_error), '. Write the stick again from a fresh download.']
    : null);
  byId('disk-next').disabled = !!p.source_error;
  const version = byId('disk-version');
  version.hidden = !p.version;
  version.textContent = p.version ? `Installs VaporOS ${p.version}.` : '';

  const list = usable();
  const ok = list.filter(fits);
  w.disk = ok.find((d) => d.path === (w.disk?.path || w.diskPath)) || (ok.length === 1 ? ok[0] : null);
  const need = `a drive of at least ${gib(minSize())} (about ${bytes(minSize())})`;
  if (!list.length) {
    disks.empty(`No drives found. Connect ${need} and rescan.`);
    return;
  }
  disks.ready(() => [...list.map(diskCard), ok.length ? null : h('p', { class: 'wiz-version', text: `None of these is big enough. Connect ${need} and rescan.` })].filter(Boolean));
}

function diskCard(d) {
  const small = !fits(d);
  const input = h('input', { class: 'wiz-opt-input', type: 'radio', name: 'disk', value: d.path, disabled: small });
  input.checked = !!w.disk && w.disk.path === d.path;
  input.addEventListener('change', () => {
    w.disk = d;
    byId('disk-error').textContent = '';
    save();
  });
  const tags = [];
  if (d.has_vaporos) tags.push(h('span', { class: 'wiz-tag', dataset: { tone: 'vapor' }, text: d.hostname ? `VaporOS installed: ${d.hostname}.local` : 'VaporOS installed' }));
  for (const lib of d.steam_libraries || []) tags.push(h('span', { class: 'wiz-tag', dataset: { tone: 'games' }, text: `Steam library${lib.label ? `: ${lib.label}` : ''}` }));
  if (d.removable) tags.push(h('span', { class: 'wiz-tag', text: 'Removable' }));
  if (small) tags.push(h('span', { class: 'wiz-tag', dataset: { tone: 'small' }, text: `Too small (needs ${gib(minSize())})` }));
  const facts = [bytes(d.size), TRANSPORTS[String(d.transport || '').toLowerCase()] || d.transport, d.path].filter(Boolean).join(' · ');
  return h('label', { class: 'wiz-opt' }, input,
    d.transport === 'usb' ? icon('usb') : icon('drive'),
    h('span', { class: 'wiz-opt-body' },
      h('b', { class: 'wiz-opt-name', text: d.model || d.path }),
      h('span', { class: 'wiz-opt-meta mono', text: facts }),
      tags.length ? h('span', { class: 'wiz-tags' }, tags) : null));
}

function diskNext() {
  if (w.probe?.source_error) return;
  if (!w.disk) {
    byId('disk-error').textContent = 'Choose the drive to install on.';
    return;
  }
  // Repair is the safe default on a drive that already has VaporOS.
  const vapor = !!w.disk.has_vaporos;
  byId('mode-field').hidden = !vapor;
  w.mode = vapor && !(w.modeChosen && w.mode === 'erase') ? 'repair' : 'erase';
  for (const r of $$('input[name="mode"]')) r.checked = r.value === w.mode;
  modeFields();
  go('account');
}

// ---------- step 2: name and password

// modeFields adapts the step to erase or repair. A repair keeps the PC's
// name and time zone: the wizard sends neither, and the install keeps them.
function modeFields() {
  const repair = w.mode === 'repair';
  for (const id of ['password', 'password2']) byId(id).required = !repair;
  byId('repair-keeps').hidden = !repair;
  byId('hostname-field').hidden = repair;
  byId('timezone-field').hidden = repair;
  byId('password-hint').textContent = repair
    ? 'Leave empty to keep the current password, or set a new one (at least 8 characters).'
    : "At least 8 characters. You'll use it to sign in here.";
}

function hostPreview() {
  byId('host-preview').textContent = `http://${cleanHostname(byId('hostname').value) || 'vapor'}.local`;
}

function submitAccount(e) {
  e.preventDefault();
  const repair = w.mode === 'repair';
  const host = byId('hostname');
  const p1 = byId('password');
  const p2 = byId('password2');
  host.value = cleanHostname(host.value);
  const errs = [[host, repair ? '' : hostnameError(host.value)]];
  const why = passwordError(p1.value, p2.value, { optional: repair });
  const mismatch = !!why && p1.value && passwordError(p1.value) === '';
  errs.push([p1, mismatch ? '' : why], [p2, mismatch ? why : '']);
  for (const [el, msg] of errs) fieldError(el, msg);
  const first = errs.find(([, msg]) => msg);
  if (first) {
    first[0].focus();
    return;
  }
  hideReveals(byId('account-form'));
  w.hostname = repair ? '' : host.value;
  w.password = p1.value;
  renderExtras();
  go('extras');
}

// ---------- step 3: games and time

// guessZone prefers what the live system knows, but the ISO has no zone of
// its own and says UTC; then the browser's zone is the better guess (B13).
function guessZone(sel) {
  const offered = (tz) => !!tz && [...sel.options].some((o) => o.value === tz);
  const probed = w.probe?.timezone || '';
  if (probed && probed !== 'UTC') return probed;
  let mine = '';
  try {
    mine = Intl.DateTimeFormat().resolvedOptions().timeZone || '';
  } catch {
    /* no Intl */
  }
  return offered(mine) ? mine : probed || 'UTC';
}

function renderExtras() {
  const sel = byId('timezone');
  const tz = w.timezone || guessZone(sel);
  if (![...sel.options].some((o) => o.value === tz)) sel.prepend(h('option', { value: tz, text: tz.replace(/_/g, ' ') }));
  sel.value = tz;
  const libs = offeredLibraries();
  // Each starts checked the first time it is offered; after that the
  // visitor's choice sticks, even across another drive or a reload.
  for (const lib of libs) {
    if (w.librariesSeen.has(lib.uuid)) continue;
    w.librariesSeen.add(lib.uuid);
    w.libraries.add(lib.uuid);
  }
  byId('library-list').replaceChildren(...libs.map((lib) => {
    const input = h('input', { class: 'wiz-opt-input', type: 'checkbox', value: lib.uuid });
    input.checked = w.libraries.has(lib.uuid);
    input.addEventListener('change', () => {
      if (input.checked) w.libraries.add(lib.uuid);
      else w.libraries.delete(lib.uuid);
      save();
    });
    return h('label', { class: 'wiz-opt', dataset: { kind: 'check' } }, input,
      h('span', { class: 'wiz-opt-mark', 'aria-hidden': 'true' }),
      h('span', { class: 'wiz-opt-body' },
        h('b', { class: 'wiz-opt-name', text: lib.label || lib.path || lib.uuid }),
        h('span', { class: 'wiz-opt-meta mono', text: [lib.model, lib.size ? bytes(lib.size) : '', lib.path].filter(Boolean).join(' · ') })));
  }));
  byId('library-empty').hidden = libs.length > 0;
  save();
}

// ---------- step 4: confirm

function renderSummary() {
  const d = w.disk;
  const erase = w.mode === 'erase';
  const libs = chosenLibraries();
  const rows = [
    ['Drive', diskLabel(d)],
    ['Install', erase ? 'Erase the drive and install' : 'Repair: keep games and settings'],
    ['Version', w.probe?.version || 'The one on this USB stick'],
    ['Address', w.hostname ? `http://${w.hostname}.local` : 'Unchanged'],
    ['Password', w.password ? 'New password set' : 'Unchanged'],
    ['Time zone', w.timezone ? w.timezone.replace(/_/g, ' ') : 'Unchanged'],
    ['Steam libraries', libs.length ? [...new Set(libs.map((l) => l.label || l.uuid))].join(', ') : 'None'],
  ];
  byId('summary').replaceChildren(...rows.map(([k, v]) => h('div', { class: 'fact' }, h('dt', { text: k }), h('dd', { text: v }))));
  // C8: another drive with VaporOS stays as it is, and keeps its games.
  const others = usable().filter((x) => x.has_vaporos && x.path !== d.path);
  note('other-vapor', '', others.length
    ? [others.map((x) => `${x.model || x.path} also has VaporOS on it.`).join(' '), ` This install leaves ${others.length > 1 ? 'those drives' : 'that drive'} alone, and VaporOS can't use ${others.length > 1 ? 'them' : 'it'} for games while that install is there.`]
    : null);
  byId('erase-warning').hidden = !erase;
  byId('erase-field').hidden = !erase;
  byId('erase-disk').textContent = diskLabel(d);
  byId('erase-confirm').value = '';
  byId('install-error').textContent = '';
  installButton();
}

const erased = () => byId('erase-confirm').value.trim().toUpperCase() === 'ERASE';

function installButton() {
  byId('install-btn').disabled = w.mode === 'erase' && !erased();
}

async function submitInstall(e) {
  e.preventDefault();
  const btn = byId('install-btn');
  if (btn.disabled || (w.mode === 'erase' && !erased())) return;
  byId('install-error').textContent = '';
  const repair = w.mode === 'repair';
  // Empty means "keep": a repair never renames the PC or moves its clock.
  const body = {
    disk: w.disk.path,
    mode: w.mode,
    hostname: repair ? '' : w.hostname,
    password: w.password,
    timezone: repair ? '' : w.timezone,
    libraries: chosenLibraries().map((l) => l.uuid),
    source: '',
  };
  btn.disabled = true;
  btn.setAttribute('aria-busy', 'true');
  try {
    await api('POST', '/install', body);
  } catch (err) {
    btn.removeAttribute('aria-busy');
    btn.disabled = false;
    if (askForCode(err)) return;
    // 409: an install already runs (another tab started it): follow it.
    if (!(err instanceof ApiError && err.status === 409)) {
      byId('install-error').textContent = await errorText(err);
      return;
    }
  }
  btn.removeAttribute('aria-busy');
  (await run()).start();
}

// ---------- the install itself (pages/install-run.js)

// reachable: does anything answer VaporOS's API at base (another origin,
// http://<name>.local/)? An opaque no-cors answer is enough. This page's
// CSP allows connect-src http://*.local for it.
async function reachable(base) {
  const ctl = new AbortController();
  const t = setTimeout(() => ctl.abort(), 4000);
  try {
    await transport().fetch(`${base}api/v1/ping`, { mode: 'no-cors', cache: 'no-store', signal: ctl.signal });
    return true;
  } catch {
    return false;
  } finally {
    clearTimeout(t);
  }
}

let runner = null;
function run() {
  runner ||= import('./install-run.js').then((m) => m.runner({
    vf, go, askForCode, reachable,
    back: () => {
      renderSummary();
      go('confirm');
    },
  }));
  return runner;
}

// ---------- wiring

function wire() {
  byId('code-form').addEventListener('submit', submitCode);
  const rescan = byId('disk-rescan');
  rescan.addEventListener('click', () => probe(rescan));
  byId('disk-next').addEventListener('click', diskNext);
  for (const b of $$('[data-back]')) b.addEventListener('click', back);
  for (const r of $$('input[name="mode"]')) {
    r.addEventListener('change', () => {
      w.mode = r.value;
      w.modeChosen = true;
      modeFields();
      save();
    });
  }
  const host = byId('hostname');
  host.addEventListener('input', () => {
    hostPreview();
    save();
  });
  host.addEventListener('blur', () => {
    host.value = cleanHostname(host.value);
  });
  for (const id of ['hostname', 'password', 'password2', 'setup-code']) {
    byId(id).addEventListener('input', (e) => fieldError(e.target, ''));
  }
  byId('account-form').addEventListener('submit', submitAccount);
  byId('timezone').addEventListener('change', (e) => {
    w.timezone = e.target.value;
    save();
  });
  byId('extras-form').addEventListener('submit', (e) => {
    e.preventDefault();
    w.timezone = w.mode === 'repair' ? '' : byId('timezone').value;
    renderSummary();
    go('confirm');
  });
  byId('erase-confirm').addEventListener('input', installButton);
  byId('confirm-form').addEventListener('submit', submitInstall);
  bindReveal(wiz);
}

// idle settles the drive list's skeleton when the wizard goes elsewhere
// first (the code step, a resumed install), so the page reads as loaded.
function idle() {
  const el = byId('disk-list');
  if (el.getAttribute('aria-busy') !== 'true') return;
  el.removeAttribute('aria-busy');
  el.dataset.state = 'idle';
  settleMain();
}

async function main() {
  // The wizard only ever navigates to itself (the code's reload, a reload
  // mid-install) or away for good (the hand-off): no cross-fade. On phones
  // Chrome aborts the one it would start with an uncaught error.
  addEventListener('pageswap', (e) => e.viewTransition?.skipTransition());
  await boot('setup', { auth: false, events: false });
  const code = initSetupCode(byId('setup-code'));
  restore();
  wire();
  hostPreview();
  let status;
  try {
    status = await verify();
  } catch (err) {
    go('disk', { focus: false });
    const text = await errorText(err);
    disks.ready(() => h('div', { class: 'region-error', role: 'alert' }, h('p', { class: 'region-error-text', text: `Couldn't reach the installer. ${text}` })));
    return;
  }
  if (!status) {
    idle();
    go('code', { focus: false });
    if (code) fieldError(byId('setup-code'), WRONG_CODE);
    return;
  }
  connect({ auth: false });
  on('install.progress', (s) => runner?.then((r) => r.apply(s)));
  // A reload during an install picks it up where it is.
  if (status.state === 'running' || status.state === 'done') {
    idle();
    probe().catch(() => {});
    const r = await run();
    if (status.state === 'running') r.start(status);
    else r.finish();
    return;
  }
  await probe();
  if (w.step === 'code') return;
  go('disk', { focus: false });
  if (status.state === 'failed' && status.error) {
    note('disk-fault', 'danger', [h('b', { text: 'The last install failed: ' }), String(status.error)]);
  }
}

main();
