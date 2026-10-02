// pages/extensions.js: System › Extensions (docs/CONTRACTS.md "Extensions").
// One card per extension from GET /extensions, kept current by the
// extensions.state event alone: nothing here polls. Install, Remove, a
// setting, an action and Try again each answer with the whole document,
// which replaces the one shown. Cards are made once and filled in place,
// so a live update never moves focus or closes what is open. The words
// are ext.js's; extensions-ask.js is the page's dialog.

import { api, errorText, signedInBefore } from '../core/api.js';
import { announce } from '../core/announce.js';
import { byId, cloneTpl, h, icon, part, setVar } from '../core/dom.js';
import { on, onReconnect } from '../core/live.js';
import { busy } from '../ui/form.js';
import { region } from '../ui/region.js';
import { current, notify, onStatus, scene, shell } from '../ui/shell.js';
import * as X from '../ext.js';

// The read starts before boot (ARCH §7.4) in a tab that was signed in.
const read = (opts) => api('GET', '/extensions', undefined, opts);
const early = signedInBefore() ? read() : null;
early?.catch(() => {});
const asker = () => import('./extensions-ask.js');
const confirm = (c) => import('../ui/dialog.js').then((m) => m.confirmDialog(c));

let doc = null;
let ctx = X.context(null);
let disks = null; // X.drives of GET /storage, once a drive setting shows
let disksAsked = false;
let skipOnce = false; // the next start leaves extensions out
const cards = new Map(); // id → its <li>
const sigs = new WeakMap(); // <li> → {part: what it was built from}
const controls = new Map(); // "id/key" → a setting's <input> or <select>
const shown = new Map(); // id → the state its card last showed

const enc = encodeURIComponent;
const find = (id) => (doc ? doc.extensions.find((x) => x.id === id) : null);
const fail = async (err) => notify(await errorText(err), { kind: 'error' });

const put = (el, text) => {
  if (el.textContent !== text) el.textContent = text;
};

// changed reports whether a card's part must be rebuilt from v.
function changed(li, key, v) {
  const s = sigs.get(li) || {};
  const json = JSON.stringify(v);
  if (s[key] === json) return false;
  s[key] = json;
  sigs.set(li, s);
  return true;
}

// take shows a document, from a GET, an answer or the event.
function take(d) {
  if (!d || !Array.isArray(d.extensions)) return;
  doc = d;
  if (typeof d.skip_once === 'boolean') skipOnce = d.skip_once;
  render();
}

function render() {
  ctx = X.context(doc);
  const list = byId('ext-list');
  const want = doc.extensions.map((x) => card(x));
  for (const id of [...cards.keys()]) if (!find(id)) cards.delete(id);
  if (!want.length) want.push(h('li', { class: 'region-empty', text: "This version of VaporOS has no extensions." }));
  if (list.children.length !== want.length || want.some((li, i) => list.children[i] !== li)) list.replaceChildren(...want);
  renderRestart();
  renderSkip();
  // A document can come after a failed first read (the event, a reconnect).
  byId('ext-error').hidden = true;
  list.hidden = false;
  if (list.hasAttribute('aria-busy')) region(list).ready();
}

function card(x) {
  let li = cards.get(x.id);
  if (!li) {
    li = cloneTpl('tpl-ext');
    li.dataset.id = x.id;
    cards.set(x.id, li);
  }
  const name = x.name || x.id;
  const c = X.chip(x);
  li.dataset.state = c ? c.state : '';
  put(part(li, 'name'), name);
  const chip = part(li, 'chip');
  chip.hidden = !c;
  if (c) {
    chip.dataset.state = c.state;
    put(chip, c.label);
  }
  const plain = X.plain(x);
  part(li, 'plain').hidden = !plain;
  put(part(li, 'plain'), plain);
  put(part(li, 'from'), X.from(x));
  put(part(li, 'summary'), x.summary || '');
  progress(li, x, name);
  const reason = part(li, 'reason');
  reason.hidden = !(c && c.reason);
  put(reason, c ? c.reason : '');
  const lines = X.lines(x, ctx);
  if (changed(li, 'lines', lines)) {
    part(li, 'lines').replaceChildren(...lines.map((l) => h('li', { class: 'ext-line', text: l.text, dataset: l.tone ? { tone: l.tone } : null })));
  }
  part(li, 'lines').hidden = !lines.length;
  settings(li, x);
  actions(li, x);
  more(li, x);
  say(x, name, c ? c.label : plain);
  return li;
}

// say announces a card whose state changed while the page was open.
function say(x, name, words) {
  const was = shown.get(x.id);
  shown.set(x.id, x.state);
  if (was && was !== x.state && words) announce(`${name}: ${words.replace(/ · \d+%$/, '')}`);
}

function progress(li, x, name) {
  const bar = part(li, 'bar');
  bar.hidden = x.state !== 'installing';
  if (bar.hidden) return;
  const pct = X.progress(x);
  setVar(bar, '--progress', (pct ?? 0) / 100);
  bar.setAttribute('aria-label', `Installing ${name}`);
  // Without a total the bar is indeterminate: no value at all.
  if (pct === null) {
    bar.removeAttribute('aria-valuenow');
    bar.setAttribute('aria-valuetext', 'Getting ready');
  } else {
    bar.setAttribute('aria-valuenow', String(pct));
    bar.setAttribute('aria-valuetext', `${pct} percent`);
  }
}

// ---------------------------------------------------------------- actions

function actions(li, x) {
  const name = x.name || x.id;
  const rm = X.removal(x, ctx);
  const web = x.mounted ? X.webURL(x, location.hostname) : '';
  const acts = x.mounted ? (x.actions || []).filter((a) => a && a.name) : [];
  const retry = x.state === 'needs-attention' && (x.wanted || x.core);
  const sig = [X.canInstall(x), retry, web, x.web && x.web.label, acts.map((a) => [a.name, a.label]), rm];
  const box = part(li, 'actions');
  if (changed(li, 'actions', sig)) {
    const kids = [];
    const sr = (text) => h('span', { class: 'sr-only', text });
    if (X.canInstall(x)) kids.push(h('button', { class: 'btn small', type: 'button', onclick: (e) => install(x.id, e.currentTarget) }, h('span', {}, 'Install', sr(` ${name}`))));
    if (retry) kids.push(h('button', { class: 'btn small', type: 'button', onclick: (e) => retryOne(x.id, e.currentTarget) }, h('span', {}, 'Try again', sr(` to install ${name}`))));
    if (web) {
      kids.push(h('a', { class: 'btn small ghost', href: web, target: '_blank', rel: 'noopener noreferrer' }, icon('external'), h('span', {}, X.webLabel(x), sr(', opens in a new tab'))));
    }
    for (const a of acts) {
      kids.push(h('button', { class: 'btn small ghost', type: 'button', onclick: (e) => act(x.id, a.name, e.currentTarget) }, h('span', {}, a.label || a.name, sr(` (${name})`))));
    }
    if (rm.show) {
      const why = `ext-${x.id}-why`;
      kids.push(h('button', { class: 'btn small ghost', type: 'button', disabled: !!rm.why, 'aria-describedby': rm.why ? why : null, onclick: (e) => remove(x.id, e.currentTarget) }, h('span', {}, 'Remove', sr(` ${name}`))));
      if (rm.why) kids.push(h('p', { class: 'ext-why', id: why, text: rm.why }));
    }
    box.replaceChildren(...kids);
  }
  box.hidden = !box.children.length;
}

// after puts focus back on the card when the button pressed is gone.
function after(id, btn) {
  const li = cards.get(id);
  if (li && btn && !btn.isConnected && (document.activeElement === document.body || !document.activeElement)) part(li, 'name').focus();
}

async function install(id, btn) {
  const x = find(id);
  if (!x) return;
  const also = X.adds(x, ctx).filter((y) => ctx.names[y]).map((y) => ctx.names[y]);
  // One this boot runs, removed until the restart, simply stays.
  const back = !!x.mounted;
  // restart.auto speaks of a restart already needed; a new one may happen
  // by itself.
  const auto = doc.restart && doc.restart.needed ? !!doc.restart.auto : true;
  const when = back ? 'It stays installed.' : auto ? 'It downloads now and is added at the next restart, which VaporOS does by itself when nobody is playing.' : 'It downloads now and is added at the next restart.';
  // needs_password covers what it also installs; the dialog asks anyway
  // when the box wants the password after all.
  const ok = await (await asker()).ask({
    title: `Install ${x.name}?`,
    body: (x.copy && x.copy.install) || x.summary || '',
    can: X.can(x),
    note: [also.length ? `It also installs ${X.and(also)}.` : '', when].filter(Boolean).join(' '),
    password: !!x.needs_password,
    passwordHint: X.passwordHint(x, ctx),
    confirm: 'Install',
    run: async ({ password }) => take(await api('POST', `/extensions/${enc(id)}`, password ? { password } : {})),
  });
  if (!ok) return;
  notify(back ? `${x.name} stays installed.` : `${x.name} is downloading. It's added at the next restart.`, { kind: 'ok' });
  after(id, btn);
}

async function remove(id, btn) {
  const x = find(id);
  if (!x) return;
  const ok = await (await asker()).ask({
    title: `Remove ${x.name}?`,
    body: (x.copy && x.copy.remove) || '',
    note: x.mounted ? 'Its files go at the next restart.' : '',
    purge: true,
    confirm: 'Remove',
    tone: 'danger',
    run: async ({ purge }) => take(await api('DELETE', `/extensions/${enc(id)}${purge ? '?purge=1' : ''}`)),
  });
  if (!ok) return;
  notify(x.mounted ? `${x.name} is removed at the next restart.` : `${x.name} is removed.`, { kind: 'ok' });
  after(id, btn);
}

async function retryOne(id, btn) {
  await busy(btn, async () => take(await api('POST', `/extensions/${enc(id)}/retry`, {})), fail);
  after(id, btn);
}

async function act(id, name, btn) {
  const x = find(id);
  const a = x && (x.actions || []).find((y) => y.name === name);
  if (!a) return;
  const c = a.confirm;
  if (c && !(await confirm({ title: c.title, body: c.body, confirm: c.button || a.label, tone: c.tone === 'danger' ? 'danger' : 'normal' }))) return;
  await busy(btn, async () => {
    take(await api('POST', `/extensions/${enc(id)}/actions/${enc(name)}`, {}));
    notify(`${a.label}: done.`, { kind: 'ok' });
  }, fail);
}

// --------------------------------------------------------------- settings

function settings(li, x) {
  const box = part(li, 'settings');
  const list = (x.settings || []).filter((s) => s && s.key);
  box.hidden = !list.length || !(x.wanted || x.mounted || x.core);
  if (box.hidden) return;
  if (list.some((s) => s.type === 'disk') && !disksAsked) loadDisks();
  const sig = [list.map((s) => [s.key, s.type, s.label, s.help, s.restart, s.choices]), list.some((s) => s.type === 'disk') ? disks : null];
  if (changed(li, 'settings', sig)) box.replaceChildren(...list.map((s) => control(x, s)));
  for (const s of list) value(x, s);
}

const settingID = (x, s) => `ext-${x.id}-${s.key}`;

function control(x, s) {
  const id = settingID(x, s);
  const hint = X.settingHint(s);
  const hintID = hint ? `${id}-hint` : null;
  // A failed or called-off change shows the saved value again.
  const saved = () => {
    const y = find(x.id);
    const t = y && (y.settings || []).find((z) => z.key === s.key);
    return t ? t.value : s.value;
  };
  const save = (v, revert) => saveSetting(x.id, s.key, v, revert);
  if (s.type === 'bool') {
    const input = h('input', { class: 'sys-switch-input', type: 'checkbox', role: 'switch', id, 'aria-describedby': hintID });
    input.addEventListener('change', () => save(input.checked, () => (input.checked = saved() === true)));
    controls.set(`${x.id}/${s.key}`, input);
    return h('div', { class: 'sys-switch-row' },
      h('div', { class: 'sys-switch-text' }, h('label', { class: 'sys-switch-label', for: id, text: s.label || s.key }), hint && h('p', { class: 'field-hint', id: hintID, text: hint })),
      input, h('span', { class: 'sys-switch', 'aria-hidden': 'true' }));
  }
  const sel = h('select', { class: 'input', id, 'aria-describedby': hintID }, options(s));
  sel.addEventListener('change', () => save(sel.value, () => (sel.value = String(saved() ?? ''))));
  controls.set(`${x.id}/${s.key}`, sel);
  const none = s.type === 'disk' && Array.isArray(disks) && !disks.some((d) => !d.system);
  return h('div', { class: 'field' },
    h('label', { class: 'field-label', for: id, text: s.label || s.key }),
    h('div', { class: 'ext-select' }, sel),
    hint && h('p', { class: 'field-hint', id: hintID, text: hint }),
    none && h('p', { class: 'field-hint' }, 'No game drives yet. ', h('a', { href: `${document.documentElement.dataset.base || ''}/system/storage`, text: 'Add one in Storage' }), '.'));
}

function options(s) {
  if (s.type !== 'disk') return (s.choices || []).map((c) => h('option', { value: c, text: X.choiceLabel(c) }));
  if (!Array.isArray(disks)) return [h('option', { value: String(s.value || ''), text: disksAsked && disks === false ? "Couldn't list the drives" : 'Looking at your drives…' })];
  const out = [h('option', { value: '', text: 'Choose a game drive' })];
  // A disk setting holds the drive's folder (CONTRACTS: an absolute path).
  for (const d of disks) out.push(h('option', { value: d.path, text: d.text }));
  if (s.value && !disks.some((d) => d.path === s.value)) out.push(h('option', { value: String(s.value), text: 'A drive that isn\'t connected' }));
  return out;
}

// value shows a setting's saved value, unless a change is being saved.
function value(x, s) {
  const el = controls.get(`${x.id}/${s.key}`);
  if (!el || el.getAttribute('aria-disabled') === 'true') return;
  if (s.type === 'bool') el.checked = s.value === true;
  else el.value = String(s.value ?? '');
}

function loadDisks() {
  disksAsked = true;
  api('GET', '/storage').then((r) => {
    disks = X.drives(r && r.disks);
  }, () => {
    disks = false;
  }).then(() => doc && render());
}

async function saveSetting(id, key, v, revert) {
  const x = find(id);
  const s = x && (x.settings || []).find((y) => y.key === key);
  const el = controls.get(`${id}/${key}`);
  if (!s || !el) return;
  // One change at a time: aria-disabled, not disabled, keeps the focus.
  if (el.getAttribute('aria-disabled') === 'true') {
    revert();
    return;
  }
  el.setAttribute('aria-disabled', 'true');
  const send = async (password) => take(await api('PUT', `/extensions/${enc(id)}/settings`, { settings: { [key]: v }, ...(password ? { password } : {}) }));
  // A setting that feeds kernel module options takes the password with it.
  const withPassword = async () => (await asker()).ask({
    title: `Change ${s.label}?`,
    body: `${x.name} applies it as VaporOS starts.`,
    password: true,
    passwordHint: 'It changes how the system starts, so VaporOS asks for its password.',
    confirm: 'Save',
    run: ({ password }) => send(password),
  });
  try {
    let ok = true;
    if (s.needs_password) {
      ok = await withPassword();
    } else {
      try {
        await send('');
      } catch (err) {
        // The box wants the password after all: ask for it, no dead end.
        if (!X.wantsPassword(err)) throw err;
        ok = await withPassword();
      }
    }
    if (!ok) {
      revert();
      return;
    }
    notify(s.restart ? `Saved. ${X.AFTER_RESTART}` : 'Saved.', { kind: 'ok' });
  } catch (err) {
    revert();
    fail(err);
  } finally {
    el.removeAttribute('aria-disabled');
    // What the box saved, which events may have changed meanwhile.
    const y = find(id);
    const t = y && (y.settings || []).find((z) => z.key === key);
    if (t) value(y, t);
  }
}

// ------------------------------------------------------ what it can do

function more(li, x) {
  const can = X.can(x);
  const dl = X.downloads(x);
  const cav = (x.caveats || []).filter(Boolean);
  if (!changed(li, 'more', [can, dl, cav])) return;
  part(li, 'more').hidden = !can.length && !dl.length && !cav.length;
  put(part(li, 'more-label'), X.moreLabel(can.length, dl.length));
  part(li, 'can').replaceChildren(...can.map((t) => h('li', { class: 'ext-fact', text: t })));
  part(li, 'can-box').hidden = !can.length;
  part(li, 'downloads').replaceChildren(...dl.map((d) => h('li', { class: 'ext-fact', text: d.text, dataset: d.warn ? { tone: 'warning' } : null })));
  part(li, 'downloads-box').hidden = !dl.length;
  part(li, 'caveats').replaceChildren(...cav.map((t) => h('li', { class: 'ext-fact', text: t })));
  part(li, 'caveats-box').hidden = !cav.length;
}

// ---------------------------------------------------------------- restart

let restartShown = false;

function renderRestart() {
  const r = doc.restart || {};
  byId('ext-restart').hidden = !r.needed;
  if (!r.needed) {
    restartShown = false;
    return;
  }
  const text = X.restartText(doc);
  put(byId('ext-restart-text'), text);
  byId('ext-restart-auto').hidden = !r.auto;
  if (!restartShown) announce(text);
  restartShown = true;
}

// ------------------------------------------------- start without extensions

function renderSkip() {
  byId('ext-skip-on').hidden = !skipOnce;
  byId('ext-skip-cancel').hidden = !skipOnce;
  byId('ext-skip').hidden = skipOnce;
}

// skipped takes what POST or DELETE /extensions/skip-once answered: the
// document when the box sends one, else what was asked happened.
function skipped(ans, on) {
  if (ans && Array.isArray(ans.extensions)) take(ans);
  if (!(ans && typeof ans.skip_once === 'boolean')) skipOnce = on;
  renderSkip();
}

function bindSkip() {
  const skip = byId('ext-skip');
  const cancel = byId('ext-skip-cancel');
  skip.disabled = false;
  skip.addEventListener('click', async () => {
    const ok = await confirm({
      title: 'Start once without extensions?',
      body: "The next start leaves every extension out, so Windows games use Valve's Proton. The start after that adds them again.",
      confirm: 'Leave them out',
    });
    if (!ok) return;
    await busy(skip, async () => {
      skipped(await api('POST', '/extensions/skip-once', {}), true);
      notify('The next start leaves extensions out.', { kind: 'ok' });
      if (skip.hidden) cancel.focus();
    }, fail);
  });
  cancel.addEventListener('click', () => busy(cancel, async () => {
    skipped(await api('DELETE', '/extensions/skip-once'), false);
    notify('The next start adds extensions again.', { kind: 'ok' });
    if (cancel.hidden) skip.focus();
  }, fail));
}

// ------------------------------------------------------------------ start

async function failed(err) {
  if (doc) return;
  const text = `Couldn't load the extensions. ${await errorText(err)}`;
  // The event may have brought a document while the words loaded.
  if (doc) return;
  byId('ext-error-text').textContent = text;
  byId('ext-error').hidden = false;
  const list = byId('ext-list');
  list.hidden = true;
  if (list.hasAttribute('aria-busy')) region(list).ready();
}

function listen() {
  // A replay is older than the GET: once that answered, only live news counts.
  on('extensions.state', (d, live) => {
    if (live || !doc) take(d);
  });
  onReconnect(() => read({ passive: true }).then(take, () => {}));
}

async function start() {
  listen();
  await shell('extensions');
  byId('ext-retry').addEventListener('click', () => {
    byId('ext-error').hidden = true;
    byId('ext-list').hidden = false;
    read().then(take, failed);
  });
  byId('ext-restart-go').addEventListener('click', () => scene().then((m) => m.powerAction('reboot', { snap: current() })));
  bindSkip();
  (early || read()).then(take, failed);
  // This page says itself what a restart adds; the row keeps the rest.
  onStatus((snap) => {
    const r = ((snap.restart && snap.restart.reasons) || []).filter((x) => x && x.kind);
    byId('restart-row').toggleAttribute('data-covered', r.length > 0 && r.every((x) => x.kind === 'extensions'));
  });
}

start();
