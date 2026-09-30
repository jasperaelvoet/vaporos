// pages/power-more.js: Power's controls (spec-cc-screens §8.3-§8.5): idle
// power-off, stay awake, and the wired adapters with the Wake card
// (DECISIONS NEW-1), loaded beside pages/power.js, which paints.

import { ApiError, api, errorText, serverNow } from '../core/api.js';
import { byId, cloneTpl, part } from '../core/dom.js';
import { clock } from '../fmt.js';
import { wakeTarget } from '../state.js';
import { bindForm, fieldError, isDirty } from '../ui/form.js';
import { notify, refresh as refreshStatus } from '../ui/shell.js';
import { bindCopy } from '../ui/clipboard.js';
import { fillWake, rememberWol } from '../ui/wake.js';
import { idleMinutesError } from '../validate.js';
import { P, render, take } from './power.js';

const PRESETS = ['15', '30', '60', '120'];
const fail = async (err) => notify(await errorText(err), { kind: 'error' });
const radios = () => Array.from(document.querySelectorAll('#pwr-after .pwr-radio'));
let wolSig = '';

// update runs after every paint of the state.
export function update(p) {
  const auto = byId('pwr-auto');
  if (auto.getAttribute('aria-disabled') !== 'true') auto.checked = !!p.idle_shutdown;
  auto.disabled = false;
  const form = byId('pwr-idle-form');
  if (!isDirty(form)) {
    const n = String(p.idle_minutes || 15);
    const preset = PRESETS.includes(n) ? n : 'custom';
    for (const r of radios()) r.checked = r.value === preset;
    byId('pwr-minutes').value = n;
    byId('pwr-custom').hidden = preset !== 'custom';
    byId('pwr-save').disabled = true;
  }
  byId('pwr-after').disabled = !p.idle_shutdown;
  if (!p.idle_shutdown) byId('pwr-save').disabled = true;
  byId('pwr-idle').removeAttribute('aria-busy');

  const until = Date.parse(p.keep_awake_until || '') > serverNow() ? p.keep_awake_until : '';
  byId('pwr-awake-line').textContent = until ? `VaporOS stays on until ${clock(until)}.` : 'Keep VaporOS on for a while, for example during a long download.';
  byId('pwr-stop').hidden = !until && !(p.busy && p.busy.reason === 'manual keep-awake');
  for (const id of ['pwr-1h', 'pwr-4h']) byId(id).disabled = !p.idle_shutdown;
  byId('pwr-awake-off').hidden = !!p.idle_shutdown;
  byId('pwr-awake').removeAttribute('aria-busy');

  if (Array.isArray(p.wol)) adapters(p.wol);
}

// adapters: the Wake card for the adapter a magic packet reaches, and a
// row per wired adapter with its status and the fix (§8.5, C10).
function adapters(wol) {
  const sig = JSON.stringify(wol);
  if (sig === wolSig) return;
  wolSig = sig;
  rememberWol(wol);
  const t = wakeTarget(wol);
  const shown = t && t.enabled ? t : null;
  const wake = byId('pwr-wake');
  wake.hidden = !wol.length;
  fillWake(wake, wol, { up: true });
  byId('pwr-nics').replaceChildren(...wol.map((w, i) => {
    const li = cloneTpl('tpl-pwr-nic');
    const status = w.enabled ? 'ready' : w.supported ? 'off' : 'none';
    li.dataset.status = status;
    part(li, 'name').textContent = w.iface || 'Wired adapter';
    part(li, 'tag').textContent = { ready: 'Ready to wake', off: 'Switched off', none: "Can't wake the PC" }[status];
    // The Wake card already shows its adapter's MAC with a Copy button.
    if (w !== shown && w.mac) {
      const mac = part(li, 'mac');
      mac.id = `pwr-mac-${i}`;
      mac.textContent = w.mac;
      part(li, 'copy').dataset.copyTarget = mac.id;
      part(li, 'copy-name').textContent = ` the MAC address of ${w.iface}`;
      part(li, 'mac-row').hidden = false;
    }
    if (w.ipv4) {
      const addr = part(li, 'addr');
      addr.textContent = w.prefix ? `${w.ipv4}/${w.prefix}` : w.ipv4;
      addr.hidden = false;
    }
    const fix = status === 'off'
      ? "VaporOS switches it on at every start. If it stays off, turn on Wake-on-LAN in the PC's firmware settings."
      : status === 'none' ? "This adapter doesn't support Wake-on-LAN." : '';
    part(li, 'fix').textContent = fix;
    part(li, 'fix').hidden = !fix;
    return li;
  }));
  bindCopy(byId('pwr-nics'));
  byId('pwr-nics-empty').hidden = wol.length > 0;
  byId('pwr-wol-wait').hidden = true;
  byId('pwr-wol').removeAttribute('aria-busy');
}

// reread asks again without keeping the box awake: /status is quick, and
// GET /power (ethtool) only while the adapters are unknown.
let rereadTimer = 0;
export function reread() {
  clearTimeout(rereadTimer);
  rereadTimer = setTimeout(() => {
    if (!P.p || !Array.isArray(P.p.wol)) api('GET', '/power', undefined, { passive: true }).then(take, () => {});
    else refreshStatus(true);
  }, 600);
}

async function keepAwake(btn, minutes) {
  btn.setAttribute('aria-busy', 'true');
  try {
    await api('POST', '/power/keep-awake', { minutes });
    notify(minutes ? `VaporOS stays on for ${minutes / 60} h.` : 'Stay awake stopped.', { kind: 'ok' });
    refreshStatus(true);
  } catch (err) {
    fail(err);
  } finally {
    btn.removeAttribute('aria-busy');
  }
}

async function put(body) {
  const p = await api('PUT', '/power', body);
  take(p);
  return p;
}

export function start() {
  byId('pwr-1h').addEventListener('click', (e) => keepAwake(e.currentTarget, 60));
  byId('pwr-4h').addEventListener('click', (e) => keepAwake(e.currentTarget, 240));
  byId('pwr-state-awake').addEventListener('click', (e) => keepAwake(e.currentTarget, 60));
  for (const id of ['pwr-stop', 'pwr-state-stop']) byId(id).addEventListener('click', (e) => keepAwake(e.currentTarget, 0));

  // The switch applies at once and flips back if that fails (R4).
  const auto = byId('pwr-auto');
  auto.addEventListener('change', async () => {
    const on = auto.checked;
    auto.setAttribute('aria-disabled', 'true');
    try {
      const p = await put({ idle_shutdown: on });
      notify(on ? `Saved. VaporOS powers off after ${p.idle_minutes} idle minutes.` : 'Saved. VaporOS stays on.', { kind: 'ok' });
    } catch (err) {
      auto.checked = !on;
      fail(err);
    } finally {
      auto.removeAttribute('aria-disabled');
    }
  });

  const minutes = byId('pwr-minutes');
  const chosen = () => {
    const r = radios().find((x) => x.checked);
    return r && r.value !== 'custom' ? r.value : minutes.value.trim();
  };
  byId('pwr-after').addEventListener('change', (e) => {
    if (!e.target.matches('.pwr-radio')) return;
    const custom = e.target.value === 'custom';
    byId('pwr-custom').hidden = !custom;
    if (custom) minutes.focus();
    else fieldError(minutes, '');
  });
  bindForm(byId('pwr-idle-form'), {
    saved: '',
    validate: () => ({ 'pwr-minutes': idleMinutesError(chosen()) }),
    fieldFor: (err) => (err.status === 400 ? 'pwr-minutes' : ''),
    submit: async () => {
      const n = Number(chosen());
      try {
        await put({ idle_minutes: n });
      } catch (err) {
        // The server's words name a JSON field; say it as the field does.
        if (err.status === 400) throw new ApiError(400, idleMinutesError(-1), { method: 'PUT', path: '/power' });
        throw err;
      }
      notify(`Saved. VaporOS powers off after ${n} idle minutes.`, { kind: 'ok' });
    },
  });
  byId('pwr-save').disabled = true;
  render();
}
