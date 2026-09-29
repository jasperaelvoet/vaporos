// ui/wake.js: the Wake card (NEW-1): Moonlight wakes the box; the MAC and
// broadcast address serve any other Wake-on-LAN app or a router.

import { part } from '../core/dom.js';
import { saveSnapshot } from '../core/store.js';
import { wakeTarget } from '../state.js';
import { bindCopy } from './clipboard.js';

// fillWake fills a card from wol[]. up: VaporOS answers now, so the card
// says to save the addresses while it can.
export function fillWake(root, wol, { up = true } = {}) {
  const card = root.matches('[data-part="wake"]') ? root : part(root, 'wake');
  if (!card) return;
  const t = wakeTarget(wol);
  const armed = !!(t && t.enabled);
  const known = Array.isArray(wol);
  // Moonlight wakes the PC with the same magic packet, so without an armed
  // adapter only the power button works.
  part(card, 'moonlight').hidden = known && !armed;
  part(card, 'facts').hidden = !armed;
  part(card, 'nowol').hidden = armed || !known;
  part(card, 'save').hidden = !(armed && up);
  if (armed) {
    part(card, 'mac').textContent = t.mac || '';
    part(card, 'bcast').textContent = t.broadcast || '';
    part(card, 'bcast-row').hidden = !t.broadcast;
  }
  bindCopy(card);
}

// rememberWol keeps the adapters with the last-known state, so the card can
// be shown once VaporOS stops answering.
export function rememberWol(wol) {
  if (Array.isArray(wol)) saveSnapshot({ wol: wol.map(({ iface, mac, enabled, supported, ipv4, prefix, broadcast }) => ({ iface, mac, enabled, supported, ipv4, prefix, broadcast })) });
}
