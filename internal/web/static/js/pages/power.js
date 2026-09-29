// Power: idle power-off, keep-awake, and what Wake-on-LAN needs.
import {
  boot, api, on, onReconnect, byId, h, fill, icon, badge, setText, toast, busy, onSubmit, invalid,
  $$, clock, duration,
} from '../lib.js';

let p = null;
let idle = null;

async function load() {
  try {
    p = await api('GET', '/power');
  } catch (e) {
    setText('pstate-title', "Couldn't load power settings");
    setText('pstate-detail', e.message);
    return;
  }
  render();
}

const awakeUntil = () => (p && p.keep_awake_until && Date.parse(p.keep_awake_until) > Date.now() ? p.keep_awake_until : null);

function renderState() {
  let title;
  let detail;
  const until = awakeUntil();
  if (until) {
    title = `Staying awake until ${clock(until)}`;
    detail = 'Idle power-off is paused until then.';
  } else if (p.busy && p.busy.reason) {
    title = 'Staying awake';
    detail = `Busy: ${p.busy.reason}.`;
  } else if (p.idle_shutdown && idle && Number(idle.shutdown_in) > 0) {
    title = `Powers off in ${duration(idle.shutdown_in)}`;
    detail = `Nobody has played for ${duration(idle.idle_seconds)}. Start a stream to keep it on.`;
  } else if (p.idle_shutdown) {
    title = `Powers off after ${p.idle_minutes} minutes idle`;
    detail = 'Idle means no stream, no download and no update.';
  } else {
    title = 'Always on';
    detail = 'Idle power-off is turned off.';
  }
  setText('pstate-title', title);
  setText('pstate-detail', detail);
  byId('awake-clear').hidden = !until;
  setText('awake-status', until
    ? `VaporOS stays on until ${clock(until)}.`
    : 'Keep VaporOS on for a while, for example during a long download.');
}

function render() {
  renderState();
  const form = byId('idle-form');
  if (!form.contains(document.activeElement)) {
    byId('idle-on').checked = !!p.idle_shutdown;
    byId('idle-minutes').value = p.idle_minutes || 15;
  }
  byId('idle-minutes').disabled = !byId('idle-on').checked;

  const wol = p.wol || [];
  fill(byId('wol'), wol.map((w) => h('li', { class: 'list-item' },
    h('span', { class: 'list-icon' }, icon('zap')),
    h('div', { class: 'list-main' },
      h('span', { class: 'list-title mono nowrap', text: w.mac || 'unknown' }),
      h('span', { class: 'list-sub' }, `Network adapter ${w.iface} `, badge(w.enabled ? 'On' : 'Off', w.enabled ? 'ok' : 'warn'))))));
  byId('wol').hidden = !wol.length;
  byId('wol-empty').hidden = wol.length > 0;
}

function wire() {
  byId('idle-on').addEventListener('change', () => {
    byId('idle-minutes').disabled = !byId('idle-on').checked;
  });
  onSubmit(byId('idle-form'), async () => {
    const minutesEl = byId('idle-minutes');
    const minutes = Math.round(Number(minutesEl.value));
    const on = byId('idle-on').checked;
    if (on && !(minutes >= 5 && minutes <= 720)) return invalid(minutesEl, 'Choose between 5 and 720 minutes.');
    // PUT the whole document back (minus the read-only busy state) so the
    // fields this form doesn't show are kept.
    const { busy: _busy, ...rest } = p;
    const next = { ...rest, idle_shutdown: on, idle_minutes: on ? minutes : p.idle_minutes };
    await api('PUT', '/power', next);
    toast(on ? `Saved. VaporOS powers off after ${minutes} idle minutes.` : 'Saved. VaporOS stays on.', 'ok');
    document.activeElement.blur();
    await load();
    return true;
  });
  for (const b of $$('[data-minutes]')) {
    b.addEventListener('click', () => busy(b, async () => {
      const minutes = Number(b.dataset.minutes);
      await api('POST', '/power/keep-awake', { minutes });
      toast(minutes ? `VaporOS stays on for ${duration(minutes * 60)}.` : 'Keep-awake cleared.', 'ok');
      await load();
    }));
  }
  on('power.idle', (i) => {
    idle = i;
    if (p) renderState();
  });
  onReconnect(load);
  // The "until" time passes on its own; re-evaluate it every minute.
  setInterval(() => p && renderState(), 60000);
}

async function main() {
  await boot('power');
  wire();
  await load();
}

main();
