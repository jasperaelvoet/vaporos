// ui/controls.js: small controls (ARCH §6.10). Meters take a fraction and
// their words; switches apply at once and revert when the call fails.

import { errorText } from '../core/api.js';
import { setVar } from '../core/dom.js';
import { notify } from './notices.js';

// setMeter fills a role=meter element: fraction 0-1, text for
// aria-valuetext, data-level warn at 85 % and danger at 95 %.
export function setMeter(el, fraction, text) {
  const f = Math.min(1, Math.max(0, Number(fraction) || 0));
  setVar(el, '--v', f.toFixed(4));
  el.setAttribute('aria-valuenow', String(Math.round(f * 100)));
  el.setAttribute('aria-valuetext', text);
  el.dataset.level = f >= 0.95 ? 'danger' : f >= 0.85 ? 'warn' : 'ok';
}

// bindSwitch applies a role=switch checkbox at once through save(checked).
// While saving it is aria-disabled (disabled would drop focus); on failure it
// flips back with an error notice.
export function bindSwitch(input, save) {
  input.addEventListener('change', async () => {
    if (input.getAttribute('aria-disabled') === 'true') return;
    const want = input.checked;
    input.setAttribute('aria-disabled', 'true');
    try {
      await save(want);
    } catch (err) {
      input.checked = !want;
      notify(await errorText(err), { kind: 'error' });
    } finally {
      input.removeAttribute('aria-disabled');
    }
  });
}

// bindStepper wires a stepper's minus and plus buttons to its number input;
// holding a button repeats, faster over time. The input stays the truth.
export function bindStepper(root) {
  const input = root.querySelector('input[type="number"]');
  for (const btn of root.querySelectorAll('[data-step]')) {
    let t = 0;
    const step = () => {
      const d = Number(btn.dataset.step) * (Number(input.step) || 1);
      const v = Math.min(Number(input.max) || Infinity, Math.max(Number(input.min) || -Infinity, (Number(input.value) || 0) + d));
      input.value = String(v);
      input.dispatchEvent(new Event('input', { bubbles: true }));
    };
    const repeat = (wait) => {
      t = setTimeout(() => {
        step();
        repeat(Math.max(40, wait * 0.8));
      }, wait);
    };
    btn.addEventListener('pointerdown', () => repeat(400));
    for (const ev of ['pointerup', 'pointerleave', 'pointercancel']) btn.addEventListener(ev, () => clearTimeout(t));
    btn.addEventListener('click', step);
  }
}
