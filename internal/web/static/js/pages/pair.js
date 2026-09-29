// Pair a device: get Moonlight, add this PC, type the PIN Moonlight shows.
import {
  boot, api, on, onReconnect, byId, h, fill, icon, setText, toast, busy, onSubmit, invalid,
  confirmDialog, ApiError,
} from '../lib.js';

async function loadSystem() {
  let s;
  try {
    s = await api('GET', '/system');
  } catch {
    fill(byId('pc-addresses'), h('span', { class: 'muted', text: "Couldn't load this PC's address." }));
    return;
  }
  setText('pc-name', s.hostname || 'VaporOS');
  // Moonlight is most reliable with a plain IPv4 address; link-local IPv6
  // is useless to type, so it is left out.
  const ips = (s.ips || []).filter((ip) => !/^fe80:/i.test(ip));
  ips.sort((a, b) => Number(a.includes(':')) - Number(b.includes(':')));
  const names = [...ips, s.mdns || (s.hostname ? `${s.hostname}.local` : '')].filter(Boolean);
  fill(byId('pc-addresses'), names.map((a) => h('span', { class: 'address', text: a })));
}

// pairings are the devices waiting for a PIN (GET /sunshine). With more
// than one, Sunshine needs to be told which one the PIN is for.
let pairings = [];

async function loadSunshine() {
  try {
    const s = await api('GET', '/sunshine');
    pairings = Array.isArray(s.pairings) ? s.pairings.filter((p) => p && p.id) : [];
    setWaiting(!!s.pending_pairing || pairings.length > 0, pairings.length === 1 ? pairings[0].name : '');
    renderPairings();
  } catch { /* the form works regardless */ }
}

function pairingLabel(p) {
  const name = p.name || 'Unnamed device';
  return p.address ? `${name} (${p.address})` : name;
}

function renderPairings() {
  const pick = byId('pairing-pick');
  const sel = byId('pairing-id');
  const keep = sel.value;
  fill(sel,
    h('option', { value: '', text: 'Choose a device', disabled: true }),
    pairings.map((p) => h('option', { value: p.id, text: pairingLabel(p) })));
  sel.value = pairings.some((p) => p.id === keep) ? keep : '';
  pick.hidden = pairings.length < 2;
  setText('pairing-pick-hint', `${pairings.length} devices are waiting to pair. Pick the one whose screen shows this PIN.`);
}

function setWaiting(waiting, name = '') {
  byId('pin-waiting').hidden = !waiting;
  byId('step-pin').classList.toggle('attention', waiting);
  setText('pin-waiting-name', name ? `${name} wants to pair. ` : '');
}

async function loadClients() {
  const list = byId('clients');
  let clients;
  try {
    clients = (await api('GET', '/sunshine/clients')).clients || [];
  } catch (e) {
    fill(list, h('li', { class: 'list-item muted', text: e.message }));
    list.hidden = false;
    byId('clients-empty').hidden = true;
    return;
  }
  clients.sort((a, b) => String(a.name).localeCompare(String(b.name)));
  fill(list, clients.map((c) => h('li', { class: 'list-item' },
    h('span', { class: 'list-icon' }, icon('phone')),
    h('div', { class: 'list-main' }, h('span', { class: 'list-title', text: c.name || 'Unnamed device' })),
    h('div', { class: 'list-actions' }, unpairButton(c)))));
  list.hidden = clients.length === 0;
  byId('clients-empty').hidden = clients.length > 0;
}

function unpairButton(c) {
  const name = c.name || 'this device';
  const b = h('button', { class: 'btn btn-danger btn-sm', type: 'button', 'aria-label': `Unpair ${name}` }, icon('trash'), 'Unpair');
  b.addEventListener('click', async () => {
    const ok = await confirmDialog({
      title: `Unpair ${name}?`,
      body: "It can't stream from this PC until you pair it again.",
      confirm: 'Unpair',
      danger: true,
    });
    if (!ok) return;
    await busy(b, async () => {
      await api('DELETE', `/sunshine/clients/${encodeURIComponent(c.uuid)}`);
      toast(`${c.name || 'The device'} is unpaired.`, 'ok');
      await loadClients();
    });
  });
  return b;
}

function wire() {
  const pin = byId('pin');
  pin.addEventListener('input', () => {
    const digits = pin.value.replace(/\D/g, '').slice(0, 4);
    if (digits !== pin.value) pin.value = digits;
  });
  const choice = byId('pairing-id');
  choice.addEventListener('change', () => {
    // Suggest the name the device gave itself, unless one was typed.
    const p = pairings.find((x) => x.id === choice.value);
    const name = byId('device-name');
    if (p && p.name && !name.value.trim()) name.value = p.name;
  });
  onSubmit(byId('pair-form'), async () => {
    const name = byId('device-name');
    const body = { pin: pin.value, name: name.value.trim() };
    if (pairings.length > 1) {
      if (!choice.value) return invalid(choice, 'Choose the device that shows this PIN.');
      body.pairing_id = choice.value;
    }
    if (!/^\d{4}$/.test(pin.value)) return invalid(pin, 'Enter the 4 digits Moonlight shows.');
    if (!body.name) return invalid(name, 'Give the device a name, like "Living room TV".');
    try {
      await api('POST', '/sunshine/pair', body);
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) throw e;
      if (e instanceof ApiError && e.status === 409) {
        // Several devices are waiting (or the chosen one gave up): show the
        // current list so the visitor can pick, instead of blaming the PIN.
        await loadSunshine();
        throw new Error(pairings.length > 1
          ? 'More than one device is waiting to pair. Choose the one that shows this PIN, then press Pair again.'
          : e.message);
      }
      throw new Error(`Pairing didn't work (${e.message.replace(/\.$/, '')}). Check the PIN, and that Moonlight still shows it.`);
    }
    toast(`${body.name} is paired. Pick Steam in Moonlight to play.`, 'ok');
    pin.value = '';
    name.value = '';
    setWaiting(false);
    byId('step-pin').classList.add('done');
    // Another device may still be waiting; the list says so.
    await Promise.all([loadSunshine(), loadClients()]);
    return true;
  });
  const refresh = byId('clients-refresh');
  refresh.addEventListener('click', () => busy(refresh, loadClients));
  on('pairing.pending', (p) => {
    setWaiting(true, p && p.name);
    loadSunshine(); // a second device waiting brings up the picker
    if (document.activeElement === document.body) pin.focus();
  });
  onReconnect(() => Promise.all([loadSunshine(), loadClients()]));
}

async function main() {
  await boot('pair');
  wire();
  await Promise.all([loadSystem(), loadSunshine(), loadClients()]);
}

main();
