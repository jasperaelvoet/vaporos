// core/announce.js: the two announcers (spec-cc-screens R9), at most one
// message a second each.

const last = { polite: 0, assertive: 0 };
const queued = { polite: '', assertive: '' };

export function announce(text, { assertive = false } = {}) {
  const kind = assertive ? 'assertive' : 'polite';
  const el = document.getElementById(`announce-${kind}`);
  if (!el || !text) return;
  const wait = Math.max(0, 1000 - (Date.now() - last[kind]));
  const first = !queued[kind];
  queued[kind] = text;
  if (!first) return;
  setTimeout(() => {
    // Clear, then set in the next frame, so a repeated text is spoken again.
    el.textContent = '';
    requestAnimationFrame(() => {
      el.textContent = queued[kind];
      queued[kind] = '';
      last[kind] = Date.now();
    });
  }, wait);
}
