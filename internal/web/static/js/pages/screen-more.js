// pages/screen-more.js: Screen's cards below Right now (spec-cc-screens
// §5.3-§5.7): stream quality, the virtual screen, resolutions with Remove
// and Add, the stream server, ports and layers. screen.js loads it.

import { api, errorText } from '../core/api.js';
import { announce } from '../core/announce.js';
import { byId, cloneTpl, h, part, setText } from '../core/dom.js';
import { on, onReconnect } from '../core/live.js';
import { ago, groupModes, modeLabel, parseMode } from '../fmt.js';
import { bindForm, busy, fieldError } from '../ui/form.js';
import { notify, onStatus } from '../ui/shell.js';
import { S, cardError, early, renderDisplay, renderNow, settle, streaming, views } from './screen.js';

let rules = null;
import('../validate.js').then((m) => (rules = m), () => {});

const ENCODERS = { vulkan: 'Vulkan (recommended for AMD)', vaapi: 'VA-API', software: 'Software (CPU)', nvenc: 'NVENC (NVIDIA)' };
const GAMEPADS = { auto: 'Automatic', xone: 'Xbox One', xseries: 'Xbox Series', x360: 'Xbox 360', ds4: 'DualShock 4', ds5: 'DualSense', switch: 'Switch Pro', generic: 'Generic' };

const key = (s) => {
  const m = parseMode(s);
  return m ? `${m.w}x${m.h}@${m.hz}` : '';
};
const oops = async (err) => notify(await errorText(err), { kind: 'error' });

// ---- stream quality (§5.3: A1, A2, B8, B9)

const form = byId('quality');
let quality = null;
let shownValues = {};

function values() {
  const enc = form.querySelector('input[name="encoder"]:checked');
  return { encoder: enc ? enc.value : '', bitrate: byId('q-bitrate').value.trim(), gamepad: byId('q-gamepad').value, audio: byId('q-audio').value.trim() };
}

// A saved value the server no longer offers stays selectable (nvenc).
function choices(s, k, fallback) {
  const c = s.choices && Array.isArray(s.choices[k]) && s.choices[k].length ? [...s.choices[k]] : fallback;
  return s[k] && !c.includes(s[k]) ? [...c, s[k]] : c;
}

function presets() {
  const v = byId('q-bitrate').value.trim();
  for (const b of byId('q-presets').children) b.setAttribute('aria-pressed', String(v !== '' && Number(v) === Number(b.dataset.mbps)));
}

function enable(on) {
  byId('q-encoder').disabled = !on;
  for (const el of [byId('q-bitrate'), byId('q-gamepad'), byId('q-audio'), ...byId('q-presets').children]) el.disabled = !on;
  byId('quality-actions').hidden = !on;
}

// An edited card keeps its edits (B9).
function renderSettings(s, force) {
  S.settings = s;
  if (!force && quality && quality.isDirty()) return;
  const enc = choices(s, 'encoder', Object.keys(ENCODERS).slice(0, 3));
  const box = byId('q-encoders');
  if ([...box.querySelectorAll('input')].map((r) => r.value).join() !== enc.join()) {
    box.replaceChildren(...enc.map((v) => h('label', { class: 'scr-option', for: `q-enc-${v}` },
      h('input', { class: 'scr-radio', type: 'radio', name: 'encoder', id: `q-enc-${v}`, value: v }),
      h('span', { class: 'scr-dot', 'aria-hidden': 'true' }),
      h('span', { class: 'scr-option-name', text: ENCODERS[v] || v }))));
  }
  for (const r of box.querySelectorAll('input')) r.checked = r.value === s.encoder;
  const pads = choices(s, 'gamepad', Object.keys(GAMEPADS));
  const sel = byId('q-gamepad');
  if ([...sel.options].map((o) => o.value).join() !== pads.join()) sel.replaceChildren(...pads.map((v) => h('option', { value: v, text: GAMEPADS[v] || v })));
  sel.value = s.gamepad;
  const range = (s.choices && s.choices.bitrate_kbps_max) || {};
  const rate = byId('q-bitrate');
  rate.max = String(Math.floor((Number(range.max) || 1e6) / 1000));
  rate.value = String(Math.round((Number(s.bitrate_kbps_max) || 0) / 1000));
  // A2: a missing audio_sink is the default sink.
  byId('q-audio').value = typeof s.audio_sink === 'string' ? s.audio_sink : '';
  presets();
  shownValues = values();
  for (const id of ['q-bitrate', 'q-gamepad', 'q-audio']) fieldError(byId(id), '');
  setText('quality-form-error', '');
  byId('quality-error').hidden = true;
  enable(true);
  if (quality) quality.setDirty(false);
  settle(form);
}

function settingsFailed(err) {
  enable(false);
  cardError('quality', err, "Couldn't load the stream settings.");
  settle(form);
}

function loadSettings(passive, force) {
  return api('GET', '/sunshine/settings', undefined, { passive }).then((s) => renderSettings(s, force), (err) => {
    if (!(passive && S.settings)) settingsFailed(err);
  });
}

// A 400 lands under its field, by its first word (sunshine/conf.go).
const FIELDS = { bitrate_kbps_max: 'q-bitrate', audio_sink: 'q-audio', gamepad: 'q-gamepad' };

async function save() {
  const v = values();
  const body = {};
  if (v.encoder !== shownValues.encoder) body.encoder = v.encoder;
  if (v.bitrate !== shownValues.bitrate) body.bitrate_kbps_max = Number(v.bitrate) * 1000;
  if (v.gamepad !== shownValues.gamepad) body.gamepad = v.gamepad;
  if (v.audio !== shownValues.audio) body.audio_sink = v.audio;
  if (!Object.keys(body).length) return;
  try {
    renderSettings(await api('PUT', '/sunshine/settings', body), true);
    notify(streaming() ? 'Saved. It applies after this stream ends.' : 'Saved. Streaming restarts to use it.', { kind: 'ok' });
  } catch (err) {
    if (err.status === 500 && /did not restart/i.test(err.message)) {
      notify(await errorText(err), { kind: 'warn' });
      return loadSettings(false, true);
    }
    if (err.status === 400) err.field = FIELDS[String(err.message).split(' ')[0]];
    throw err;
  }
}

function initQuality() {
  quality = bindForm(form, {
    validate: () => {
      const v = values();
      return rules ? { 'q-bitrate': rules.bitrateError(v.bitrate === '' ? NaN : v.bitrate), 'q-audio': rules.audioSinkError(v.audio) } : {};
    },
    submit: save,
    fieldFor: (err) => err.field || '',
    saved: '',
  });
  quality.setDirty(false);
  byId('q-presets').addEventListener('click', (e) => {
    const b = e.target.closest('[data-mbps]');
    if (!b) return;
    const rate = byId('q-bitrate');
    rate.value = b.dataset.mbps;
    rate.dispatchEvent(new Event('input', { bubbles: true }));
  });
  byId('q-bitrate').addEventListener('input', presets);
  byId('quality-retry').addEventListener('click', () => loadSettings(false, true));
}

// ---- virtual screen (§5.4)

let moving = false;

function renderVirtual(d) {
  const noGPU = d.profile === 'none';
  const hdr = byId('hdr');
  if (!hdr.hasAttribute('aria-disabled')) hdr.checked = !!d.hdr;
  hdr.disabled = noGPU;
  // Sizing Steam (display.ui_scaling) needs no GPU to be stored; an older
  // VaporOS has no such setting.
  const scaling = byId('ui-scaling');
  byId('scaling-row').hidden = typeof d.ui_scaling !== 'boolean';
  if (!scaling.hasAttribute('aria-disabled')) scaling.checked = d.ui_scaling === true;
  scaling.disabled = false;
  const free = d.available_connectors;
  const sel = byId('port');
  byId('port-field').hidden = !Array.isArray(free);
  if (Array.isArray(free) && !moving && (sel.value === '' || sel.value === sel.dataset.current)) {
    const cur = d.virtual_connector || '';
    sel.replaceChildren(...(cur ? [h('option', { value: cur, text: `${cur} (current)` })] : []), ...free.filter((c) => c !== cur).map((c) => h('option', { value: c, text: c })));
    sel.value = sel.dataset.current = cur;
    sel.disabled = noGPU || !free.length;
    portNote();
    if (!free.length && !noGPU) {
      byId('port-note').hidden = false;
      setText('port-note', 'No free ports.');
    }
  }
  settle(byId('virtual'));
}

function portNote() {
  const sel = byId('port');
  const to = sel.value && sel.value !== sel.dataset.current ? sel.value : '';
  byId('port-note').hidden = !to;
  setText('port-note', to && `Moves to ${to} when VaporOS restarts.`);
  byId('port-actions').hidden = !to;
}

async function move(btn) {
  const sel = byId('port');
  const port = sel.value;
  if (!(await (await import('../ui/dialog.js')).confirmDialog({ id: 'port', vars: { port, old: sel.dataset.current } }))) return;
  moving = true;
  await busy(btn, async () => {
    await api('PUT', '/display/settings', { virtual_connector: port });
    notify(`The virtual screen moves to ${port} when VaporOS restarts.`, { kind: 'ok' });
    sel.value = '';
  }, oops);
  moving = false;
  loadDisplay();
}

// liveSwitch applies a display setting at once and reverts on failure
// (R4); busy taps are undone.
function liveSwitch(el, key, said) {
  el.addEventListener('change', async () => {
    if (el.hasAttribute('aria-disabled')) {
      el.checked = !el.checked;
      return;
    }
    const want = el.checked;
    el.setAttribute('aria-disabled', 'true');
    try {
      await api('PUT', '/display/settings', { [key]: want });
      if (S.display) S.display[key] = want;
      announce(`${said} ${want ? 'on' : 'off'}`);
    } catch (err) {
      el.checked = !want;
      oops(err);
    }
    el.removeAttribute('aria-disabled');
  });
}

function initVirtual() {
  liveSwitch(byId('hdr'), 'hdr', 'HDR');
  liveSwitch(byId('ui-scaling'), 'ui_scaling', 'Sizing Steam for each device');
  byId('port').addEventListener('change', portNote);
  byId('port-move').addEventListener('click', (e) => move(e.currentTarget));
}

// ---- resolutions (§5.5: C10, D2, D8)

function renderModes(d) {
  const learned = d.learned || [];
  const extra = new Set(learned.map(key));
  const cur = key(d.current);
  // Without a GPU nothing drives the virtual screen: no table (§5.5 Empty).
  const groups = d.modes && d.modes.length ? groupModes([...d.modes, ...learned]) : [];
  byId('modes').replaceChildren(...groups.map((g) => {
    const li = cloneTpl('tpl-scr-mode');
    part(li, 'size').textContent = `${g.w} × ${g.h}`;
    part(li, 'rates').replaceChildren(...g.rates.map((hz) => {
      const k = `${g.w}x${g.h}@${hz}`;
      const kind = k === cur ? 'current' : extra.has(k) ? 'extra' : '';
      return h('span', { class: 'scr-rate', dataset: kind ? { kind } : null }, `${hz} Hz`, kind && h('span', { class: 'sr-only', text: kind === 'current' ? ' (in use)' : ' (extra)' }));
    }));
    return li;
  }));
  byId('modes').hidden = !groups.length;
  byId('modes-empty').hidden = !!groups.length;
  renderExtras(d, learned);
  byId('add-mode').disabled = d.profile === 'none';
  settle(byId('modes-card'));
}

// Added by hand or asked for by a device (API-6), each with Remove.
function renderExtras(d, learned) {
  const api6 = Array.isArray(d.added);
  const added = api6 ? d.added : [];
  const mine = new Set(added.map(key));
  const asked = learned.filter((m) => !mine.has(key(m)));
  const who = (m) => (d.devices || []).filter((x) => key(x.mode) === key(m)).map((x) => [x.name, ago(x.last_seen)].filter(Boolean).join(' · ')).join(', ');
  const item = (m, sub) => {
    const li = cloneTpl('tpl-scr-extra');
    li.dataset.mode = m;
    part(li, 'mode').textContent = modeLabel(m);
    part(li, 'sub').textContent = sub;
    part(li, 'sub').hidden = !sub;
    const btn = part(li, 'remove');
    part(li, 'remove-name').textContent = ` ${modeLabel(m)}`;
    btn.addEventListener('click', () => remove(m, btn));
    btn.disabled = d.profile === 'none';
    if (!api6) btn.remove();
    return li;
  };
  const had = document.activeElement.closest('.scr-extra');
  byId('added').replaceChildren(...added.map((m) => item(m, '')));
  byId('asked').replaceChildren(...asked.map((m) => item(m, who(m))));
  if (had) refocus(had.dataset.mode);
  byId('added-group').hidden = !added.length;
  byId('asked-group').hidden = !asked.length;
  if (!api6) setText('asked-title', 'Learned from your devices, or added by you');
  byId('extras-empty').hidden = !!(added.length + asked.length);
}

function refocus(mode) {
  const li = [...document.querySelectorAll('.scr-extra')].find((x) => x.dataset.mode === mode);
  (li ? part(li, 'remove') : byId('extras-title')).focus({ preventScroll: !!li });
}

async function remove(m, btn) {
  const label = modeLabel(m);
  const li = btn.closest('li');
  const next = (li.nextElementSibling || li.previousElementSibling || {}).dataset;
  await busy(btn, async () => {
    const r = await api('DELETE', `/display/modes/${encodeURIComponent(m)}`).catch((err) => {
      if (err.status !== 404) throw err;
    });
    notify(!r ? `${label} was already removed.` : r.reboot_needed ? `Removed ${label}. Restart VaporOS to finish.` : `Removed ${label}.`, { kind: 'ok' });
    await loadDisplay();
    if (!li.isConnected) refocus(next ? next.mode : '');
  }, oops);
}

let adder = null;
const addValues = () => ['add-w', 'add-h', 'add-hz'].map((id) => byId(id).value.trim());

function validateAdd() {
  const v = addValues();
  const n = v.map((x) => (x === '' ? NaN : Number(x)));
  const msg = rules ? rules.modeError(...n) : '';
  if (msg) {
    let id = 'add-w';
    if (/Hz/.test(msg)) id = 'add-hz';
    else if (/whole/.test(msg)) id = ['add-w', 'add-h', 'add-hz'][n.findIndex((x) => !Number.isInteger(x))];
    else if (/Too (small|large)/.test(msg) && n[0] >= 320 && n[0] <= 8192) id = 'add-h';
    return { [id]: msg };
  }
  const k = `${n[0]}x${n[1]}@${n[2]}`;
  const d = S.display || {};
  return [...(d.modes || []), ...(d.learned || [])].some((m) => key(m) === k) ? { 'add-w': `VaporOS already offers ${modeLabel(k)}.` } : {};
}

async function submitAdd() {
  const mode = addValues().map(Number).join('x').replace(/x(\d+)$/, '@$1');
  const r = await api('POST', '/display/modes', { mode });
  (await import('../ui/sheet.js')).closeSheet(byId('add-sheet'));
  notify(`Added ${modeLabel(mode)}.${r.reboot_needed === false ? '' : ' Restart VaporOS to use it.'}`, { kind: 'ok' });
  loadDisplay();
}

async function openAdd(invoker) {
  const { openSheet } = await import('../ui/sheet.js');
  const f = byId('add-form');
  adder = adder || bindForm(f, {
    validate: validateAdd,
    submit: submitAdd,
    fieldFor: (err) => (/refresh|clock|line rate/.test(err.message) ? 'add-hz' : /small|large|never/.test(err.message) ? 'add-w' : ''),
    saved: '',
  });
  f.reset();
  for (const id of ['add-w', 'add-h', 'add-hz']) fieldError(byId(id), '');
  setText('add-form-error', '');
  adder.setDirty(false);
  openSheet(byId('add-sheet'), { invoker, initialFocus: byId('add-w') });
}

// ---- stream server (§5.6: C3)

function renderServer() {
  const sun = S.sunshine;
  const st = S.sunshineErr && S.sunshineErr.status;
  const [text, tone] = st === 503 ? ['Starting', 'wait'] : st === 502 ? ['Not answering', 'danger'] : S.sunshineErr ? ['Unknown', ''] : sun && sun.running ? ['Running', 'ok'] : ['Stopped', 'danger'];
  const badge = byId('server-badge');
  badge.textContent = text;
  badge.dataset.tone = tone;
  setText('server-version', (sun && sun.version) || '–');
  setText('server-streaming', sun ? (sun.streaming ? 'Yes' : 'No') : '–');
  byId('server-error').hidden = !S.sunshineErr || !!tone;
  if (S.sunshineErr && !tone) cardError('server', S.sunshineErr, "Couldn't read the stream server.");
  byId('server-restart').disabled = false;
  settle(byId('server'));
}

function sunshineFrom(p) {
  return p.then((s) => {
    S.sunshine = s;
    S.sunshineErr = null;
  }, (err) => {
    S.sunshineErr = err;
    if (err.status !== 503 && err.status !== 502) S.sunshine = null;
  }).then(() => {
    renderServer();
    if (S.display) renderNow(S.display);
  });
}

const loadSunshine = (passive) => sunshineFrom(api('GET', '/sunshine', undefined, { passive }));

async function restart(btn) {
  if (streaming() && !(await (await import('../ui/dialog.js')).confirmDialog({ id: 'sunrestart' }))) return;
  await busy(btn, async () => {
    await api('POST', '/sunshine/restart', {});
    notify('Streaming is restarting.', { kind: 'info' });
    setTimeout(() => loadSunshine(true), 3000);
  }, oops);
}

// ---- wiring

function loadDisplay() {
  return api('GET', '/display').then(renderDisplay, oops);
}

export function start() {
  views.push(renderVirtual, renderModes);
  initQuality();
  initVirtual();
  byId('add-mode').addEventListener('click', (e) => openAdd(e.currentTarget));
  byId('server-restart').addEventListener('click', (e) => restart(e.currentTarget));
  byId('server-retry').addEventListener('click', () => loadSunshine(false));
  early.settings.then((s) => renderSettings(s, true), settingsFailed);
  sunshineFrom(early.sunshine);
  if (location.hash === '#stream') early.settings.catch(() => {}).then(() => byId('stream').scrollIntoView());
  // The shell re-asks /status on events; a live session moves Sunshine's rows.
  // A replayed session that agrees with the page asks nothing.
  let t = 0;
  const soon = (want) => (_, live) => {
    if (!live && S.sunshine && !!S.sunshine.streaming === want) return;
    clearTimeout(t);
    t = setTimeout(() => loadSunshine(true), 600);
  };
  // /status answers at once (GET /sunshine can take 3 s): its rows first.
  onStatus((snap) => {
    if (snap.sunshine && !S.sunshine && !S.sunshineErr) {
      S.sunshine = snap.sunshine;
      renderServer();
    }
  });
  on('session.begin', soon(true));
  on('session.end', soon(false));
  onReconnect(() => {
    soon()(null, true);
    loadSettings(true);
  });
}
