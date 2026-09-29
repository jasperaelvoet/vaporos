// core/boot.js: /auth/me and the redirects, without a gate (ARCH §7.4):
// the server painted the frame, and pages start their GETs alongside.

import { goLogin, markSignedIn, refreshMe, session, url } from './api.js';
import { connect } from './live.js';
import { byId, optionalById } from './dom.js';
import { loadSnapshot } from './store.js';
import { settleMain } from '../ui/region.js';

const root = document.documentElement;

// halt: the browser is leaving, so page code after boot must not run.
const halt = () => new Promise(() => {});

export async function boot(page, { auth = true, events = auth } = {}) {
  session.page = page;
  session.booting = true;
  byId('fatal-retry').addEventListener('click', () => location.reload());
  let me;
  try {
    me = await refreshMe();
  } catch {
    fatal();
    return halt();
  }
  if ((me.installer || me.needs_setup) && page !== 'setup') {
    location.replace(url('/setup') + location.search);
    return halt();
  }
  if (auth && !me.authenticated) {
    goLogin();
    return halt();
  }
  if (me.authenticated) markSignedIn();
  session.booting = false;
  for (const b of document.querySelectorAll('main form button[type="submit"]:disabled')) b.disabled = false;
  root.dataset.boot = 'ready';
  settleMain();
  if (events) connect({ auth });
  return me;
}

// fatal is G2, with the last-known state and the Wake card when kept.
export function fatal() {
  root.dataset.boot = 'failed';
  const box = byId('fatal');
  box.hidden = false;
  const snap = loadSnapshot();
  const last = optionalById('fatal-last');
  if (snap && last) {
    const sys = snap.status?.system || {};
    const host = sys.mdns || (sys.hostname ? `${sys.hostname}.local` : '');
    const seen = new Date(snap.at).toLocaleString([], { weekday: 'short', hour: '2-digit', minute: '2-digit' });
    last.textContent = [host, sys.version ? `VaporOS ${sys.version}` : '', `last seen ${seen}`].filter(Boolean).join(' · ');
    last.hidden = false;
  }
  const wake = optionalById('fatal-wake');
  if (wake && snap?.wol) {
    import('../ui/wake.js').then(({ fillWake }) => {
      fillWake(wake, snap.wol, { up: false });
      wake.hidden = false;
    }, () => {});
  }
  const link = optionalById('link');
  if (link) {
    link.dataset.link = 'offline';
    optionalById('link-text').textContent = 'Offline';
    optionalById('link-name').textContent = 'Offline. Show recent notices.';
  }
  settleMain();
  byId('fatal-title').focus();
}
