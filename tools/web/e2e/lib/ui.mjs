// What the harness needs to know about each UI set: when a page is ready,
// whether it is a bare page (sign-in, setup) without navigation, and which
// checks fail a run. The new UI must pass every check; the legacy UI is
// frozen, so checks it was never built for only report.

export const UIS = {
  legacy: {
    // lib.js adds html.ready once /auth/me answered, html.failed on a fatal error.
    ready: () => document.documentElement.classList.contains('ready') || document.documentElement.classList.contains('failed'),
    failed: () => document.documentElement.classList.contains('failed'),
    bare: () => document.body.classList.contains('bare'),
    blocking: new Set(['status', 'ready', 'console', 'csp', 'requests', 'overflow', 'landmarks', 'axe']),
    // Known defects of the frozen UI, reported but not failing: its light
    // theme's muted text (the Live label and small print) is under 4.5:1.
    axeKnown: ['color-contrast'],
  },
  next: {
    // core/boot.js sets html[data-boot="ready"] (ARCH §7.4); data regions
    // drop aria-busy once their first render is done (ARCH §6.11).
    ready: () =>
      ['ready', 'failed'].includes(document.documentElement.dataset.boot) &&
      !document.querySelector('[data-region][aria-busy="true"]'),
    failed: () => document.documentElement.dataset.boot === 'failed',
    bare: () => document.body.hasAttribute('data-bare') || document.body.classList.contains('bare'),
    blocking: new Set(['status', 'ready', 'console', 'csp', 'requests', 'overflow', 'landmarks', 'focus', 'targets', 'axe', 'clipped']),
    axeKnown: [],
  },
};

export function ui(name) {
  const u = UIS[name];
  if (!u) throw new Error(`unknown UI ${name}; use legacy or next`);
  return u;
}
