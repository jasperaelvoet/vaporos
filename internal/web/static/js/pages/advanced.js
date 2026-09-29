// Advanced: network name, admin password, SSH, Sunshine's log, restarts.
import {
  boot, api, byId, h, kv, toast, busy, onSubmit, invalid, powerAction, ApiError,
  duration, hostnameError, passwordError, sshKeys, sshKeyError,
} from '../lib.js';

let sys = null;
let ssh = null;

async function loadSystem() {
  try {
    sys = await api('GET', '/system');
  } catch (e) {
    kv(byId('about'), [['Status', e.message]]);
    return;
  }
  const host = byId('hostname');
  if (document.activeElement !== host) host.value = sys.hostname || '';
  kv(byId('about'), [
    ['Version', h('span', { class: 'mono', text: sys.version || '' })],
    ['Channel', sys.channel],
    ['Slot', sys.booted_slot ? sys.booted_slot.toUpperCase() : ''],
    ['Up for', duration(sys.uptime_s)],
    ['Processor', sys.cpu],
    ['Graphics', sys.gpu && sys.gpu.name ? `${sys.gpu.name}${sys.gpu.driver ? ` (${sys.gpu.driver})` : ''}` : 'None detected'],
  ]);
}

async function loadSSH() {
  try {
    ssh = await api('GET', '/ssh');
  } catch (e) {
    toast(`Couldn't load SSH settings: ${e.message}`, 'error');
    return;
  }
  byId('ssh-on').checked = !!ssh.enabled;
  byId('ssh-keys').value = (ssh.keys || []).join('\n');
}

async function loadLogs() {
  const pre = byId('logs-text');
  try {
    const text = await api('GET', '/sunshine/logs', undefined, { text: true });
    pre.textContent = text.trim() || 'The log is empty.';
  } catch (e) {
    pre.textContent = `Couldn't load the log: ${e.message}`;
    return;
  }
  pre.scrollTop = pre.scrollHeight; // newest lines are at the end
}

function wire() {
  onSubmit(byId('hostname-form'), async () => {
    const field = byId('hostname');
    const name = field.value.trim().toLowerCase();
    const err = hostnameError(name);
    if (err) return invalid(field, err);
    if (sys && name === sys.hostname) return true;
    await api('PUT', '/system/hostname', { hostname: name });
    toast(`Renamed. VaporOS is now at http://${name}.local`, 'ok', { href: `http://${name}.local${location.pathname}`, label: 'Open' });
    await loadSystem();
    return true;
  });

  onSubmit(byId('password-form'), async () => {
    const cur = byId('pw-current');
    const p1 = byId('pw-new');
    const p2 = byId('pw-new2');
    const err = passwordError(p1.value, p2.value);
    if (err) return invalid(p1.value.length < 8 ? p1 : p2, err);
    try {
      await api('POST', '/auth/password', { current: cur.value, new: p1.value }, { quiet401: true });
    } catch (e) {
      // This page just loaded with a valid session, so a 401 here is the
      // server rejecting the current password.
      if (e instanceof ApiError && (e.status === 401 || e.status === 403)) return invalid(cur, "The current password isn't right.");
      throw e;
    }
    byId('password-form').reset();
    toast('Password changed.', 'ok');
    return true;
  });

  onSubmit(byId('ssh-form'), async () => {
    const area = byId('ssh-keys');
    const keys = sshKeys(area.value);
    for (const k of keys) {
      const err = sshKeyError(k);
      if (err) return invalid(area, `${err} Line: ${k.slice(0, 40)}…`);
    }
    const enabled = byId('ssh-on').checked;
    if (enabled && !keys.length) return invalid(area, 'Add at least one public key; SSH never accepts passwords.');
    await api('PUT', '/ssh', { ...(ssh || {}), enabled, keys });
    ssh = { ...(ssh || {}), enabled, keys };
    toast(enabled ? 'SSH is on for the keys listed.' : 'SSH is off.', 'ok');
    return true;
  });

  const refresh = byId('logs-refresh');
  refresh.addEventListener('click', () => busy(refresh, loadLogs));
  const reboot = byId('reboot-btn');
  reboot.addEventListener('click', () => powerAction('reboot', reboot));
  const off = byId('poweroff-btn');
  off.addEventListener('click', () => powerAction('poweroff', off));
}

async function main() {
  await boot('advanced');
  wire();
  await Promise.all([loadSystem(), loadSSH(), loadLogs()]);
  if (location.hash === '#logs') byId('logs').scrollIntoView();
}

main();
