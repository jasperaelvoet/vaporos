// confirms.js: every confirm in the UI (spec-cc-screens §2.4), as a table.
// Pure; ui/dialog.js loads it with the dialog. A danger confirm focuses
// Cancel (D4).

import { fill } from './copy.js';

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
