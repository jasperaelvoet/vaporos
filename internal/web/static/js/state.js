// state.js: what the box is doing, as pure functions of the API's answers
// (spec-cc-screens §2.8-§2.10, §3.2-§3.4, T1). No DOM, storage, location or
// network: jstest/state.test.mjs runs every preset in fixtures/ through
// these and checks its "expect".
//
// A snapshot is the shape of GET /status plus what the page learned itself:
//   { system, sunshine, stream, display, update, power, restart,
//     sunshineError: {status} | null, live: {progress, session} }
// Any part may be missing (not loaded yet, or its request failed).

import { UPDATE_PHASES, busyReason, confirmCopy, pairPrompt, restartRowText } from './copy.js';
import { clock, compareVersions, duration, hostLabel, modeLabel, parseMode, percent, phaseLabel } from './fmt.js';

// T1: the state vocabulary shared with the TV (design/tokens.json).
export const STATES = ['ready', 'streaming', 'updating', 'restart-needed', 'asleep', 'fault', 'installing'];

// snapshotFromStatus turns a GET /status answer into a snapshot.
export function snapshotFromStatus(st) {
  if (!st || typeof st !== 'object') return {};
  const sunshine = st.sunshine ? { ...st.sunshine, session: st.stream || null } : null;
  if (sunshine && st.stream) sunshine.streaming = true;
  return {
    system: st.system || null,
    sunshine,
    stream: st.stream || null,
    display: st.display || null,
    update: st.update || null,
    power: st.power || null,
    restart: st.restart || null,
  };
}

// working reports whether an update progress counts as updating (T3; B1: a
// check never does, and a finished phase never does).
export function working(progress) {
  const p = progress && UPDATE_PHASES[progress.phase];
  return !!(p && p.working);
}

// updateProgress is the progress to show: the GET's while it says busy,
// or a live event's working phase.
export function updateProgress(snap) {
  const u = snap.update || {};
  const live = snap.live && snap.live.progress;
  if (live && working(live)) return live;
  if (u.busy && working(u.progress)) return u.progress;
  return null;
}

// restartReasons: why a restart is wanted, in /status order: update or
// rollback (from next_boot), then display. Without next_boot (an older API),
// a staged version or a held running version stands in (spec-cc-screens §2.10).
export function restartReasons(snap) {
  if (snap.restart && Array.isArray(snap.restart.reasons)) {
    return snap.restart.reasons.map((r) => ({ kind: r.kind, version: r.version || '' }));
  }
  const out = [];
  const u = snap.update;
  if (u) {
    const next = u.next_boot;
    if (next && next.version) {
      out.push({ kind: compareVersions(next.version, u.booted) > 0 ? 'update' : 'rollback', version: next.version });
    } else if (next === undefined) {
      if (u.staged && u.staged.version && u.staged.version !== u.booted) out.push({ kind: 'update', version: u.staged.version });
      else if (u.held && u.held.version && u.held.version === u.booted && u.other_slot) out.push({ kind: 'rollback', version: u.other_slot.version || '' });
    }
  }
  if (snap.display && snap.display.reboot_needed) out.push({ kind: 'display', version: '' });
  return out;
}

// stagedVersion is the version a restart installs, if one is staged.
export function stagedVersion(snap) {
  const u = snap.update || {};
  const r = restartReasons(snap).find((x) => x.kind === 'update');
  if (r) return r.version;
  return u.staged && u.staged.version && u.staged.version !== u.booted ? u.staged.version : '';
}

export function isStreaming(snap) {
  const d = snap.display || {};
  const s = snap.sunshine || {};
  return d.state === 'streaming' || !!s.streaming || !!snap.stream || !!(snap.live && snap.live.session);
}

// session is the stream in progress, from the freshest source.
export function session(snap) {
  return (snap.live && snap.live.session) || snap.stream || (snap.sunshine && snap.sunshine.session) || null;
}

export function pairings(snap) {
  const p = snap.sunshine && snap.sunshine.pairings;
  return Array.isArray(p) ? p : [];
}

const REASON_TITLE = { update: 'Restart to update', rollback: 'Restart to go back', display: 'Restart to finish setup' };
const REASON_DETAIL = {
  update: (r) => `Version ${r.version} is ready.`,
  rollback: (r) => `Version ${r.version} starts on the next restart.`,
  display: () => 'Display changes are waiting.',
};

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
      actions: [{ id: 'stream-details', label: 'Details' }],
    });
  }
  const prog = updateProgress(snap);
  if (prog) {
    const v = prog.version || (u && u.available && u.available.version) || '';
    return hero('updating', 'updating', v ? `Updating to ${v}` : 'Updating', `${phaseLabel(prog.phase)} · ${percent(prog.percent)}%. Keep playing: it switches over when VaporOS restarts.`, {
      progress: percent(prog.percent) / 100,
      actions: [{ id: 'updates', label: 'Details', href: '/system/updates' }],
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
  const reasons = restartReasons(snap);
  if (reasons.length) {
    const first = reasons[0];
    const upd = reasons.find((r) => r.kind === 'update');
    return hero('restart-needed', 'restart-needed', reasons.length === 1 ? REASON_TITLE[first.kind] : 'Restart to finish',
      reasons.map((r) => REASON_DETAIL[r.kind](r)).join(' '), {
        reason: upd ? 'staged' : first.kind === 'rollback' ? 'rollback' : 'display',
        actions: [upd ? { id: 'activate', label: 'Restart to update' } : { id: 'reboot', label: 'Restart now' }],
      });
  }
  const chips = d && d.current ? [modeLabel(d.current)] : [];
  const detail = d && d.state === 'welcome'
    ? 'The monitor shows the welcome screen until a game starts.'
    : `Open Moonlight on any device and pick ${sys.hostname || 'this PC'}.`;
  return hero('ready', 'ready', 'Ready to stream', detail, { chips, mode: (d && d.current) || '', hdr: !!(d && d.hdr) });
}

function hero(key, state, title, detail, extra = {}) {
  const mode = parseMode(extra.mode || '');
  return {
    key,
    state,
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
  if (waiting.length) {
    out.push(card('pair', '', 'attention', pairPrompt(waiting), 'Enter the PIN Moonlight shows.', [{ id: 'pin', label: waiting.length === 1 ? `Enter PIN for ${waiting[0].name}` : 'Enter PIN' }]));
  }
  if (u) {
    const prog = updateProgress(snap);
    if (prog && (h.state === 'streaming' || h.state === 'fault')) {
      out.push(card('update-progress', prog.version || '', 'updating', `Updating to ${prog.version || 'a new version'}`, `${phaseLabel(prog.phase)} · ${percent(prog.percent)}%`, [{ id: 'updates', label: 'Details', href: '/system/updates' }]));
    }
    const staged = stagedVersion(snap);
    if (staged && (h.state === 'streaming' || h.state === 'updating')) {
      out.push(card('update-ready', staged, 'ready', `Version ${staged} is ready`, 'It starts the next time VaporOS restarts.', [{ id: 'activate', label: 'Restart to update' }]));
    }
    const avail = u.available && u.available.version;
    if (avail && compareVersions(avail, u.booted) > 0 && !staged && !u.busy && !prog) {
      const size = u.available.size ? `${Math.round(u.available.size / 1e8) / 10} GB download. ` : '';
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
    const at = new Date(Date.parse(p.web_until) + (p.idle_minutes || 0) * 60e3).toISOString();
    return `Only this page keeps it on. It powers off about ${clock(at, new Date(now))} if nobody plays.`;
  }
  return `Powers off after ${p.idle_minutes} min without anyone playing.`;
}

// canWake reports whether Wake-on-LAN is armed on some wired adapter.
export function canWake(p) {
  return !!(p && Array.isArray(p.wol) && p.wol.some((w) => w.enabled));
}

// wakeTarget picks the adapter the Wake card shows: an armed one with an
// address first, then any armed one, then any that supports it.
export function wakeTarget(wol) {
  const list = Array.isArray(wol) ? wol : [];
  return list.find((w) => w.enabled && w.broadcast) || list.find((w) => w.enabled) || list.find((w) => w.supported) || null;
}

// stripModel is the now-streaming strip and its sheet (spec-cc-screens §2.8).
export function stripModel(snap, now = Date.now()) {
  if (!isStreaming(snap)) return { show: false };
  const ss = session(snap) || {};
  const d = snap.display || {};
  const mode = ss.mode || d.current || '';
  const m = parseMode(mode);
  const client = ss.client || 'a device';
  const parts = [m ? `${m.w}×${m.h}` : '', m ? `${m.hz} Hz` : '', ss.hdr ? 'HDR' : ''].filter(Boolean);
  const name = [`Now streaming: ${client}`, m ? `, ${m.w} by ${m.h} at ${m.hz} hertz` : '', ss.hdr ? ', HDR' : '', '. Show details.'].join('');
  let sinceText = '';
  if (ss.since) {
    const s = Math.max(0, Math.round((now - Date.parse(ss.since)) / 1000));
    if (Number.isFinite(s)) sinceText = s < 60 ? 'just started' : duration(Math.floor(s / 60) * 60);
  }
  return {
    show: true,
    client,
    app: ss.app && ss.app !== 'Steam' ? ss.app : ss.app === 'Steam' ? 'Steam' : '',
    mode,
    modeText: modeLabel(mode),
    hdr: !!ss.hdr,
    since: ss.since || '',
    sinceText,
    line: parts.join(' · '),
    name,
  };
}

// powerPlan decides how Restart or Power off confirms (MASTER-PLAN §3.5's
// hold rule): hold only when the outcome can be undone without walking to
// the PC and interrupts nobody; otherwise the hold is disabled, a tap opens
// the dialog, and the hint names the consequence.
export function powerPlan(kind, snap) {
  const u = snap.update || {};
  const streaming = isStreaming(snap);
  const ss = session(snap) || {};
  const staged = stagedVersion(snap);
  if (u.busy) {
    return { confirm: confirmCopy(kind === 'poweroff' ? 'busy-poweroff' : 'busy-reboot'), hold: false, hint: 'An update is being written: this stops it.' };
  }
  if (kind === 'poweroff') {
    const nowol = snap.power && Array.isArray(snap.power.wol) && !canWake(snap.power);
    const c = confirmCopy(nowol ? 'poweroff-nowol' : 'poweroff');
    if (streaming) return { confirm: c, hold: false, hint: `Ends the stream to ${ss.client || 'the device playing'}.` };
    if (nowol) return { confirm: c, hold: false, hint: 'Wake-on-LAN is off: only the power button starts it again.' };
    return { confirm: c, hold: true, hint: '' };
  }
  const c = staged ? confirmCopy('reboot-staged', { v: staged }) : confirmCopy('reboot');
  if (streaming) return { confirm: c, hold: false, hint: `Ends the stream to ${ss.client || 'the device playing'}.` };
  return { confirm: c, hold: true, hint: staged ? `Restarting also installs version ${staged}.` : '' };
}

// restartRow is the row on Devices, Screen and System (spec-cc-screens
// §2.10): its text and which action its Restart runs.
export function restartRow(snap) {
  const reasons = restartReasons(snap);
  if (!reasons.length) return { show: false };
  const upd = reasons.find((r) => r.kind === 'update');
  const u = snap.update || {};
  const staged = u.staged && u.staged.version;
  return {
    show: true,
    text: restartRowText(reasons),
    action: upd && (!staged || staged === upd.version) ? 'activate' : 'reboot',
    version: upd ? upd.version : '',
  };
}

// hostOf is the address shown in the app bar.
export function hostOf(snap, fallback) {
  return hostLabel(snap.system || {}, fallback);
}
