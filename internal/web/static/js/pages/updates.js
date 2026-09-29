// Updates: check, download (stage) into the idle slot, restart into it, or
// roll back to the other slot. Progress arrives as update.progress events.
import {
  boot, api, on, onReconnect, byId, h, fill, kv, setText, toast, busy, onSubmit, invalid,
  confirmDialog, powerAction, bytes, ago, percent, phaseLabel,
} from '../lib.js';

let u = null;
let progress = null;

async function load() {
  try {
    u = await api('GET', '/update');
  } catch (e) {
    setText('ustate-title', "Couldn't load updates");
    setText('ustate-detail', e.message);
    return;
  }
  render();
  renderSettings();
}

function inFlight() {
  if (!progress) return false;
  return !['done', 'staged', 'error', 'failed', 'idle'].includes(String(progress.phase || '').toLowerCase());
}

function staged() {
  return u.staged && u.staged.version && u.staged.version !== u.booted ? u.staged : null;
}

function available() {
  const a = u.available;
  if (!a || !a.version || a.version === u.booted) return null;
  const s = staged();
  return s && s.version === a.version ? null : a;
}

function render() {
  if (!u) return;
  const s = staged();
  const a = available();
  const checked = u.available && u.available.checked ? `Checked ${ago(u.available.checked)}.` : '';
  let title;
  let detail;
  if (inFlight()) {
    title = `Installing version ${progress.version || (a && a.version) || ''}`.trim();
    detail = 'You can keep playing. It starts on the next restart.';
  } else if (s) {
    title = `Version ${s.version} is ready`;
    detail = `Restart to start using it${s.at ? ` (prepared ${ago(s.at)})` : ''}. If it fails to start, VaporOS goes back by itself.`;
  } else if (a) {
    title = `Version ${a.version} is available`;
    detail = [a.size ? `Download size ${bytes(a.size)}.` : '', checked].filter(Boolean).join(' ');
  } else {
    title = 'VaporOS is up to date';
    detail = [`Version ${u.booted || 'unknown'}.`, checked].filter(Boolean).join(' ');
  }
  setText('ustate-title', title);
  setText('ustate-detail', detail);
  const ch = (u.config && u.config.channel) || '';
  const b = byId('ustate-badge');
  b.hidden = !ch;
  b.className = ch === 'main' ? 'badge' : 'badge badge-warn';
  b.textContent = ch ? `Channel ${ch}` : '';

  renderProgress();
  byId('stage-btn').hidden = !a || inFlight();
  byId('activate-btn').hidden = !s || inFlight();
  byId('check-btn').disabled = inFlight();

  const err = u.last_error;
  byId('ulast-error').hidden = !err;
  setText('ulast-error-text', err ? `The last update attempt failed: ${err}` : '');

  const other = u.other_slot && u.other_slot.version;
  kv(byId('versions'), [
    ['Running', h('span', { class: 'mono', text: u.booted || 'unknown' })],
    ['Previous', other ? h('span', { class: 'mono', text: other }) : 'None'],
    s ? ['Next restart', h('span', { class: 'mono', text: s.version })] : null,
  ]);
  const canRollback = !!other && other !== u.booted;
  byId('rollback-btn').disabled = !canRollback || inFlight();
  setText('rollback-hint', canRollback
    ? `Roll back starts version ${other} on the next restart.`
    : 'There is no previous version to go back to yet.');

  const failed = u.failed || [];
  byId('failed-card').hidden = !failed.length;
  fill(byId('failed'), failed.map((v) => h('li', { class: 'chip mono', text: v })));
}

function renderProgress() {
  const box = byId('uprogress');
  box.hidden = !inFlight();
  if (box.hidden) return;
  const pct = percent(progress.percent);
  setText('uprogress-phase', phaseLabel(progress.phase));
  setText('uprogress-pct', `${pct}%`);
  byId('uprogress-bar').value = pct;
  setText('uprogress-bytes', progress.total ? `${bytes(progress.bytes || 0)} of ${bytes(progress.total)}` : '');
}

function renderSettings() {
  const form = byId('usettings');
  if (form.contains(document.activeElement)) return;
  const cfg = u.config || {};
  byId('auto').checked = cfg.auto !== 'off';
  byId('channel').value = cfg.channel || 'main';
}

function wire() {
  const check = byId('check-btn');
  check.addEventListener('click', () => busy(check, async () => {
    const r = await api('POST', '/update/check', {});
    if (r && r.available) {
      u.available = r.available;
    } else {
      if (u.available) u.available = { ...u.available, checked: new Date().toISOString() };
      toast('VaporOS is up to date.', 'ok');
    }
    render();
  }));

  const stage = byId('stage-btn');
  stage.addEventListener('click', () => busy(stage, async () => {
    const a = available();
    await api('POST', '/update/stage', a ? { version: a.version } : {});
    progress = { phase: 'download', percent: 0, version: a && a.version };
    render();
  }));

  const activate = byId('activate-btn');
  activate.addEventListener('click', () => powerAction('activate', activate));

  const rollback = byId('rollback-btn');
  rollback.addEventListener('click', async () => {
    const other = u.other_slot && u.other_slot.version;
    const ok = await confirmDialog({
      title: `Go back to version ${other}?`,
      body: 'VaporOS starts the previous version from the next restart on. Your games and settings stay.',
      confirm: 'Roll back',
    });
    if (!ok) return;
    const done = await busy(rollback, async () => {
      await api('POST', '/update/rollback', {});
      return true;
    });
    if (!done) return;
    await load();
    const now = await confirmDialog({
      title: 'Restart now?',
      body: `Version ${other} starts after a restart. Streams in progress stop.`,
      confirm: 'Restart now',
    });
    if (now) powerAction('reboot', null, { confirmed: true });
    else toast(`Version ${other} starts the next time VaporOS restarts.`, 'ok');
  });

  onSubmit(byId('usettings'), async () => {
    const ch = byId('channel');
    const channel = ch.value.trim();
    if (!/^[A-Za-z0-9._/-]{1,128}$/.test(channel)) return invalid(ch, 'Use a branch name like main.');
    const body = { channel, auto: byId('auto').checked ? 'stage' : 'off' };
    await api('PUT', '/update/settings', body);
    u.config = { ...(u.config || {}), ...body };
    toast(channel === 'main' ? 'Saved.' : `Saved. VaporOS now follows the ${channel} test channel.`, 'ok');
    render();
    return true;
  });

  on('update.progress', (p) => {
    progress = p;
    render();
    if (!inFlight()) setTimeout(load, 800);
  });
  on('update.state', (s) => {
    u = { ...(u || {}), ...s };
    render();
  });
  onReconnect(load);
}

async function main() {
  await boot('updates');
  wire();
  await load();
}

main();
