// pages/about.js: System › About (spec-cc-screens §12). One GET /status
// (the shell's) has it all: system is GET /system and sunshine carries the
// stream server's version. Reconnects refresh it through the shell. The
// temperatures sit on the heat ramp at their real reading; Copy details
// puts every row on the clipboard as "Term: value" lines.

import { api } from '../core/api.js';
import { byId, h, setVar } from '../core/dom.js';
import { region } from '../ui/region.js';
import { notify, onStatus, shell } from '../ui/shell.js';

const fmt = import('../fmt.js');
const COOL = 20; // °C at the ramp's cold end
const HOT = 100; // °C at white-hot

let F = null;
let have = false;

const put = (id, text) => {
  const el = byId(id);
  if (el.textContent !== text) el.textContent = text;
};

// ips: every address but link-local, IPv4 first (J/dashboard:169).
function ips(list) {
  const out = (list || []).filter((ip) => !/^fe80:/i.test(ip) && !ip.startsWith('169.254.'));
  return out.sort((a, b) => Number(a.includes(':')) - Number(b.includes(':')));
}

function gpuRow(gpu) {
  const dd = byId('abt-gpu');
  if (!gpu || !gpu.name) {
    dd.replaceChildren('None detected');
    return;
  }
  const badge = h('span', { class: 'abt-badge', dataset: { tone: gpu.supported ? 'ok' : 'cold' }, text: gpu.supported ? 'Supported' : 'Not supported' });
  dd.replaceChildren(h('span', { class: 'abt-gpu-name', text: gpu.driver ? `${gpu.name} (${gpu.driver})` : gpu.name }), ' ', badge);
}

function temps(list) {
  const rows = (list || [])
    .filter((t) => t && Number.isFinite(Number(t.c)))
    .sort((a, b) => b.c - a.c)
    .map((t) => {
      const bar = h('span', { class: 'abt-temp-bar', 'aria-hidden': 'true' }, h('span', { class: 'abt-temp-fill' }));
      setVar(bar, '--t', Math.min(1, Math.max(0, (t.c - COOL) / (HOT - COOL))).toFixed(3));
      return h('div', { class: 'abt-temp' }, h('dt', { text: t.name || 'Sensor' }), h('dd', {}, h('span', { class: 'abt-temp-c mono', text: `${Math.round(t.c)} °C` }), bar));
    });
  byId('abt-temps').replaceChildren(...(rows.length ? rows : [h('div', { class: 'abt-temp' }, h('dt', { text: 'Sensors' }), h('dd', { text: 'None reported' }))]));
}

function render(s) {
  const sys = s.system;
  if (!sys) return;
  have = true;
  byId('about-error').hidden = true;
  byId('about').hidden = false;
  put('abt-name', sys.hostname || '');
  put('abt-address', F.hostLabel(sys));
  byId('abt-ips').replaceChildren(...ips(sys.ips).flatMap((ip, i) => (i ? [h('br'), ip] : [ip])));
  if (!ips(sys.ips).length) put('abt-ips', 'None');
  put('abt-version', sys.version || '');
  // The page came from the vosd that served it; if that is not the one
  // answering now, both versions are worth seeing (inventory §4 E).
  const served = document.querySelector('.rail-version .mono')?.textContent.trim() || '';
  byId('abt-webui-row').hidden = !served || !sys.version || served === sys.version;
  put('abt-webui', served);
  put('abt-channel', sys.channel || '');
  put('abt-slot', String(sys.booted_slot || '').toUpperCase());
  put('abt-uptime', F.duration(sys.uptime_s));
  put('abt-cpu', sys.cpu || 'Unknown');
  gpuRow(sys.gpu);
  temps(sys.temps);
  const sun = s.sunshine;
  put('abt-sunshine', sun && sun.version ? `Sunshine ${sun.version}` : sun && sun.running === false ? 'Sunshine (stopped)' : 'Sunshine');
  byId('abt-copy').disabled = false;
  const box = byId('about');
  if (box.hasAttribute('aria-busy')) region(box).ready();
}

function failed() {
  if (have) return;
  byId('about-error').hidden = false;
  byId('about').hidden = true;
  region(byId('about')).ready();
}

// details: every row as "Term: value", in page order.
function details() {
  const lines = [];
  for (const row of document.querySelectorAll('#about .fact:not([hidden]), #about .abt-temp')) {
    const dt = row.querySelector('dt');
    const dd = row.querySelector('dd');
    const value = [...dd.childNodes].map((n) => (n.nodeName === 'BR' ? ', ' : n.textContent)).join('').replace(/\s+/g, ' ').trim();
    lines.push(`${dt.textContent.trim()}: ${value}`);
  }
  return lines.join('\n');
}

async function copyDetails(btn) {
  const { copyText } = await import('../ui/clipboard.js');
  const ok = (await copyText(details(), { from: btn })) === 'copied';
  notify(ok ? 'Copied.' : "This browser didn't allow copying.", { kind: ok ? 'ok' : 'warn' });
}

shell('about').then(async () => {
  F = await fmt;
  byId('abt-copy').addEventListener('click', (e) => copyDetails(e.currentTarget));
  byId('about-retry').addEventListener('click', () => {
    api('GET', '/status').then((st) => render({ system: st.system, sunshine: st.sunshine }), () => {});
  });
  onStatus(render);
  // The shell's GET /status is in flight: share it to learn if it fails.
  api('GET', '/status', undefined, { share: true }).then((st) => st.system || failed(), failed);
});
