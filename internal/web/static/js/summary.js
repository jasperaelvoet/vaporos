// summary.js: what Home says about the box (spec-cc-screens §3.2-§3.4):
// the hero, the context cards, the update status line and the power line.
// Pure, like state.js, which it builds on; jstest/state.test.mjs checks it
// against every dev-server preset's "expect".

import { busyReason, pairPrompt } from './copy.js';
import { clock, compareVersions, duration, modeLabel, parseMode, percent, phaseLabel } from './fmt.js';
import { isStreaming, pairings, pendingReasons, session, stagedVersion, updateProgress } from './state.js';

const REASON_TITLE = { rollback: 'Restart to go back', next: 'Restart to switch', display: 'Restart to finish setup', extensions: 'Restart to finish' };
const REASON_DETAIL = {
  rollback: (r) => `Version ${r.version} starts on the next restart.`,
  next: (r) => `Version ${r.version} starts on the next restart.`,
  display: () => 'Display changes are waiting.',
  extensions: () => 'Extension changes are waiting.',
};
const REASON_KEY = { rollback: 'rollback', next: 'rollback', display: 'display', extensions: 'extensions' };
// A kind this page does not know yet (a newer VaporOS) still asks for the
// restart, in general words.
const reasonTitle = (r) => REASON_TITLE[r.kind] || 'Restart to finish';
const reasonDetail = (r) => (REASON_DETAIL[r.kind] ? REASON_DETAIL[r.kind](r) : 'Changes are waiting.');

// IDLE_SOON: from this many seconds before the idle power-off, Home warns.
export const IDLE_SOON = 300;

// idleSoon is the seconds left before an idle power-off when it is close and
// nothing keeps VaporOS on, else 0. shutdown_in comes from power.idle.
export function idleSoon(p) {
  if (!p || !p.idle_shutdown || (p.busy && p.busy.reason)) return 0;
  const left = p.shutdown_in;
  return left > 0 && left <= IDLE_SOON ? left : 0;
}

// heroModel is Home's hero (spec-cc-screens §3.2), highest priority first.
// key names the case (the presets' expect.hero); state is data-state.
export function heroModel(snap) {
  const d = snap.display;
  const s = snap.sunshine;
  const u = snap.update;
  const sys = snap.system || {};
  const err = snap.sunshineError;
  if (!d && !u) return hero('checking', '', 'Checking…', '');

  if (isStreaming(snap)) {
    const ss = session(snap) || {};
    const mode = ss.mode || (d && d.current) || '';
    const chips = [];
    if (mode) chips.push(modeLabel(mode));
    if (ss.hdr) chips.push('HDR');
    return hero('streaming', 'streaming', ss.client ? `Streaming to ${ss.client}` : 'Streaming', 'A game is streaming from this PC.', {
      chips,
      mode,
      hdr: !!ss.hdr,
      // Navigation, not the thing to act on: never white-hot.
      actions: [{ id: 'stream-details', label: 'Details', quiet: true }],
    });
  }
  const prog = updateProgress(snap);
  if (prog) {
    const v = prog.version || (u && u.available && u.available.version) || '';
    return hero('updating', 'updating', v ? `Updating to ${v}` : 'Updating', `${phaseLabel(prog.phase)} · ${percent(prog.percent)}%. Keep playing: it switches over when VaporOS restarts.`, {
      progress: percent(prog.percent) / 100,
      actions: [{ id: 'updates', label: 'Details', href: '/system/updates', quiet: true }],
    });
  }
  if (d && d.profile === 'none') {
    const gpu = sys.gpu && sys.gpu.name;
    return hero('fault-no-gpu', 'fault', 'No supported graphics card',
      gpu ? `${gpu} isn't supported yet. Streaming needs an AMD Radeon GPU.` : 'VaporOS is running, but streaming needs an AMD Radeon GPU.', { reason: 'no-gpu' });
  }
  if (err && err.status === 503) {
    return hero('starting', '', 'Starting up…', 'Streaming is getting ready. This page updates by itself.', { reason: 'starting' });
  }
  if (s && s.running === false) {
    return hero('fault-stopped', 'fault', 'Streaming is stopped', 'The stream server stopped. Restarting it usually fixes this.', {
      reason: 'stopped',
      actions: [{ id: 'sunrestart', label: 'Restart streaming' }],
    });
  }
  if (err && err.status === 502) {
    return hero('fault-unreachable', 'fault', "Streaming isn't answering", 'Restart it, or check the log.', {
      reason: 'unreachable',
      actions: [{ id: 'sunrestart', label: 'Restart streaming' }, { id: 'logs', label: 'View log', href: '/system/logs' }],
    });
  }
  if (err) {
    return hero('fault-unknown', 'fault', "Can't read the streaming status", 'VaporOS may still be starting. This page updates by itself.', { reason: 'unknown' });
  }
  // A staged update is not among these: the box is ready, and a card
  // offers it (MASTER-PLAN §1.3).
  const reasons = pendingReasons(snap);
  if (reasons.length) {
    const first = reasons[0];
    return hero('restart-needed', 'restart-needed', reasons.length === 1 ? reasonTitle(first) : 'Restart to finish',
      [...new Set(reasons.map(reasonDetail))].join(' '), {
        reason: REASON_KEY[first.kind] || 'restart',
        actions: [{ id: 'reboot', label: 'Restart now' }],
      });
  }
  // The idle mode is a tag: the virtual screen is off until a game starts.
  const chips = d && d.current ? [modeLabel(d.current)] : [];
  const waiting = pairings(snap);
  if (waiting.length) {
    // The pair prompt is the state: the word, the heat and the white-hot key
    // agree (the TV keeps its tone and marks the attention).
    return hero('ready', 'ready', pairPrompt(waiting), 'Enter the PIN Moonlight shows. Pairing stops when Moonlight stops waiting.', {
      attention: 'pair',
      chips,
      mode: (d && d.current) || '',
      actions: [{ id: 'pin', label: 'Enter PIN' }],
    });
  }
  const detail = d && d.state === 'welcome'
    ? 'The monitor shows the welcome screen until a game starts.'
    : `Open Moonlight on any device and pick ${sys.hostname || 'this PC'}.`;
  return hero('ready', 'ready', 'Ready to stream', detail, { chips, mode: (d && d.current) || '', hdr: !!(d && d.hdr), idle: idleSoon(snap.power) > 0 });
}

function hero(key, state, title, detail, extra = {}) {
  const mode = parseMode(extra.mode || '');
  return {
    key,
    state,
    attention: extra.attention || '',
    idle: !!extra.idle,
    reason: extra.reason || '',
    title,
    detail,
    chips: extra.chips || [],
    progress: extra.progress ?? null,
    actions: extra.actions || [],
    mode: mode ? { w: mode.w, h: mode.h, hz: mode.hz } : null,
    hdr: !!extra.hdr,
  };
}

// contextCards is Home's list (spec-cc-screens §3.4) in priority order.
// dismissedFailed is the version the viewer dismissed H-C5 for.
export function contextCards(snap, { dismissedFailed = '', liveError = null } = {}) {
  const out = [];
  const u = snap.update;
  const h = heroModel(snap);
  const waiting = pairings(snap);
  if (waiting.length && h.attention !== 'pair') {
    out.push(card('pair', '', 'attention', pairPrompt(waiting), 'Enter the PIN Moonlight shows.', [{ id: 'pin', label: waiting.length === 1 ? `Enter PIN for ${waiting[0].name}` : 'Enter PIN' }]));
  }
  const left = idleSoon(snap.power);
  if (left && h.state === 'ready') {
    out.push(card('idle-soon', '', 'warn', `Powers off in ${duration(left)}`, 'Nobody is playing. Stay awake keeps it on for an hour.', [{ id: 'awake1h', label: 'Stay awake 1 h' }]));
  }
  if (u) {
    const prog = updateProgress(snap);
    if (prog && (h.state === 'streaming' || h.state === 'fault')) {
      out.push(card('update-progress', prog.version || '', 'updating', `Updating to ${prog.version || 'a new version'}`, `${phaseLabel(prog.phase)} · ${percent(prog.percent)}%`, [{ id: 'updates', label: 'Details', href: '/system/updates' }]));
    }
    // H-C3: a staged update is ready, whatever else the hero says.
    const staged = stagedVersion(snap);
    if (staged && !u.busy) {
      out.push(card('update-ready', staged, 'ready', `Version ${staged} is ready`, 'It starts the next time VaporOS restarts.', [{ id: 'activate', label: 'Restart to update' }]));
    }
    const avail = u.available && u.available.version;
    if (avail && compareVersions(avail, u.booted) > 0 && !staged && !u.busy && !prog) {
      const size = u.available.size ? `Up to ${Math.round(u.available.size / 1e8) / 10} GB download. ` : '';
      out.push(card('update-available', avail, '', `Version ${avail} is available`, `${size}Switches over on the next restart.`, [{ id: 'stage', label: 'Download update' }]));
    }
    const failed = (u.failed || []).slice().sort(compareVersions).pop();
    if (failed && compareVersions(failed, u.booted) > 0 && failed !== staged && failed !== dismissedFailed) {
      out.push(card('update-failed', failed, 'fault', `Version ${failed} didn't start`, `VaporOS went back to ${u.booted} by itself and won't install it again.`, [{ id: 'dismiss-failed', label: 'Dismiss' }, { id: 'updates', label: 'Details', href: '/system/updates' }], true));
    }
    const lastErr = String(u.last_error || '');
    if ((lastErr && !lastErr.startsWith('check: ')) || (liveError && liveError.phase === 'error')) {
      out.push(card('update-stopped', '', 'fault', "The update didn't install", lastErr || (liveError && liveError.error) || '', [{ id: 'stage-retry', label: 'Try again' }, { id: 'updates', label: 'Details', href: '/system/updates' }]));
    }
    const auto = u.config && u.config.auto;
    if (lastErr.startsWith('check: ') && auto !== 'off') {
      out.push(card('cant-check', '', 'fault', "Can't check for updates", lastErr.slice(7), [{ id: 'check', label: 'Check now' }]));
    }
  }
  const disk = snap.system && snap.system.disk;
  if (disk && disk.data_total > 0) {
    const used = 1 - disk.data_free / disk.data_total;
    if (used >= 0.85) {
      out.push(card('low-space', '', used >= 0.95 ? 'danger' : 'warn', used >= 0.95 ? 'Almost out of space' : 'Space is running low', `${Math.round(disk.data_free / 1e9)} GB free on the system drive.`, [{ id: 'storage', label: 'Storage', href: '/system/storage' }]));
    }
  }
  const p = snap.power;
  if (p && p.idle_shutdown && Array.isArray(p.wol) && !p.wol.some((w) => w.enabled)) {
    out.push(card('cant-wake', '', 'warn', 'Nothing can wake VaporOS', 'It powers off when idle, but Wake-on-LAN is off.', [{ id: 'power', label: 'Power settings', href: '/system/power' }]));
  }
  return out;
}

function card(id, version, tone, title, body, actions, dismissible = false) {
  return { id, version, tone, title, body, actions, dismissible };
}

// statusLine is Home's glance at updates (spec-cc-screens §3.4), from the
// top-level checked field only (B2).
export function statusLine(u, now = Date.now()) {
  if (!u) return '';
  const head = `VaporOS ${u.booted || ''}`.trim();
  if (u.progress && u.progress.phase === 'check') return `${head} · Checking for updates…`;
  if (u.config && u.config.auto === 'off') return `${head} · Automatic updates are off`;
  if (!u.checked) return `${head} · Not checked yet`;
  const s = Math.round((now - Date.parse(u.checked)) / 1000);
  const when = s < 45 ? 'just now' : s < 3600 ? `${Math.round(s / 60)} min ago` : s < 86400 ? `${Math.round(s / 3600)} h ago` : `${Math.round(s / 86400)} d ago`;
  return `${head} · Up to date · checked ${when}`;
}

// powerLine is the one line about staying on (spec-cc-screens §3.3, T2).
// shutdownIn comes from a live power.idle.
export function powerLine(p, { now = Date.now(), shutdownIn = null } = {}) {
  if (!p) return '';
  if (p.keep_awake_until && Date.parse(p.keep_awake_until) > now) return `Staying awake until ${clock(p.keep_awake_until, new Date(now))}.`;
  const reason = p.busy && p.busy.reason;
  if (reason && !p.busy.web && reason !== 'web UI in use') {
    const words = busyReason(reason, { until: p.keep_awake_until ? clock(p.keep_awake_until, new Date(now)) : '' });
    if (words) return `Staying on: ${words}.`;
  }
  if (!p.idle_shutdown) return 'Always on.';
  const sIn = shutdownIn ?? p.shutdown_in;
  if (sIn > 0) return `Powers off in ${duration(sIn)} if nobody plays.`;
  if (p.busy && p.busy.web && p.web_until) {
    // Whoever reads this has the page open: say what the PC does, not
    // what the page does (spec-cc-screens T2, B5).
    return `On until about ${clock(webOffAt(p), new Date(now))}. Idle power-off starts ${p.idle_minutes} min after this page closes.`;
  }
  return `Powers off after ${p.idle_minutes} min without anyone playing.`;
}

// webOffAt is when idle power-off would switch the PC off if the only thing
// keeping it on, an open control center page, closed now.
export function webOffAt(p) {
  return new Date(Date.parse(p.web_until) + (p.idle_minutes || 0) * 60e3).toISOString();
}
