// ui/screen-shape.js: sets the screen shape's inputs (MASTER-PLAN §4.4,
// partials/screen-shape.html) from a mode: --mode-w and --mode-h through
// the CSSOM, data-hdr and data-state, and the readout in words.

import { part, setVar } from '../core/dom.js';
import { modeLabel, parseMode, shapeAspect } from '../fmt.js';

export function setShape(root, mode, { hdr = false, state = '' } = {}) {
  const m = parseMode(mode);
  const a = shapeAspect(mode);
  setVar(root, '--mode-w', String(m ? m.w : Math.round(a * 1000)));
  setVar(root, '--mode-h', String(m ? m.h : 1000));
  setVar(root, '--mode-aspect', String(a));
  root.dataset.hdr = String(!!hdr);
  root.dataset.state = state;
  part(root, 'readout').textContent = m ? modeLabel(mode, hdr) : '';
}
