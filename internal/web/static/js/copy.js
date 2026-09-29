// copy.js: the words the control center uses in more than one place, as
// tables (spec-cc-screens §2.4-§2.10, T2, T3, T4, T7). Pure: no DOM,
// storage, location or network, so jstest/copy.test.mjs checks it under
// Node. Error copy is messages.js (T5); field rules are validate.js (T6).

// fill replaces <name> placeholders with vars[name].
export function fill(text, vars = {}) {
  return String(text).replace(/<(\w+)>/g, (m, k) => (vars[k] === undefined || vars[k] === null ? m : String(vars[k])));
}

// capitalize upper-cases the first letter, as raw server text is shown.
export function capitalize(s) {
  s = String(s || '');
  return s ? s.charAt(0).toUpperCase() + s.slice(1) : s;
}

// T7: one label per action.
export const LABELS = {
  stage: 'Download update',
  activate: 'Restart to update',
  check: 'Check now',
  rollback: 'Go back to <v>',
  reboot: 'Restart',
  poweroff: 'Power off',
  sunrestart: 'Restart streaming',
  endstream: 'End stream',
  awake1h: 'Stay awake 1 h',
  awake4h: 'Stay awake 4 h',
  awakeStop: 'Stop',
  adopt: 'Use for games',
  unadopt: 'Stop using',
  unpair: 'Unpair',
  refresh: 'Refresh',
  rescan: 'Rescan',
  retry: 'Try again',
  cancelUpdate: 'Stop',
};

// T3: update phases. working: counts as updating (B1: check never does).
export const UPDATE_PHASES = {
  check: { label: 'Starting', working: false },
  download: { label: 'Downloading', working: true },
  write: { label: 'Downloading', working: true },
  verify: { label: 'Verifying', working: true },
  install: { label: 'Finishing', working: true },
  done: { label: 'Ready', working: false },
  error: { label: 'Stopped', working: false },
  idle: { label: '', working: false },
  cancelled: { label: 'Cancelled', working: false },
};

// T4: install steps (internal/install/install.go).
export const INSTALL_STEPS = {
  probe: 'Getting ready',
  partition: 'Preparing the drive',
  write: 'Copying VaporOS',
  verify: 'Checking the copy',
  bootloader: 'Setting up start-up',
  configure: 'Saving your settings',
  done: 'Done',
};

// T2: why the machine stays on, from /power busy.reason. null means the
// reason is never shown as such (web UI in use: someone has the page open).
const BUSY_EXACT = {
  'keep-awake': 'Staying awake until <until>',
  'manual keep-awake': 'Staying awake: a keep-awake file is set.',
  'Moonlight stream': 'Someone is streaming',
  'Moonlight stream (waiting for the client to reconnect)': 'Waiting up to 10 min for Moonlight to reconnect',
  'checking for updates': 'Checking for updates',
  'installing an update': 'Installing an update',
  'Steam game': 'A game is running',
  'Steam download/update': 'Steam is downloading',
  'Steam disk activity': 'Steam is using the disk',
  'web UI in use': null,
};

// busyReason maps a raw busy reason to copy. until is keep_awake_until as a
// clock time, for the keep-awake reason.
export function busyReason(raw, { until = '' } = {}) {
  const r = String(raw || '').trim();
  if (!r) return '';
  if (r in BUSY_EXACT) return BUSY_EXACT[r] === null ? null : fill(BUSY_EXACT[r], { until: until || 'later' });
  let m = /^streaming to (.+)$/.exec(r);
  if (m) return `Streaming to ${m[1]}`;
  m = /^installing update (\S+)/.exec(r);
  if (m) return `Installing update ${m[1]}`;
  return capitalize(r);
}

// C-*: every confirm in the UI (spec-cc-screens §2.4). tone: normal or
// danger; a danger confirm focuses Cancel.
export const CONFIRMS = {
  reboot: { title: 'Restart VaporOS?', body: 'Streams in progress stop. VaporOS is back in about a minute.', confirm: 'Restart' },
  'reboot-staged': { title: 'Restart and update?', body: 'Version <v> starts after the restart. Streams in progress stop.', confirm: 'Restart' },
  'busy-reboot': { title: 'An update is being written', body: 'Restarting now stops it. It starts over later.', confirm: 'Restart anyway', tone: 'danger' },
  'busy-poweroff': { title: 'An update is being written', body: 'Powering off now stops it. It starts over later.', confirm: 'Power off anyway', tone: 'danger' },
  poweroff: { title: 'Power off VaporOS?', body: 'Streams in progress stop. Wake it from Moonlight, or press its power button.', confirm: 'Power off', tone: 'danger' },
  'poweroff-nowol': { title: 'Power off VaporOS?', body: 'Wake-on-LAN is off, so only its power button starts it again.', confirm: 'Power off', tone: 'danger' },
  activate: { title: 'Restart to update?', body: "Streams in progress stop. If version <v> doesn't start, VaporOS goes back by itself.", confirm: 'Restart to update' },
  rollback: { title: 'Go back to version <v>?', body: 'VaporOS starts version <v> from the next restart on. Your games and settings stay.', confirm: 'Go back' },
  'rollback-now': { title: 'Restart now?', body: 'Version <v> starts after a restart. Streams in progress stop.', confirm: 'Restart now', cancel: 'Later' },
  unpair: { title: 'Unpair <name>?', body: "It can't stream from this PC until you pair it again.", confirm: 'Unpair', tone: 'danger' },
  'stop-using': { title: 'Stop using <label>?', body: "VaporOS stops mounting it. The games stay on the drive, but Steam can't see them until you add it again.", confirm: 'Stop using', tone: 'danger' },
  ntfs: { title: '<label> is a Windows drive', body: 'Its games may not start from NTFS. Use it anyway?', confirm: 'Use anyway' },
  sunrestart: { title: 'Restart streaming?', body: 'The current stream stops. Moonlight can reconnect after a few seconds.', confirm: 'Restart', tone: 'danger' },
  endstream: { title: 'End the stream?', body: 'Moonlight disconnects. The game keeps running on the PC: quit it from Moonlight or in Steam.', confirm: 'End stream', tone: 'danger' },
  cancel: { title: 'Stop the download?', body: 'Nothing changes.', confirm: 'Stop', tone: 'danger' },
  'cancel-writing': { title: 'Stop the download?', body: 'The older version kept for going back is already being replaced, so there is none until the next update.', confirm: 'Stop', tone: 'danger' },
  rename: { title: 'Rename to <new>?', body: 'This page moves to http://<new>.local, where you sign in again. Home-screen shortcuts to the old name stop working.', confirm: 'Rename' },
  port: { title: 'Move the virtual screen to <port>?', body: 'It moves when VaporOS restarts. Plug the monitor into <old> only after that.', confirm: 'Move' },
  discard: { title: 'Discard changes?', body: "What you typed here isn't saved yet.", confirm: 'Discard', tone: 'danger' },
};

// confirmCopy returns a confirm's words with vars filled in.
export function confirmCopy(id, vars = {}) {
  const c = CONFIRMS[id];
  if (!c) throw new Error(`unknown confirm ${id}`);
  return {
    id,
    title: fill(c.title, vars),
    body: fill(c.body, vars),
    confirm: fill(c.confirm, vars),
    cancel: c.cancel || 'Cancel',
    tone: c.tone || 'normal',
  };
}

// The reconnection scene's phases (spec-cc-screens §2.5, NEW-1).
export const SCENE = {
  restarting: { title: 'Restarting', text: 'VaporOS is restarting. This page reconnects by itself.' },
  updating: { title: 'Updating', text: 'VaporOS is restarting into version <v>.' },
  down: { text: 'Waiting for VaporOS to start…' },
  back: { title: 'Back', text: 'Back online.' },
  timeout: { title: "VaporOS hasn't come back yet", text: 'Check the PC, then reload this page.' },
  off: { title: 'Off', text: "Wake it from Moonlight, or press its power button. This page reconnects when it's back." },
  asleep: { title: 'Asleep', text: 'VaporOS stopped answering. It may have powered off after nobody played. This page reconnects by itself.' },
  unreachable: { title: "Can't reach VaporOS", text: "Check that the PC is on and that this device is on the same network. If it's asleep, wake it from Moonlight." },
};

export const WAKE = {
  moonlight: 'Moonlight wakes it: open Moonlight and pick this PC.',
  nowol: 'Wake-on-LAN is off, so only its power button starts it again.',
  save: 'Save these now, while VaporOS is on: a Wake-on-LAN app or your router can wake it with them.',
};

// G8: the connection indicator.
export const LINK = {
  live: { text: '', name: 'Live updates on' },
  connecting: { text: 'Connecting…', name: 'Live updates connecting' },
  offline: { text: 'Offline', name: 'Live updates off, retrying' },
};

export const OFFLINE = {
  stale: 'Showing the last known state.',
  asleep: 'VaporOS might be asleep. Wake it from Moonlight; this page reconnects by itself.',
  back: 'Back online',
};

// pairPrompt is the sticky notice and the takeover heading for the devices
// waiting for their PIN.
export function pairPrompt(pairings) {
  const n = (pairings || []).length;
  if (n === 0) return '';
  if (n === 1) return `${pairings[0].name || 'A device'} wants to pair`;
  return `${n} devices want to pair`;
}

// restartRowText: one reason, or several joined (spec-cc-screens §2.10).
export function restartRowText(reasons) {
  const one = {
    update: (r) => `Restart to update to ${r.version}.`,
    rollback: (r) => `Restart to go back to ${r.version}.`,
    display: () => 'Restart to apply screen changes.',
  };
  const part = {
    update: (r) => `version ${r.version} is ready`,
    rollback: (r) => `going back to ${r.version}`,
    display: () => 'display changes are waiting',
  };
  const list = (reasons || []).filter((r) => one[r.kind]);
  if (list.length === 0) return '';
  if (list.length === 1) return one[list[0].kind](list[0]);
  return `Restart to finish: ${list.map((r) => part[r.kind](r)).join(' and ')}.`;
}

export const HOLD = {
  hint: 'Hold to confirm, or tap to be asked first.',
  nudge: 'Keep holding <key> until the key fills.',
  staged: 'Restarting also installs version <v>.',
  streaming: 'Ends the stream to <client>',
  nowol: 'Wake-on-LAN is off: only the power button starts it again',
  busy: 'An update is being written: restarting stops it',
};
