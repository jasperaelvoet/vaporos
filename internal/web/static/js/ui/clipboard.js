// ui/clipboard.js: copy text on the box's plain-http origin, where the
// async clipboard does not exist (ARCH §6.13). In order: the async
// clipboard (secure contexts only), execCommand('copy') on a hidden
// textarea placed inside the open dialog when there is one (an inert page
// cannot hold a selection), then selecting the text and saying how to copy.

import { announce } from '../core/announce.js';

export async function copyText(text, { from } = {}) {
  if (globalThis.isSecureContext && navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text);
      return 'copied';
    } catch {
      /* fall through */
    }
  }
  const host = from?.closest('dialog[open]') || document.body;
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.readOnly = true;
  ta.className = 'copy-scratch';
  host.append(ta);
  ta.select();
  ta.setSelectionRange(0, text.length);
  let ok = false;
  try {
    ok = document.execCommand('copy');
  } catch {
    ok = false;
  }
  ta.remove();
  from?.focus({ preventScroll: true });
  return ok ? 'copied' : 'selected';
}

// bindCopy wires every [data-copy-target] button inside root: it copies the
// target's text, shows "Copied" for 2 s and says so; if the browser refused,
// it selects the text and tells how to copy it.
export function bindCopy(root = document) {
  for (const btn of root.querySelectorAll('[data-copy-target]')) {
    if (btn.dataset.copyBound) continue;
    btn.dataset.copyBound = '1';
    btn.addEventListener('click', async () => {
      const target = document.getElementById(btn.dataset.copyTarget);
      if (!target) return;
      const text = target.textContent.trim();
      const label = btn.querySelector('[data-part="copy-label"]');
      const result = await copyText(text, { from: btn });
      if (result === 'copied') {
        if (label) label.textContent = 'Copied';
        btn.dataset.copied = 'true';
        announce(`Copied ${text}`);
        setTimeout(() => {
          if (label) label.textContent = 'Copy';
          delete btn.dataset.copied;
        }, 2000);
        return;
      }
      const range = document.createRange();
      range.selectNodeContents(target);
      const sel = getSelection();
      sel.removeAllRanges();
      sel.addRange(range);
      const coarse = matchMedia('(pointer: coarse)').matches;
      const mac = /Mac|iPhone|iPad/.test(navigator.platform);
      announce(coarse ? 'Press and hold to copy' : mac ? 'Press ⌘C to copy' : 'Press Ctrl+C to copy');
    });
  }
}
