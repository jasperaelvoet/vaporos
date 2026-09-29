// ui/hold.js: hold to confirm, the dialog always one tap away (ARCH §6.6).
// REDLINE: the key heats up over --hold-ms and flashes white-hot when it
// fires. A tap, Enter or Space asks instead; letting go partway nudges.

import { reducedMotion } from '../core/dom.js';

const TAP_MS = 250;
const MOVE_PX = 10;

function ms(el, prop, fallback) {
  const v = getComputedStyle(el).getPropertyValue(prop).trim();
  const n = parseFloat(v);
  if (!Number.isFinite(n)) return fallback;
  return v.endsWith('ms') ? n : v.endsWith('s') ? n * 1000 : n;
}

export function holdButton(btn, { run, confirm, hint = null, key = btn.textContent.trim() }) {
  let timer = 0;
  let pressAt = 0;
  let x = 0;
  let y = 0;
  let fired = false;
  let nudgeTimer = 0;
  const base = hint ? hint.textContent : '';

  const set = (state) => {
    if (state) btn.dataset.holdState = state;
    else delete btn.dataset.holdState;
  };
  const stop = () => {
    clearTimeout(timer);
    timer = 0;
    if (btn.dataset.holdState === 'holding') set('');
  };
  const nudge = () => {
    if (!hint) return;
    const b = document.createElement('b');
    b.textContent = key;
    hint.replaceChildren('Keep holding ', b, ' until the key fills.');
    hint.dataset.nudge = 'true';
    clearTimeout(nudgeTimer);
    nudgeTimer = setTimeout(() => {
      hint.textContent = btn.dataset.holdHint || base;
      delete hint.dataset.nudge;
    }, ms(btn, '--hold-nudge-ms', 2800));
  };

  btn.addEventListener('pointerdown', (e) => {
    if (e.button !== 0 || !e.isPrimary || btn.disabled || btn.hasAttribute('data-hold-disabled')) return;
    pressAt = e.timeStamp;
    x = e.clientX;
    y = e.clientY;
    fired = false;
    set('holding');
    timer = setTimeout(() => {
      timer = 0;
      fired = true;
      set('fired');
      if (!reducedMotion()) navigator.vibrate?.(10);
      setTimeout(() => {
        if (btn.dataset.holdState === 'fired') set('');
      }, ms(btn, '--hold-fire-ms', 450));
      run();
    }, ms(btn, '--hold-ms', 1200));
  });
  btn.addEventListener('pointermove', (e) => {
    if (timer && Math.hypot(e.clientX - x, e.clientY - y) > MOVE_PX) stop();
  });
  btn.addEventListener('pointerup', (e) => {
    if (!timer) return;
    const held = e.timeStamp - pressAt;
    stop();
    if (held >= TAP_MS) {
      nudge();
      pressAt = -1; // not a tap: the click that follows is swallowed
    }
  });
  for (const ev of ['pointercancel', 'pointerleave']) btn.addEventListener(ev, stop);
  btn.addEventListener('contextmenu', (e) => {
    if (btn.dataset.holdState === 'holding') e.preventDefault();
  });
  btn.addEventListener('click', async (e) => {
    if (fired || pressAt === -1) {
      fired = false;
      pressAt = 0;
      e.preventDefault();
      return;
    }
    if (await confirm()) run();
  });

  // setHold: holding allowed or not (the hold rule), and the hint's words.
  return {
    setHold(allowed, text = '') {
      btn.toggleAttribute('data-hold-disabled', !allowed);
      btn.dataset.holdHint = text || base;
      if (hint && !hint.dataset.nudge) hint.textContent = text || base;
    },
  };
}
