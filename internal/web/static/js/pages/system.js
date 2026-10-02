// pages/system.js: the System index (spec-cc-screens §6). Everything comes
// from the shell's one GET /status and the events after it (R1): the
// nameplate, a summary per row, and the hold rule on Restart and Power off.
// Live update.progress and power.idle move the Updates and Power rows
// without a request. The Extensions row is the one exception: /status does
// not carry them, so it asks GET /extensions once and follows
// extensions.state. The pure modules and the hold load beside /auth/me,
// keeping the first paint's scripts to the shell's.

import { api, serverNow } from '../core/api.js';
import { byId } from '../core/dom.js';
import { on, onReconnect } from '../core/live.js';
import { region } from '../ui/region.js';
import { current, onStatus, scene, shell, signOut } from '../ui/shell.js';

const pure = Promise.all([import('../state.js'), import('../fmt.js'), import('../ui/hold.js')]).then((mods) => Object.assign({}, ...mods));
const extWords = import('../ext.js');
const dialog = () => import('../ui/dialog.js');

const HOLD = 'Hold to confirm, or tap to be asked first.';
const TAP = 'Tap to be asked first.';
const STOPS = 'Streams in progress stop.';
const FAILED = "Couldn't load";
// The needle's word for each temperature the shell gives the page (T1).
const WORD = { ready: 'ready', streaming: 'streaming', updating: 'updating', 'restart-needed': 'restart', fault: 'fault', asleep: 'asleep' };
const KEYS = { reboot: ['sys-reboot', 'Restart'], poweroff: ['sys-poweroff', 'Power off'] };

let M = null;
let snap = {};
let have = false; // a GET /status answered
let failed = false; // and before it did, one failed
let fetchedAt = 0;
let powerSig = '';
const live = { progress: null, shutdownIn: null };
const holds = {};

const put = (el, text) => {
  if (el.textContent !== text) el.textContent = text;
};

function sum(id, [text, tone]) {
  put(byId(`sum-${id}`), text);
  const row = byId(`row-${id}`);
  if (tone) row.dataset.tone = tone;
  else delete row.dataset.tone;
}

// Updates: the first of these that holds (spec-cc-screens §6.1).
function updatesLine(s) {
  const u = s.update;
  if (!u) return [FAILED, 'cold'];
  const prog = M.updateProgress({ ...s, live: { progress: live.progress } });
  if (prog) return [`Updating · ${M.percent(prog.percent)}%`, 'hot'];
  const staged = M.stagedVersion(s);
  if (staged) return [`Version ${staged} is ready`, 'hot'];
  const avail = u.available && u.available.version;
  if (avail && M.compareVersions(avail, u.booted) > 0) return [`Version ${avail} is available`, 'warm'];
  if (String(u.last_error || '').startsWith('check: ')) return ["Couldn't check", 'cold'];
  if (!u.checked) return ['Not checked yet', ''];
  return ['Up to date', ''];
}

function powerLine(p) {
  if (!p) return [FAILED, 'cold'];
  const now = serverNow();
  if (p.keep_awake_until && Date.parse(p.keep_awake_until) > now) return [`Awake until ${M.clock(p.keep_awake_until, new Date(now))}`, ''];
  if (!p.idle_shutdown) return ['Always on', ''];
  const left = live.shutdownIn ?? p.shutdown_in;
  if (left > 0) return [`Powers off in ${M.duration(left)}`, 'hot'];
  return [`Powers off after ${p.idle_minutes} min idle`, ''];
}

function storageLine(sys) {
  const d = sys && sys.disk;
  if (!d || !(d.data_total > 0)) return [FAILED, 'cold'];
  const used = 1 - d.data_free / d.data_total;
  const full = used >= 0.95;
  return [`${M.bytes(d.data_free)} free on the system drive${full ? ' · almost full' : ''}`, full ? 'cold' : used >= 0.85 ? 'hot' : ''];
}

// extDoc is GET /extensions: undefined until it answers, null when it
// failed. A refresh that fails keeps what the row says.
let extDoc;

async function renderExtensions() {
  if (extDoc !== undefined) sum('extensions', (await extWords).rowLine(extDoc));
}

function loadExtensions(opts) {
  api('GET', '/extensions', undefined, opts)
    .then((d) => {
      extDoc = d;
    }, () => {
      if (extDoc === undefined) extDoc = null;
    })
    .then(renderExtensions);
}

function renderPlate() {
  put(byId('plate-word'), WORD[document.documentElement.dataset.state] || '');
  const sys = snap.system;
  if (!sys) {
    if (have || failed) put(byId('plate-meta'), "Couldn't load this PC's details.");
    return;
  }
  put(byId('plate-name'), sys.hostname || 'VaporOS');
  put(byId('plate-addr'), M.hostLabel(sys));
  const up = (Number(sys.uptime_s) || 0) + (Date.now() - fetchedAt) / 1000;
  // No break inside "up 1 d 2 h" on a narrow phone.
  put(byId('plate-meta'), [sys.version ? `VaporOS ${sys.version}` : 'VaporOS', `up ${M.duration(up)}`.replace(/ /g, '\u00a0')].join(' · '));
}

function render() {
  if (!M || !(have || failed)) return;
  renderPlate();
  sum('updates', updatesLine(snap));
  sum('power', powerLine(snap.power));
  sum('storage', storageLine(snap.system));
  byId('plate').removeAttribute('aria-busy');
  const rows = byId('sys-rows');
  if (rows.hasAttribute('aria-busy')) region(rows).ready();
}

// The hold rule (MASTER-PLAN §3.5): the plans say whether holding may
// fire and what the hint warns about.
function renderKeys() {
  const plans = Object.keys(KEYS).map((k) => M.powerPlan(k, current()));
  const hints = [...new Set(plans.map((p) => p.hint).filter(Boolean))];
  const text = [plans.some((p) => p.hold) ? HOLD : TAP, ...(hints.length ? hints : [STOPS])].join(' ');
  Object.keys(KEYS).forEach((k, i) => holds[k].setHold(plans[i].hold, text));
}

function wireKeys() {
  const hint = byId('sys-hold-hint');
  for (const [kind, [id, key]] of Object.entries(KEYS)) {
    const btn = byId(id);
    holds[kind] = M.holdButton(btn, {
      hint,
      key,
      confirm: () => dialog().then((d) => d.confirmDialog(M.powerPlan(kind, current()).confirm)),
      run: () => scene().then((s) => s.powerAction(kind, { confirmed: true, snap: current() })),
    });
    btn.disabled = false;
  }
  renderKeys();
  region(document.querySelector('[data-region="power-keys"]')).ready();
}

function status(s) {
  if (!(s.system || s.update || s.power)) return; // events before the first answer
  if (s.system && s.system !== snap.system) fetchedAt = Date.now();
  // A new power answer knows better than an event from before it.
  const p = s.power || {};
  const sig = [p.idle_shutdown, p.idle_minutes, p.keep_awake_until, p.shutdown_in, p.idle_seconds].join('|');
  if (sig !== powerSig) live.shutdownIn = null;
  powerSig = sig;
  snap = s;
  have = true;
  render();
  renderKeys();
}

shell('system').then(async () => {
  byId('sys-signout').addEventListener('click', signOut);
  // The shell's GET /status is in flight: share it to learn if it fails.
  api('GET', '/status', undefined, { share: true }).catch(() => {
    failed = true;
    render();
  });
  loadExtensions();
  on('extensions.state', (d) => {
    if (!d || !Array.isArray(d.extensions)) return;
    extDoc = d;
    renderExtensions();
  });
  onReconnect(() => loadExtensions({ passive: true }));
  M = await pure;
  wireKeys();
  onStatus(status);
  on('update.progress', (p, isLive) => {
    if (!isLive || !p) return;
    live.progress = M.working(p) ? p : null;
    render();
  });
  on('power.idle', (p, isLive) => {
    if (!isLive || !p) return;
    live.shutdownIn = p.shutdown_in ?? null;
    render();
  });
  // Up for counts on without asking the box.
  setInterval(renderPlate, 30000);
  render();
});
