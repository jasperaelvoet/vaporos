// pages/system.js: a stub from the shell (C0b) until the System index's
// owner (C4b) replaces it. It wires the two power keys with the shell's
// hold to confirm (the hold rule decides when holding is allowed) and
// Sign out, which phones reach from here.

import { byId } from '../core/dom.js';
import { powerPlan } from '../state.js';
import { holdButton } from '../ui/hold.js';
import { region } from '../ui/region.js';
import { current, onStatus, scene, shell, signOut } from '../ui/shell.js';

const KEYS = { reboot: ['sys-reboot', 'Restart'], poweroff: ['sys-poweroff', 'Power off'] };

shell('system').then(() => {
  const hint = byId('sys-hold-hint');
  const plans = {};
  for (const [kind, [id, key]] of Object.entries(KEYS)) {
    const btn = byId(id);
    const hold = holdButton(btn, {
      hint,
      key,
      confirm: () => import('../ui/dialog.js').then((m) => m.confirmDialog(powerPlan(kind, current()).confirm)),
      run: () => scene().then((m) => m.powerAction(kind, { confirmed: true, snap: current() })),
    });
    onStatus((s) => {
      plans[kind] = powerPlan(kind, s);
      const all = Object.values(plans);
      const lead = all.some((p) => p.hold) ? 'Hold to confirm, or tap to be asked first.' : 'Tap to be asked first.';
      hold.setHold(plans[kind].hold, [lead, ...new Set(all.map((p) => p.hint).filter(Boolean))].join(' '));
    });
    btn.disabled = false;
  }
  region(document.querySelector('[data-region="power-keys"]')).ready();
  byId('sys-signout').addEventListener('click', signOut);
});
