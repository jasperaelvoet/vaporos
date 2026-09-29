// Streaming: Sunshine's state, the current stream, and the few settings
// worth changing (the API whitelists them).
import {
  boot, api, on, onReconnect, byId, h, fill, badge, kv, toast, busy, onSubmit, invalid,
  confirmDialog, modeLabel,
} from '../lib.js';

let sun = null;
let settings = null;

async function loadStatus() {
  try {
    sun = await api('GET', '/sunshine');
  } catch {
    sun = null;
  }
  renderStatus();
}

function renderStatus() {
  const b = byId('sun-badge');
  if (!sun) {
    b.className = 'badge badge-warn';
    b.textContent = 'Unknown';
    kv(byId('sun-kv'), [['Status', "Couldn't reach Sunshine's status."]]);
    fill(byId('session-body'), h('p', { class: 'muted', text: '–' }));
    return;
  }
  b.className = sun.running ? 'badge badge-ok' : 'badge badge-danger';
  b.textContent = sun.running ? 'Running' : 'Stopped';
  kv(byId('sun-kv'), [
    ['Version', sun.version ? h('span', { class: 'mono', text: sun.version }) : ''],
    ['Streaming', sun.streaming ? 'Yes' : 'No'],
    ['Pairing', sun.pending_pairing ? h('a', { href: '/pair', text: 'A device is waiting for its PIN' }) : 'No requests'],
  ]);
  const s = sun.streaming && sun.session;
  if (!s) {
    fill(byId('session-body'), h('p', { class: 'muted', text: 'Nobody is streaming right now.' }));
    return;
  }
  const dl = h('dl', { class: 'kv' });
  kv(dl, [
    ['Device', s.client || 'Unknown'],
    ['Mode', s.mode ? modeLabel(s.mode) : ''],
    ['HDR', s.hdr ? badge('On', 'accent') : 'Off'],
  ]);
  fill(byId('session-body'), dl);
}

// ensureOption keeps a value the list doesn't know selectable, so saving
// the form never silently changes it.
function ensureOption(select, value) {
  if (value == null || [...select.options].some((o) => o.value === value)) return;
  select.append(h('option', { value, text: value }));
}

async function loadSettings() {
  try {
    settings = await api('GET', '/sunshine/settings');
  } catch (e) {
    toast(`Couldn't load the stream settings: ${e.message}`, 'error');
    return;
  }
  const enc = byId('encoder');
  ensureOption(enc, settings.encoder || '');
  enc.value = settings.encoder || '';
  const pad = byId('gamepad');
  ensureOption(pad, settings.gamepad || 'auto');
  pad.value = settings.gamepad || 'auto';
  byId('bitrate').value = Math.round(Number(settings.bitrate_kbps_max || 0) / 1000);
  // audio_sink is optional in the API: only offer it when it is there.
  const hasSink = Object.prototype.hasOwnProperty.call(settings, 'audio_sink');
  byId('audio-field').hidden = !hasSink;
  if (hasSink) byId('audio-sink').value = settings.audio_sink || '';
}

function wire() {
  onSubmit(byId('settings-form'), async () => {
    if (!settings) return undefined;
    const rate = byId('bitrate');
    const mbps = Number(rate.value);
    if (!Number.isFinite(mbps) || mbps < 0) return invalid(rate, 'Enter a number of Mbps, or 0.');
    const next = {
      ...settings,
      encoder: byId('encoder').value,
      gamepad: byId('gamepad').value,
      bitrate_kbps_max: Math.round(mbps * 1000),
    };
    if (!byId('audio-field').hidden) next.audio_sink = byId('audio-sink').value.trim();
    await api('PUT', '/sunshine/settings', next);
    settings = next;
    toast('Saved. The next stream uses these settings.', 'ok');
    return true;
  });

  const restart = byId('sun-restart');
  restart.addEventListener('click', async () => {
    if (sun && sun.streaming) {
      const ok = await confirmDialog({
        title: 'Restart Sunshine?', body: 'The current stream stops. Moonlight can reconnect after a few seconds.',
        confirm: 'Restart', danger: true,
      });
      if (!ok) return;
    }
    await busy(restart, async () => {
      await api('POST', '/sunshine/restart', {});
      toast('Sunshine is restarting.', 'ok');
      setTimeout(loadStatus, 3000);
    });
  });

  on('session.begin', loadStatus);
  on('session.end', loadStatus);
  on('pairing.pending', loadStatus);
  onReconnect(() => Promise.all([loadStatus(), loadSettings()]));
}

async function main() {
  await boot('streaming');
  wire();
  await Promise.all([loadStatus(), loadSettings()]);
}

main();
