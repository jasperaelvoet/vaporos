// Display: the virtual screen Sunshine captures, its modes and HDR.
import {
  boot, api, on, onReconnect, byId, h, fill, icon, badge, kv, toast, onSubmit, invalid, powerAction,
  modeLabel, groupModes,
} from '../lib.js';

let d = null;

const STATES = {
  gaming: 'Steam is running on the virtual screen',
  streaming: 'Streaming',
  welcome: 'Welcome screen on the monitor',
  none: 'Nothing running',
};

async function load() {
  try {
    d = await api('GET', '/display');
  } catch (e) {
    toast(`Couldn't load the display: ${e.message}`, 'error');
    return;
  }
  render();
}

function render() {
  byId('reboot-banner').hidden = !d.reboot_needed;
  byId('nogpu-banner').hidden = d.profile !== 'none';

  kv(byId('now-kv'), [
    ['State', STATES[d.state] || d.state || ''],
    ['Mode', d.current ? modeLabel(d.current) : 'Not active'],
    ['Virtual screen', d.virtual_connector ? h('span', { class: 'mono', text: d.virtual_connector }) : 'None'],
    ['Graphics', d.profile === 'amd' ? 'AMD' : d.profile === 'none' ? 'Not supported' : d.profile],
  ]);

  // Settings: only change the form when the visitor isn't editing it.
  const form = byId('display-form');
  if (!form.contains(document.activeElement)) {
    byId('hdr').checked = !!d.hdr;
    const sel = byId('connector');
    const conns = (d.connectors || []).filter((c) => !c.physical || c.name === d.virtual_connector);
    fill(sel, conns.map((c) => h('option', { value: c.name, text: c.name === d.virtual_connector ? `${c.name} (current)` : c.name })));
    if (d.virtual_connector && !conns.some((c) => c.name === d.virtual_connector)) {
      sel.prepend(h('option', { value: d.virtual_connector, text: `${d.virtual_connector} (current)` }));
    }
    if (!sel.options.length) sel.append(h('option', { value: '', text: 'No free outputs' }));
    sel.value = d.virtual_connector || '';
    sel.disabled = d.profile === 'none' || !conns.length;
  }
  byId('hdr').disabled = d.profile === 'none';

  renderModes();
  renderConnectors();
}

function renderModes() {
  const learned = new Set(d.learned || []);
  const groups = groupModes([...(d.modes || []), ...(d.learned || [])]);
  const box = byId('modes');
  if (!groups.length) {
    fill(box, h('p', { class: 'empty small', text: 'No modes reported yet. They appear once a graphics card drives the virtual screen.' }));
  } else {
    fill(box, groups.map((g) => h('div', { class: 'mode-row' },
      h('span', { class: 'mode-res', text: `${g.w} × ${g.h}` }),
      h('span', { class: 'mode-rates' }, g.rates.map((r) => {
        const key = `${g.w}x${g.h}@${r}`;
        const cls = key === d.current ? 'rate current' : learned.has(key) ? 'rate learned' : 'rate';
        const label = key === d.current ? `${r} Hz, in use` : learned.has(key) ? `${r} Hz, learned` : `${r} Hz`;
        return h('span', { class: cls, title: label, 'aria-label': label, text: `${r} Hz` });
      })))));
  }
  fill(byId('learned'), (d.learned || []).map((m) => h('span', { class: 'chip' }, icon('sparkle'), modeLabel(m))));
  byId('learned-empty').hidden = (d.learned || []).length > 0;
}

function renderConnectors() {
  const conns = d.connectors || [];
  fill(byId('connectors'), conns.length ? conns.map((c) => {
    const tags = [];
    if (c.name === d.virtual_connector) tags.push(badge('Virtual screen', 'accent'));
    if (c.physical) tags.push(badge('Monitor', 'ok'));
    const status = String(c.status || 'unknown');
    tags.push(badge(status.charAt(0).toUpperCase() + status.slice(1)));
    return h('li', { class: 'list-item' },
      h('span', { class: 'list-icon' }, icon(c.physical ? 'monitor' : 'zap')),
      h('div', { class: 'list-main' }, h('span', { class: 'list-title mono', text: c.name }), h('span', { class: 'chips' }, tags)));
  }) : h('li', { class: 'list-item muted', text: 'No outputs found.' }));
}

function wire() {
  onSubmit(byId('display-form'), async () => {
    const body = { hdr: byId('hdr').checked };
    const sel = byId('connector');
    if (!sel.disabled && sel.value && sel.value !== d.virtual_connector) body.virtual_connector = sel.value;
    await api('PUT', '/display/settings', body);
    toast(body.virtual_connector ? 'Saved. Restart VaporOS to use the new output.' : 'Saved.', 'ok');
    document.activeElement.blur();
    await load();
    return true;
  });

  onSubmit(byId('mode-form'), async () => {
    const wEl = byId('mode-w');
    const hEl = byId('mode-h');
    const rEl = byId('mode-r');
    const [w, hh, r] = [wEl, hEl, rEl].map((el) => Math.round(Number(el.value)));
    if (w < 640 || w > 7680) return invalid(wEl, 'Width must be between 640 and 7680.');
    if (hh < 480 || hh > 4320) return invalid(hEl, 'Height must be between 480 and 4320.');
    if (r < 24 || r > 240) return invalid(rEl, 'Refresh rate must be between 24 and 240 Hz.');
    const mode = `${w}x${hh}@${r}`;
    await api('POST', '/display/modes', { mode });
    toast(`Added ${modeLabel(mode)}. Restart VaporOS to use it.`, 'ok');
    byId('mode-form').reset();
    await load();
    return true;
  });

  const reboot = byId('reboot-now');
  reboot.addEventListener('click', () => powerAction('reboot', reboot));

  on('display.changed', load);
  on('session.begin', load);
  on('session.end', load);
  onReconnect(load);
}

async function main() {
  await boot('display');
  wire();
  await load();
}

main();
