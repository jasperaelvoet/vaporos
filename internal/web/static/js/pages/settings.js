// pages/settings.js: System › Settings (spec-cc-screens §10). The name comes
// from the shell's GET /status, SSH from GET /ssh; live data never
// overwrites what someone is typing (R4). A rename closes the event stream
// the moment it is done, because the old name answers 421 within about 2 s,
// and hands off to the new address and the IP with a sign-in at each (the
// session cookie is host-only). Passwords are never kept as drafts.

import { api, errorText, url } from '../core/api.js';
import { byId } from '../core/dom.js';
import { onReconnect, pause } from '../core/live.js';
import { getJSON, remove, setJSON } from '../core/store.js';
import { region } from '../ui/region.js';
import { notify, onStatus, shell } from '../ui/shell.js';

const NAME_DRAFT = 'vos-drafts:settings#name-form';
const MISMATCH = "The passwords don't match.";

let L = null; // validate.js and ui/form.js
let sys = null;
let saved = ''; // the name the box answers to
let renaming = false;
let sshForm = null;
let ssh = null;

const dialog = () => import('../ui/dialog.js');

// -------------------------------------------------------------- the name

function syncRename() {
  const same = L.cleanHostname(byId('hostname').value) === saved;
  byId('rename').disabled = same || renaming;
  byId('rename-note').hidden = !same;
}

function renderName() {
  if (!sys || renaming) return;
  saved = sys.hostname || '';
  const input = byId('hostname');
  const form = byId('name-form');
  if (form.dataset.dirty !== 'true') {
    const draft = getJSON('session', NAME_DRAFT);
    input.value = draft && draft.hostname && draft.hostname !== saved ? draft.hostname : saved;
    form.dataset.dirty = String(input.value !== saved);
  }
  input.disabled = false;
  syncRename();
  if (form.hasAttribute('aria-busy')) region(form).ready();
  connectHint();
}

function firstIPv4(s) {
  return ((s && s.ips) || []).find((ip) => /^\d{1,3}(\.\d{1,3}){3}$/.test(ip) && !ip.startsWith('169.254.')) || '';
}

async function rename(e) {
  e.preventDefault();
  const input = byId('hostname');
  const line = byId('name-form-error');
  line.textContent = '';
  const name = L.cleanHostname(input.value);
  const err = L.hostnameError(name);
  L.fieldError(input, err);
  if (err) {
    input.focus();
    return;
  }
  if (name === saved) return;
  if (!(await (await dialog()).confirmDialog({ id: 'rename', vars: { new: name } }))) return;
  const btn = byId('rename');
  renaming = true;
  btn.setAttribute('aria-busy', 'true');
  syncRename();
  try {
    await api('PUT', '/system/hostname', { hostname: name });
  } catch (e2) {
    renaming = false;
    btn.removeAttribute('aria-busy');
    syncRename();
    if (e2.status === 400) {
      L.fieldError(input, await errorText(e2));
      input.focus();
    } else {
      line.textContent = `Couldn't rename: ${await errorText(e2)}`;
    }
    return;
  }
  // Before the old name starts answering 421: nothing may call it again.
  pause();
  remove('session', NAME_DRAFT);
  handoff(saved, name);
}

// handoff: this address is gone; the new name and the IP each need a new
// sign-in. Two links, no automatic jump: .local does not resolve everywhere.
function handoff(old, name) {
  const port = location.port ? `:${location.port}` : '';
  byId('handoff-old').textContent = `${old}.local`;
  byId('handoff-new').textContent = `${name}.local`;
  byId('handoff-title').textContent = `Renamed to ${name}`;
  const byName = byId('handoff-name');
  byName.href = `http://${name}.local${port}/login`;
  byName.textContent = `Open ${name}.local`;
  const ip = firstIPv4(sys);
  const byIP = byId('handoff-ip');
  if (ip) {
    byIP.href = `http://${ip}${port}/login`;
    byIP.textContent = `Open ${ip}`;
  }
  byIP.hidden = !ip;
  const dlg = byId('handoff');
  // Nothing on this page works any more: Esc does not go back to it.
  dlg.addEventListener('cancel', (ev) => ev.preventDefault());
  dlg.showModal();
  byId('handoff-title').focus();
}

// ----------------------------------------------------------- the password

function countdown(btn, secs) {
  let n = Math.max(1, Math.ceil(Number(secs) || 30));
  const label = btn.textContent;
  btn.disabled = true;
  const tick = () => {
    if (n <= 0) {
      btn.textContent = label;
      btn.disabled = false;
      byId('pw-form-error').textContent = '';
      return;
    }
    btn.textContent = `Try again in ${n} s`;
    n -= 1;
    setTimeout(tick, 1000);
  };
  tick();
}

async function changePassword(e) {
  e.preventDefault();
  const [cur, pw, again] = [byId('pw-current'), byId('pw-new'), byId('pw-again')];
  const line = byId('pw-form-error');
  line.textContent = '';
  const errs = {};
  if (!cur.value) errs[cur.id] = 'Enter the current password.';
  const perr = L.passwordError(pw.value, again.value);
  if (perr) errs[perr === MISMATCH ? again.id : pw.id] = perr;
  let first = null;
  for (const el of [cur, pw, again]) {
    L.fieldError(el, errs[el.id] || '');
    first = first || (errs[el.id] ? el : null);
  }
  if (first) {
    first.focus();
    return;
  }
  const btn = byId('pw-save');
  if (btn.getAttribute('aria-busy') === 'true') return;
  // Busy, not disabled: a disabled key would drop focus to <body> (WCAG 2.4.3).
  btn.setAttribute('aria-busy', 'true');
  btn.setAttribute('aria-disabled', 'true');
  try {
    // A 401 here is the server refusing the current password: this page
    // was signed in a moment ago, so it is no reason to leave it.
    await api('POST', '/auth/password', { current: cur.value, new: pw.value }, { quiet401: true });
    byId('pw-form').reset();
    for (const b of document.querySelectorAll('[data-reveal]')) reveal(b, false);
    notify('Password changed. Other devices are signed out.', { kind: 'ok' });
  } catch (err) {
    if (err.status === 401 || err.status === 403) {
      L.fieldError(cur, "That's not the current password.");
      cur.focus();
    } else if (err.status === 400) {
      L.fieldError(pw, await errorText(err));
      pw.focus();
    } else if (err.status === 429) {
      line.textContent = 'Too many tries. Wait until the button is ready again.';
      countdown(btn, err.retryAfter);
    } else if (err.status === 503) {
      line.textContent = 'VaporOS is busy checking other sign-ins. Try again in a moment.';
    } else {
      line.textContent = await errorText(err);
    }
  } finally {
    btn.removeAttribute('aria-busy');
    btn.removeAttribute('aria-disabled');
  }
}

function reveal(btn, show) {
  byId(btn.dataset.reveal).type = show ? 'text' : 'password';
  btn.setAttribute('aria-pressed', String(show));
}

// ------------------------------------------------------------------ SSH

function connectHint() {
  byId('ssh-command').textContent = `ssh vapor@${saved || 'vapor'}.local`;
  byId('ssh-connect').hidden = !(ssh && ssh.enabled);
}

function renderSSH(s) {
  ssh = { enabled: !!s.enabled, keys: Array.isArray(s.keys) ? s.keys : [] };
  if (!sshForm.isDirty()) {
    byId('ssh-on').checked = ssh.enabled;
    byId('ssh-keys').value = ssh.keys.join('\n');
  }
  byId('ssh-on').disabled = false;
  byId('ssh-keys').disabled = false;
  connectHint();
}

async function loadSSH(passive = false) {
  const form = byId('ssh-form');
  try {
    renderSSH(await api('GET', '/ssh', undefined, { passive }));
    byId('ssh-error').hidden = true;
    form.hidden = false;
  } catch (err) {
    byId('ssh-error-text').textContent = `Couldn't load SSH settings. ${await errorText(err)}`;
    byId('ssh-error').hidden = false;
    form.hidden = !ssh;
  } finally {
    if (form.hasAttribute('aria-busy')) region(form).ready();
  }
}

function sshErrors() {
  const keys = L.sshKeys(byId('ssh-keys').value);
  for (const k of keys) {
    const err = L.sshKeyError(k);
    if (err) return { 'ssh-keys': `${err} It starts “${k.slice(0, 24)}”.` };
  }
  if (byId('ssh-on').checked && !keys.length) return { 'ssh-keys': 'Add at least one public key. SSH never accepts passwords.' };
  return {};
}

async function saveSSH() {
  const enabled = byId('ssh-on').checked;
  const res = await api('PUT', '/ssh', { enabled, keys: L.sshKeys(byId('ssh-keys').value) });
  // The reply is the normalised state: show exactly what was kept.
  byId('ssh-on').checked = !!res.enabled;
  byId('ssh-keys').value = (res.keys || []).join('\n');
  ssh = { enabled: !!res.enabled, keys: res.keys || [] };
  connectHint();
  notify(res.enabled ? 'SSH is on for the keys listed.' : 'SSH is off.', { kind: 'ok' });
}

// --------------------------------------------------------------- start

function start() {
  shell('settings').then(async () => {
    L = await Promise.all([import('../validate.js'), import('../ui/form.js')]).then((m) => Object.assign({}, ...m));
    const name = byId('name-form');
    name.addEventListener('submit', rename);
    byId('hostname').addEventListener('input', (e) => {
      L.fieldError(e.target, '');
      const differs = L.cleanHostname(e.target.value) !== saved;
      name.dataset.dirty = String(differs);
      if (differs) setJSON('session', NAME_DRAFT, { hostname: e.target.value });
      else remove('session', NAME_DRAFT);
      syncRename();
    });
    byId('pw-form').addEventListener('submit', changePassword);
    for (const b of document.querySelectorAll('[data-reveal]')) b.addEventListener('click', () => reveal(b, b.getAttribute('aria-pressed') !== 'true'));
    sshForm = L.bindForm(byId('ssh-form'), {
      validate: sshErrors,
      submit: saveSSH,
      fieldFor: (err) => (err.status === 400 ? 'ssh-keys' : ''),
      draft: true,
      saved: '',
    });
    if (!sshForm.isDirty()) sshForm.setDirty(false);
    byId('ssh-retry').addEventListener('click', () => loadSSH());
    import('../ui/clipboard.js').then((m) => m.bindCopy(byId('ssh-connect')));
    loadSSH();
    onStatus((s) => {
      if (!s.system) return;
      sys = s.system;
      renderName();
    });
    // The shell's GET /status is in flight: share it to learn if it fails.
    api('GET', '/status', undefined, { share: true }).catch(async (err) => {
      if (sys) return;
      byId('name-form-error').textContent = `Couldn't load the name. ${await errorText(err)}`;
      region(byId('name-form')).ready();
    });
    onReconnect(() => loadSSH(true));
  });
}

// /advanced#logs arrives here with its fragment (spec-cc-screens §1.2).
if (location.hash === '#logs') location.replace(url('/system/logs') + location.search);
else start();
