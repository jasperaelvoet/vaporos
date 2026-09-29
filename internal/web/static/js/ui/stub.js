// ui/stub.js: the shell's placeholder for a page its owner has not built
// yet. It boots the page like any other, so the frame works, and settles
// the page's stub region.

import { region } from './region.js';
import { shell } from './shell.js';

export function stubPage(page) {
  return shell(page).then(() => {
    const el = document.querySelector('[data-region="stub"]');
    if (el) region(el).empty('This page is still being built.');
  });
}
