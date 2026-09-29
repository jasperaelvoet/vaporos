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

async function loadSunshine() {
  try {
    const s = await api('GET', '/sunshine');
    setWaiting(!!s.pending_pairing);
  } catch { /* the form works regardless */ }
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
  onSubmit(byId('pair-form'), async () => {
    const name = byId('device-name');
    if (!/^\d{4}$/.test(pin.value)) return invalid(pin, 'Enter the 4 digits Moonlight shows.');
    if (!name.value.trim()) return invalid(name, 'Give the device a name, like "Living room TV".');
    try {
      await api('POST', '/sunshine/pair', { pin: pin.value, name: name.value.trim() });
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) throw e;
      throw new Error(`Pairing didn't work (${e.message.replace(/\.$/, '')}). Check the PIN, and that Moonlight still shows it.`);
    }
    toast(`${name.value.trim()} is paired. Pick Steam in Moonlight to play.`, 'ok');
    pin.value = '';
    name.value = '';
    setWaiting(false);
    byId('step-pin').classList.add('done');
    await loadClients();
    return true;
  });
  const refresh = byId('clients-refresh');
  refresh.addEventListener('click', () => busy(refresh, loadClients));
  on('pairing.pending', (p) => {
    setWaiting(true, p && p.name);
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
