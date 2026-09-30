// pages/entry-kit.js: what sign-in, first-run setup and the installer
// share (spec-cc-screens §13-§15): the show-password toggles, errors under a
// field, the countdown after a 429 and the viewfinder's temperature.

import { setVar } from '../core/dom.js';

// The viewfinder's heat on the Inferno ramp (0-1) per moment: the sign-in
// and the setup code wait standby-cool; the install heats step by step and
// is white-hot when done; a restarting PC is an ember. cold and failed
// also switch the field to the cold map (styles/pages/entry.css).
const HEAT = {
  standby: 0.2,
  cold: 0.3,
  failed: 0.4,
  disk: 0.22,
  account: 0.25,
  extras: 0.28,
  confirm: 0.31,
  probe: 0.36,
  partition: 0.44,
  write: 0.56,
  verify: 0.66,
  bootloader: 0.74,
  configure: 0.84,
  done: 1,
  down: 0.04,
  back: 0.6,
};

// heat sets the viewfinder's temperature. Heating is quick and cooling
// slow (REDLINE), so a rise marks the element data-warming.
export function heat(vf, level) {
  if (!vf || vf.dataset.heat === level) return;
  const to = HEAT[level] ?? HEAT.standby;
  const from = HEAT[vf.dataset.heat] ?? HEAT.standby;
  if (to > from) vf.dataset.warming = '';
  else delete vf.dataset.warming;
  vf.dataset.heat = level;
  setVar(vf, '--heat', to);
}

// bindReveal wires every show-password toggle ([data-reveal], its input
// named by aria-controls). aria-pressed says whether the text shows.
export function bindReveal(root = document) {
  for (const btn of root.querySelectorAll('[data-reveal]')) {
    const input = document.getElementById(btn.getAttribute('aria-controls'));
    if (!input) continue;
    btn.addEventListener('click', () => {
      const show = input.type === 'password';
      input.type = show ? 'text' : 'password';
      btn.setAttribute('aria-pressed', String(show));
    });
  }
}

// hideReveals puts every revealed password back behind dots.
export function hideReveals(root = document) {
  for (const btn of root.querySelectorAll('[data-reveal][aria-pressed="true"]')) btn.click();
}

// fieldError shows msg in the field's <p id="<input id>-error">, or clears it.
export function fieldError(input, msg) {
  const p = document.getElementById(`${input.id}-error`);
  if (msg) input.setAttribute('aria-invalid', 'true');
  else input.removeAttribute('aria-invalid');
  if (p) {
    p.textContent = msg || '';
    p.hidden = !msg;
  }
}

// retrySeconds is a 429's wait, from Retry-After (ApiError.retryAfter).
export const retrySeconds = (err) => Math.max(1, Math.ceil(Number(err?.retryAfter) || 0));

// wait counts down on btn for a 429: "Try again in 12 s", then back to its
// label. The error line says it once; the button counts, so a screen reader
// is not told every second.
export function wait(btn, seconds, label) {
  let n = Math.max(1, Math.ceil(seconds));
  btn.disabled = true;
  btn.dataset.wait = '';
  const tick = () => {
    if (n <= 0) {
      clearInterval(timer);
      btn.textContent = label;
      btn.disabled = false;
      delete btn.dataset.wait;
      return;
    }
    btn.textContent = `Try again in ${n} s`;
    n -= 1;
  };
  const timer = setInterval(tick, 1000);
  tick();
}
