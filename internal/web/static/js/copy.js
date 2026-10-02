// copy.js: words used in more than one place (T2, T3, T4, T7). Pure.
// Errors are messages.js (T5), confirms confirms.js.

export function fill(text, vars = {}) {
  return String(text).replace(/<(\w+)>/g, (m, k) => (vars[k] === undefined || vars[k] === null ? m : String(vars[k])));
}

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

// T3. working: counts as updating (B1: a check never does).
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

// T2. null: never shown as a reason (web UI in use only means a page is open).
const BUSY_EXACT = {
  'keep-awake': 'Staying awake until <until>',
  'manual keep-awake': 'a keep-awake file is set',
  'Moonlight stream': 'Someone is streaming',
  'Moonlight stream (waiting for the client to reconnect)': 'Waiting up to 10 min for Moonlight to reconnect',
  'checking for updates': 'Checking for updates',
  'installing an update': 'Installing an update',
  'Steam game': 'A game is running',
  'Steam download/update': 'Steam is downloading',
  'Steam disk activity': 'Steam is using the disk',
  'web UI in use': null,
};

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

export function pairPrompt(pairings) {
  const n = (pairings || []).length;
  if (n === 0) return '';
  if (n === 1) return `${pairings[0].name || 'A device'} wants to pair`;
  return `${n} devices want to pair`;
}

export function restartRowText(reasons) {
  const one = {
    update: (r) => `Restart to update to ${r.version}.`,
    rollback: (r) => `Restart to go back to ${r.version}.`,
    next: (r) => `Version ${r.version} starts on the next restart.`,
    display: () => 'Restart to apply screen changes.',
    extensions: () => 'Restart to apply extension changes.',
  };
  const part = {
    update: (r) => `version ${r.version} is ready`,
    rollback: (r) => `going back to ${r.version}`,
    next: (r) => `starting version ${r.version}`,
    display: () => 'display changes are waiting',
    extensions: () => 'extension changes are waiting',
  };
  // A kind this page does not know yet (a newer VaporOS) still gets words.
  const list = (reasons || []).filter((r) => r && r.kind);
  if (list.length === 0) return '';
  if (list.length === 1) return one[list[0].kind] ? one[list[0].kind](list[0]) : 'Restart to finish.';
  const parts = [...new Set(list.map((r) => (part[r.kind] ? part[r.kind](r) : 'other changes are waiting')))];
  return `Restart to finish: ${parts.join(' and ')}.`;
}
