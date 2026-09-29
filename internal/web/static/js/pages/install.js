// pages/install.js: a stub from the shell (C0b) until the entry pages' owner
// (C5) builds the installer wizard here.

import { initSetupCode } from '../core/setup-code.js';
import { boot } from '../core/boot.js';
import { optionalById } from '../core/dom.js';
import { region } from '../ui/region.js';

boot('setup', { auth: false, events: false }).then(() => {
  initSetupCode(optionalById('setup-code'));
  const el = document.querySelector('[data-region="stub"]');
  if (el) region(el).ready();
});
