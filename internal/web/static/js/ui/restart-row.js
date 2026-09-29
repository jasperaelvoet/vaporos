// ui/restart-row.js: "Restart to finish" on Devices, Screen and System
// (spec-cc-screens §2.10). The reasons come from /status (restart.reasons)
// through state.js; Restart runs the matching action: restart to update
// when the staged version is the reason, else a plain restart. Announced
// once when it appears.

import { announce } from '../core/announce.js';
import { optionalById, part } from '../core/dom.js';
import { restartRow } from '../state.js';
let latest = {};
let shown = false;
let wired = false;

export function renderRestartRow(snap) {
  latest = snap;
  const row = optionalById('restart-row');
  if (!row) return;
  if (!wired) {
    wired = true;
    part(row, 'go').addEventListener('click', () => {
      const r = restartRow(latest);
      if (r.show) import('./scene.js').then((m) => m.powerAction(r.action, { snap: latest, version: r.version }));
    });
  }
  const r = restartRow(snap);
  row.hidden = !r.show;
  if (!r.show) {
    shown = false;
    return;
  }
  part(row, 'text').textContent = r.text;
  part(row, 'go').textContent = r.action === 'activate' ? 'Restart to update' : 'Restart';
  if (!shown) announce(r.text);
  shown = true;
}
