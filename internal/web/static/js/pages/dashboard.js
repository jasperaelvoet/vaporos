// Home: what the box is doing right now, plus the few things people do most.
import {
  boot, api, on, onReconnect, byId, h, fill, icon, badge, kv, setText, toast, busy, powerAction,
  bytes, duration, ago, clock, modeLabel, percent, phaseLabel,
} from '../lib.js';

const state = { system: null, sun: null, update: null, display: null, power: null, progress: null, idle: null };

// load fetches one source; a failure leaves it null so the other cards still
// render (a 401 has already sent the visitor to sign in).
async function load(key, path) {
  try {
    state[key] = await api('GET', path);
  } catch {
    state[key] = null;
  }
}

async function refresh() {
  await Promise.all([
    load('system', '/system'), load('sun', '/sunshine'), load('update', '/update'),
    load('display', '/display'), load('power', '/power'),
  ]);
  renderAll();
}

function renderAll() {
  renderStatus();
  renderUpdate();
  renderSystem();
  renderDisk();
  renderPower();
}

// updating reports whether an update download or write is in flight.
function updating() {
  const p = state.progress;
  if (!p) return false;
  const phase = String(p.phase || '').toLowerCase();
  return !['done', 'staged', 'error', 'failed', 'idle'].includes(phase);
}

function chip(name, text, accent = false) {
  return h('span', { class: accent ? 'chip chip-accent' : 'chip' }, icon(name), text);
}

function renderStatus() {
  const { sun, display, system } = state;
  let tone = 'idle';
  let title = 'Ready to stream';
  let detail = 'Open Moonlight on any device and pick this PC.';
  const chips = [];
  const actions = [];

  if (sun && sun.streaming) {
    const s = sun.session || {};
    tone = 'streaming';
    title = s.client ? `Streaming to ${s.client}` : 'Streaming';
    detail = 'A Moonlight device is playing right now.';
    if (s.mode) chips.push(chip('monitor', modeLabel(s.mode)));
    if (s.hdr) chips.push(chip('hdr', 'HDR', true));
  } else if (updating()) {
    const p = state.progress;
    tone = 'updating';
    title = p.version ? `Updating to ${p.version}` : 'Updating';
    detail = `${phaseLabel(p.phase)} · ${percent(p.percent)}%. You can keep playing; it applies on the next restart.`;
  } else if (display && display.profile === 'none') {
    tone = 'warn';
    title = 'No supported graphics card';
    detail = 'VaporOS is running, but streaming needs an AMD Radeon GPU.';
  } else if (sun && !sun.running) {
    tone = 'warn';
    title = "Streaming isn't running";
    detail = 'Sunshine stopped. Restarting it usually fixes this.';
    const b = h('button', { class: 'btn btn-primary', type: 'button' }, icon('restart'), 'Restart Sunshine');
    b.addEventListener('click', () => busy(b, async () => {
      await api('POST', '/sunshine/restart', {});
      toast('Sunshine is restarting.', 'ok');
      setTimeout(refresh, 3000);
    }));
    actions.push(b);
  } else if (!sun) {
    tone = 'warn';
    title = "Can't read the streaming status";
    detail = 'VaporOS may still be starting. This page updates by itself.';
  } else if (display && display.state === 'welcome') {
    tone = 'welcome';
    detail = 'The monitor shows the welcome screen until a game starts from Moonlight.';
  }
  if (tone !== 'streaming' && display && display.current) chips.push(chip('monitor', modeLabel(display.current)));

  byId('status').dataset.tone = tone;
  setText('status-host', system ? system.mdns || system.hostname || 'VaporOS' : 'VaporOS');
  setText('status-title', title);
  setText('status-detail', detail);
  fill(byId('status-chips'), chips);
  fill(byId('status-actions'), actions);
  const bar = byId('status-progress');
  bar.hidden = tone !== 'updating';
  if (!bar.hidden) bar.value = percent(state.progress.percent);
  byId('pairing-banner').hidden = !(sun && sun.pending_pairing);
}

function renderUpdate() {
  const body = byId('update-body');
  const u = state.update;
  if (!u) {
    fill(body, h('p', { class: 'muted', text: "Couldn't load the update status." }));
    return;
  }
  const booted = u.booted || '';
  const staged = u.staged && u.staged.version && u.staged.version !== booted ? u.staged : null;
  const avail = u.available && u.available.version && u.available.version !== booted
    && !(staged && staged.version === u.available.version) ? u.available : null;
  const rows = [];
  if (updating()) {
    const p = state.progress;
    const bar = h('progress', { max: '100', 'aria-label': 'Update progress' });
    bar.value = percent(p.percent);
    rows.push(h('p', {}, h('strong', { text: `${phaseLabel(p.phase)} ${p.version || ''}` }), ` · ${percent(p.percent)}%`), bar);
  } else if (staged) {
    const b = h('button', { class: 'btn btn-primary btn-sm', type: 'button' }, icon('power'), 'Restart now');
    b.addEventListener('click', () => powerAction('activate', b));
    rows.push(
      h('p', {}, h('strong', { text: `Version ${staged.version} is ready.` }), ' It starts the next time VaporOS restarts.'),
      h('div', { class: 'actions' }, b));
  } else if (avail) {
    const b = h('button', { class: 'btn btn-primary btn-sm', type: 'button' }, icon('download'), 'Download');
    b.addEventListener('click', () => busy(b, async () => {
      await api('POST', '/update/stage', { version: avail.version });
      state.progress = { phase: 'download', percent: 0, version: avail.version };
      renderAll();
    }));
    rows.push(
      h('p', {}, h('strong', { text: `Version ${avail.version} is available` }), avail.size ? ` (${bytes(avail.size)}).` : '.'),
      h('div', { class: 'actions' }, b));
  } else {
    const b = h('button', { class: 'btn btn-sm', type: 'button' }, icon('restart'), 'Check now');
    b.addEventListener('click', () => busy(b, async () => {
      const r = await api('POST', '/update/check', {});
      if (r && r.available) {
        state.update.available = r.available;
      } else {
        toast('VaporOS is up to date.', 'ok');
        if (state.update.available) state.update.available.checked = new Date().toISOString();
      }
      renderUpdate();
    }));
    const checked = u.available && u.available.checked ? `Checked ${ago(u.available.checked)}.` : '';
    rows.push(
      h('p', {}, h('strong', { text: 'Up to date.' }), ' ', h('span', { class: 'muted', text: checked })),
      h('div', { class: 'actions' }, b));
  }
  rows.unshift(h('p', { class: 'muted small' }, 'Running ', h('span', { class: 'mono', text: booted || 'unknown' })));
  if (u.failed && u.failed.length) {
    rows.push(h('p', { class: 'small' }, badge('Rolled back', 'warn'), ` ${u.failed[u.failed.length - 1]} didn't start, so VaporOS kept the previous version.`));
  }
  fill(body, rows);
}

function renderSystem() {
  const s = state.system;
  const dl = byId('system-kv');
  if (!s) {
    fill(dl, h('p', { class: 'muted', text: "Couldn't load system information." }));
    return;
  }
  const gpu = s.gpu || {};
  const ips = (s.ips || []).filter((ip) => !/^fe80:/i.test(ip)); // link-local is no use to type
  const temps = (s.temps || []).filter((t) => Number.isFinite(Number(t.c))).sort((a, b) => b.c - a.c).slice(0, 2);
  kv(dl, [
    ['Address', s.mdns || (s.hostname ? `${s.hostname}.local` : '')],
    ips.length ? ['IP', ips.join(', ')] : null,
    ['Graphics', gpu.name ? h('span', {}, gpu.name, ' ', gpu.supported ? badge('Supported', 'ok') : badge('Not supported', 'warn')) : 'None detected'],
    s.cpu ? ['Processor', s.cpu] : null,
    ['Up for', duration(s.uptime_s)],
    temps.length ? ['Temperature', temps.map((t) => `${t.name} ${Math.round(t.c)} °C`).join(' · ')] : null,
  ]);
}

function renderDisk() {
  const body = byId('disk-body');
  const d = state.system && state.system.disk;
  if (!d || !d.data_total) {
    fill(body, h('p', { class: 'muted', text: 'No storage information.' }));
    return;
  }
  const used = Math.max(0, d.data_total - d.data_free);
  const pct = percent((used / d.data_total) * 100);
  const bar = h('progress', { class: `usage-bar${pct >= 95 ? ' full' : pct >= 85 ? ' high' : ''}`, max: '100', 'aria-label': 'System drive used' });
  bar.value = pct;
  fill(body,
    h('div', { class: 'meter-line' }, h('span', { class: 'big-number', text: `${bytes(d.data_free)} free` }), h('span', { class: 'muted', text: `of ${bytes(d.data_total)}` })),
    bar,
    h('p', { class: 'muted small', text: 'On the system drive. Games on other drives are under Storage.' }));
}

function renderPower() {
  const p = state.power;
  const note = [];
  if (p) {
    if (p.keep_awake_until && Date.parse(p.keep_awake_until) > Date.now()) note.push(`Staying awake until ${clock(p.keep_awake_until)}.`);
    else if (p.busy && p.busy.reason) note.push(`Staying awake: ${p.busy.reason}.`);
    else if (p.idle_shutdown && state.idle && Number(state.idle.shutdown_in) > 0) note.push(`Powers off in ${duration(state.idle.shutdown_in)} if nobody plays.`);
    else if (p.idle_shutdown) note.push(`Powers off after ${p.idle_minutes} min without anyone playing.`);
  }
  setText('power-note', note.join(' '));
}

function wire() {
  const awake = byId('quick-awake');
  awake.addEventListener('click', () => busy(awake, async () => {
    await api('POST', '/power/keep-awake', { minutes: 60 });
    toast('VaporOS stays on for the next hour.', 'ok');
    await load('power', '/power');
    renderPower();
  }));
  const restart = byId('quick-restart');
  restart.addEventListener('click', () => powerAction('reboot', restart));
  const off = byId('quick-off');
  off.addEventListener('click', () => powerAction('poweroff', off));

  const reloadSun = async () => {
    await load('sun', '/sunshine');
    renderStatus();
  };
  on('session.begin', reloadSun);
  on('session.end', reloadSun);
  on('pairing.pending', () => {
    byId('pairing-banner').hidden = false;
  });
  on('update.progress', (p) => {
    state.progress = p;
    renderStatus();
    renderUpdate();
    const phase = String(p.phase || '').toLowerCase();
    if (['done', 'staged', 'error', 'failed'].includes(phase)) {
      setTimeout(async () => {
        await load('update', '/update');
        renderAll();
      }, 800);
    }
  });
  on('update.state', (s) => {
    state.update = { ...(state.update || {}), ...s };
    renderUpdate();
  });
  on('display.changed', async () => {
    await load('display', '/display');
    renderStatus();
  });
  on('power.idle', (i) => {
    state.idle = i;
    renderPower();
  });
  onReconnect(refresh);
}

async function main() {
  await boot('dashboard');
  wire();
  await refresh();
}

main();
