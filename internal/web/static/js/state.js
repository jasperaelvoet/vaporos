// state.js: what the box is doing, pure (jstest runs it). A snapshot is GET
// /status's shape plus sunshineError and live {progress, session}; any part
// may be missing. Home's summaries are summary.js.

import { UPDATE_PHASES, restartRowText } from './copy.js';
import { compareVersions, duration, hostLabel, modeLabel, parseMode } from './fmt.js';

// T1, shared with the TV (design/tokens.json).
export const STATES = ['ready', 'streaming', 'updating', 'restart-needed', 'asleep', 'fault', 'installing'];

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

// B1: a check or a finished phase never counts as updating.
export function working(progress) {
  const p = progress && UPDATE_PHASES[progress.phase];
  return !!(p && p.working);
}

export function updateProgress(snap) {
  const u = snap.update || {};
  const live = snap.live && snap.live.progress;
  if (live && working(live)) return live;
  if (u.busy && working(u.progress)) return u.progress;
  return null;
}

// restartReasons in /status order. Without next_boot (an older API), a staged
// version or a held running version stands in (spec-cc-screens §2.10).
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

// stagedVersion is the update a restart installs: the staged version, when a
// restart starts it (next_boot names it, or an older API has no next_boot).
// Only update.staged says so: a newer next_boot with nothing staged is a
// forward rollback, which POST /update/activate refuses (409).
export function stagedVersion(snap) {
  const u = snap.update || {};
  const v = u.staged && u.staged.version;
  if (!v || v === u.booted) return '';
  if (u.next_boot && u.next_boot.version && u.next_boot.version !== v) return '';
  return v;
}

// pendingReasons are why a restart is needed (T1 restart-needed): a
// rollback waiting for it, another version it would start that is not the
// staged update (kind "next": a forward rollback), and display changes. A
// staged update is not one: VaporOS is ready, and Home offers it as a card
// (MASTER-PLAN §1.3; the TV's tone agrees).
export function pendingReasons(snap) {
  const staged = stagedVersion(snap);
  const out = [];
  for (const r of restartReasons(snap)) {
    if (r.kind !== 'update') out.push(r);
    else if (!staged || r.version !== staged) out.push({ kind: 'next', version: r.version });
  }
  return out;
}

export function isStreaming(snap) {
  const d = snap.display || {};
  const s = snap.sunshine || {};
  return d.state === 'streaming' || !!s.streaming || !!snap.stream || !!(snap.live && snap.live.session);
}

export function session(snap) {
  return (snap.live && snap.live.session) || snap.stream || (snap.sunshine && snap.sunshine.session) || null;
}

export function pairings(snap) {
  const p = snap.sunshine && snap.sunshine.pairings;
  return Array.isArray(p) ? p : [];
}

export function canWake(p) {
  return !!(p && Array.isArray(p.wol) && p.wol.some((w) => w.enabled));
}

// The Wake card's adapter: armed with an address, then armed, then capable.
export function wakeTarget(wol) {
  const list = Array.isArray(wol) ? wol : [];
  return list.find((w) => w.enabled && w.broadcast) || list.find((w) => w.enabled) || list.find((w) => w.supported) || null;
}

export function stripModel(snap, now = Date.now()) {
  if (!isStreaming(snap)) return { show: false };
  const ss = session(snap) || {};
  const d = snap.display || {};
  const mode = ss.mode || d.current || '';
  const m = parseMode(mode);
  const client = ss.client || 'a device';
  // One way to write a mode everywhere (fmt.modeLabel): 3840 × 2160 · 60 Hz.
  const parts = [m ? modeLabel(mode) : '', ss.hdr ? 'HDR' : ''].filter(Boolean);
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

// The hold rule (MASTER-PLAN §3.5): hold only when the outcome can be undone
// without walking to the PC and interrupts nobody; else a tap asks, and the
// hint names the consequence. confirm is a confirms.js row: {id, vars}.
export function powerPlan(kind, snap) {
  const u = snap.update || {};
  const streaming = isStreaming(snap);
  const ss = session(snap) || {};
  const staged = stagedVersion(snap);
  if (u.busy) {
    return { confirm: { id: kind === 'poweroff' ? 'busy-poweroff' : 'busy-reboot' }, hold: false, hint: 'An update is being written: this stops it.' };
  }
  if (kind === 'poweroff') {
    const nowol = snap.power && Array.isArray(snap.power.wol) && !canWake(snap.power);
    const c = { id: nowol ? 'poweroff-nowol' : 'poweroff' };
    if (streaming) return { confirm: streamConfirm(c, ss), hold: false, hint: `Ends the stream to ${ss.client || 'the device playing'}.` };
    if (nowol) return { confirm: c, hold: false, hint: 'Wake-on-LAN is off: only the power button starts it again.' };
    return { confirm: c, hold: true, hint: '' };
  }
  const c = staged ? { id: 'reboot-staged', vars: { v: staged } } : { id: 'reboot' };
  if (streaming) return { confirm: streamConfirm(c, ss), hold: false, hint: `Ends the stream to ${ss.client || 'the device playing'}.` };
  return { confirm: c, hold: true, hint: staged ? `Restarting also installs version ${staged}.` : '' };
}

// streamConfirm names who is streaming in a restart or power-off confirm, as
// the hint and the Power sheet already do.
function streamConfirm(c, ss) {
  return ss.client ? { id: `${c.id}-stream`, vars: { ...(c.vars || {}), client: ss.client } } : c;
}

// restartRow is "Restart to finish" (spec-cc-screens §2.10): only for a
// pending rollback or display changes, never for a staged update alone. Its
// Restart is a plain restart, which also starts a staged update.
export function restartRow(snap) {
  const reasons = pendingReasons(snap);
  if (!reasons.length) return { show: false };
  return { show: true, text: restartRowText(reasons), action: 'reboot', version: '' };
}

export function hostOf(snap, fallback) {
  return hostLabel(snap.system || {}, fallback);
}
