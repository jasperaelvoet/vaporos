// pages/updates-more.js: Updates' buttons and settings (spec-cc-screens
// §7.3-§7.5), loaded beside pages/updates.js, which paints.

import { api, errorText } from '../core/api.js';
import { byId, setText } from '../core/dom.js';
import { channelError } from '../validate.js';
import { bindForm, busy, fieldError, isDirty } from '../ui/form.js';
import { current, notify, scene } from '../ui/shell.js';
import { settleMain } from '../ui/region.js';
import { S, refresh, render, take } from './updates.js';

let st = null;
const ask = (c) => import('../ui/dialog.js').then((m) => m.confirmDialog(c));
const power = (kind, opts) => scene().then((m) => m.powerAction(kind, { snap: current(), ...opts }));
const fail = async (err, vars) => notify(await errorText(err, vars), { kind: 'error' });

// update runs after every paint: the settings follow the server unless the
// viewer is editing them (B9).
export function update(u, view) {
  st = view;
  const cfg = u.config || {};
  const auto = byId('upd-auto');
  if (auto.getAttribute('aria-disabled') !== 'true') auto.checked = cfg.auto !== 'off';
  auto.disabled = false;
  const form = byId('upd-channel-form');
  if (!isDirty(form)) {
    const ch = cfg.channel || 'main';
    const pick = byId('upd-channel-pick');
    let opt = pick.querySelector('option[data-current]');
    if (ch !== 'main') {
      if (!opt) {
        opt = document.createElement('option');
        opt.dataset.current = '';
        pick.insertBefore(opt, pick.lastElementChild);
      }
      opt.value = ch;
      opt.textContent = `${ch} (test builds)`;
    } else if (opt) {
      opt.remove();
    }
    pick.value = ch;
    byId('upd-other-field').hidden = true;
  }
  byId('upd-channel-pick').disabled = false;
  byId('upd-settings').removeAttribute('aria-busy');
}

// cancelled: after write began, the older copy is already gone (C-cancel).
export function cancelled(phase) {
  notify(phase === 'write' || phase === 'verify' ? "Download stopped. There's no older version to go back to until the next update." : 'Download stopped. Nothing changed.', { kind: 'info' });
}

// failedLoad: the first read failed (R3): say so, with Try again.
export async function failedLoad(err) {
  if (S.u) return;
  byId('upd-error').hidden = false;
  setText('upd-error-text', `Couldn't load updates. ${await errorText(err)}`);
  setText('upd-title', 'Update status unknown');
  setText('upd-detail', '');
  for (const id of ['upd-status', 'upd-slots', 'upd-settings']) byId(id).removeAttribute('aria-busy');
  settleMain();
}

export function start() {
  // Save waits for a change (boot enables every submit button).
  byId('upd-channel-save').disabled = true;
  byId('upd-check').addEventListener('click', async () => {
    if (S.asked) return;
    S.asked = true;
    render();
    try {
      const r = await api('POST', '/update/check', {});
      if (!r.available) notify('VaporOS is up to date.', { kind: 'ok' });
    } catch (err) {
      fail(err);
    } finally {
      S.asked = false;
      render();
      refresh(0);
    }
  });

  byId('upd-stage').addEventListener('click', async () => {
    const v = st && st.avail && st.avail.version;
    S.starting = true;
    render();
    // The button is gone: the status says what happens now.
    byId('upd-title').focus({ preventScroll: true });
    try {
      await api('POST', '/update/stage', v ? { version: v } : {});
      // No progress within 10 s: ask once more (spec-cc-screens §7.3).
      setTimeout(() => {
        if (S.starting) {
          S.starting = false;
          refresh(0);
        }
      }, 10000);
    } catch (err) {
      S.starting = false;
      render();
      fail(err);
      if (err.status === 409) refresh(0);
    }
  });

  byId('upd-cancel').addEventListener('click', async () => {
    const p = st && st.prog;
    const writing = p && (p.phase === 'write' || p.phase === 'verify');
    if (!(await ask({ id: writing ? 'cancel-writing' : 'cancel' }))) return;
    try {
      await api('POST', '/update/cancel', {});
    } catch (err) {
      fail(err);
      refresh(0);
    }
  });

  byId('upd-restart').addEventListener('click', () => power('activate', { version: st && st.staged }));
  byId('upd-reboot').addEventListener('click', () => power('reboot'));

  byId('upd-back').addEventListener('click', async (e) => {
    const v = S.u && S.u.other_slot && S.u.other_slot.version;
    if (!v || !(await ask({ id: 'rollback', vars: { v } }))) return;
    const ok = await busy(e.currentTarget, () => api('POST', '/update/rollback', {}), (err) => fail(err, { v }));
    if (ok === undefined) {
      refresh(0);
      return;
    }
    const u = await api('GET', '/update', undefined, { passive: true }).catch(() => null);
    if (u) take(u);
    // Later leaves the status on "starts on the next restart" (B3).
    if (await ask({ id: 'rollback-now', vars: { v } })) power('reboot', { confirmed: true });
  });

  // The switch applies at once and flips back if that fails (R4).
  const auto = byId('upd-auto');
  auto.addEventListener('change', async () => {
    const on = auto.checked;
    auto.setAttribute('aria-disabled', 'true');
    try {
      await api('PUT', '/update/settings', { auto: on ? 'stage' : 'off' });
      refresh(0);
    } catch (err) {
      auto.checked = !on;
      fail(err);
    } finally {
      auto.removeAttribute('aria-disabled');
    }
  });

  const form = byId('upd-channel-form');
  const pick = byId('upd-channel-pick');
  const name = byId('upd-channel-name');
  pick.addEventListener('change', () => {
    const other = pick.value === 'other';
    byId('upd-other-field').hidden = !other;
    if (other) name.focus();
    else fieldError(name, '');
  });
  const chosen = () => (pick.value === 'other' ? name.value.trim() : pick.value);
  // An unsaved channel survives a sign-in or a tab switch (CRIT 35).
  const channel = bindForm(form, {
    draft: true,
    saved: '',
    validate: () => (pick.value === 'other' ? { 'upd-channel-name': channelError(name.value.trim()) || '' } : {}),
    fieldFor: (err) => (err.status === 400 && pick.value === 'other' ? 'upd-channel-name' : ''),
    submit: async () => {
      const ch = chosen();
      await api('PUT', '/update/settings', { channel: ch });
      notify(ch === 'main' ? 'Saved.' : `Saved. VaporOS now follows the ${ch} test channel.`, { kind: 'ok' });
      name.value = '';
      // A new channel forgets what the old one offered: read it again.
      queueMicrotask(() => refresh(0));
    },
  });
  if (channel.isDirty()) byId('upd-other-field').hidden = pick.value !== 'other';
}
