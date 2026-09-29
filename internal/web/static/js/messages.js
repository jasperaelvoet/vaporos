// messages.js: an API error → the words people see (T5, spec-cc-screens
// §2.13). Pure. The rule: match the endpoint, the status and a known part of
// the server's text; else show the server's text with a capital first letter;
// never show a JSON decode error. jstest/messages.test.mjs pins each row with
// the server's exact words.

import { capitalize, fill } from './copy.js';

export const NETWORK = "Can't reach VaporOS. Check that it's on and on your network.";
export const DECODE = "That didn't work. Reload the page and try again.";

// RULES: [method or '*', path pattern, status or 0, substring or '', copy].
// Paths are matched with {x} as one segment; copy may use <v>, <name>,
// <label> and <n> from the vars given to messageFor.
const RULES = [
  ['*', '/sunshine*', 503, '', 'Streaming is starting. Try again in a few seconds.'],
  ['POST', '/sunshine/pair', 400, 'pairing failed', "That PIN didn't work. Check Moonlight and try again."],
  ['POST', '/sunshine/pair', 409, 'no device is waiting', "Moonlight isn't waiting any more. Start pairing again in Moonlight."],
  ['POST', '/sunshine/pair', 409, 'devices are waiting', 'More than one device is waiting. Choose the one that shows this PIN.'],
  ['POST', '/sunshine/pair', 502, '', "Moonlight didn't finish pairing. Start again in Moonlight."],
  ['*', '/sunshine*', 502, '', "The stream server isn't answering. Restart streaming, or check the log."],
  ['DELETE', '/sunshine/clients/{x}', 404, '', '<name> was already unpaired.'],
  ['PUT', '/sunshine/settings', 500, 'did not restart', "Saved, but streaming didn't restart. Restart it below."],
  ['POST', '/sunshine/end-stream', 409, '', 'Nothing is streaming right now.'],
  ['POST', '/update/check', 502, '', "Couldn't reach the update server. Check the PC's internet connection."],
  ['POST', '/update/stage', 409, '', 'An update is already running.'],
  ['POST', '/update/activate', 409, 'still being installed', 'Wait for the update to finish.'],
  ['POST', '/update/activate', 409, 'no update is staged', "There's no update waiting. Check again."],
  ['POST', '/update/activate', 409, 'no longer bootable', "Version <v> can't start any more. Download it again."],
  ['POST', '/update/rollback', 409, 'failed to start before', "Version <v> didn't start before, so VaporOS won't go back to it."],
  ['POST', '/update/rollback', 409, 'used up its boot attempts', "Version <v> couldn't start, so you can't go back to it."],
  ['POST', '/update/rollback', 409, 'has nothing bootable', "There's no earlier version to go back to."],
  ['POST', '/update/cancel', 409, '', "The update can't be stopped now."],
  ['POST', '/display/modes', 400, 'too small', 'Too small: use at least 320 × 200.'],
  ['POST', '/display/modes', 400, 'too large', 'Too large: use at most 8192 wide or high.'],
  ['POST', '/display/modes', 400, 'refresh outside', 'Use 24 to 240 Hz.'],
  ['POST', '/display/modes', 400, 'pixel clock', 'Too much for the virtual screen: try a lower refresh rate.'],
  ['POST', '/display/modes', 400, 'line rate', 'Too much for the virtual screen: try a lower refresh rate.'],
  ['POST', '/display/modes', 400, 'never offered', "4096 × 2160 can't be used; try 3840 × 2160."],
  ['PUT', '/display/settings', 409, 'no supported GPU', 'Needs a supported graphics card.'],
  ['PUT', '/display/settings', 400, 'not a DP or HDMI', "That port can't be used."],
  ['POST', '/storage/libraries', 404, '', "That drive isn't connected any more. Press Rescan."],
  ['POST', '/storage/libraries', 500, '', "Couldn't mount <label>: <reason>"],
  ['DELETE', '/storage/libraries/{x}', 409, '', '<label> is in use. Quit the game running from it, then try again.'],
  ['PUT', '/system/hostname', 400, 'reserved', '"localhost" is reserved. Pick another name.'],
  ['PUT', '/system/hostname', 400, '', 'Use 1–63 lowercase letters, digits or dashes, not starting or ending with a dash.'],
  ['POST', '/auth/password', 403, '', "That's not the current password."],
  ['POST', '/auth/login', 401, '', "That password isn't right."],
  ['POST', '/auth/login', 503, '', 'VaporOS is busy checking other sign-ins. Try again in a moment.'],
  ['POST', '/auth/setup', 403, '', "That setup code isn't right. Use the code on the screen connected to the PC."],
  ['POST', '/install/reboot', 409, '', 'The install is still running.'],
];

function pathMatches(pattern, path) {
  path = String(path || '').split('?')[0];
  if (pattern.endsWith('*')) return path.startsWith(pattern.slice(0, -1));
  const a = pattern.split('/');
  const b = path.split('/');
  return a.length === b.length && a.every((seg, i) => seg === '{x}' || seg === b[i]);
}

// isDecodeError spots the server's words for a body it could not read.
function isDecodeError(text) {
  return /bad request body|invalid character|cannot unmarshal|unexpected end of JSON|json:/i.test(text);
}

// messageFor turns an error into copy. err is an ApiError-like
// {status, message, retryAfter}; status 0 is a network failure.
// req is {method, path}; vars fills placeholders in the copy.
export function messageFor(err, req = {}, vars = {}) {
  const status = Number(err?.status) || 0;
  const text = String(err?.message ?? '');
  if (status === 0) return NETWORK;
  if (status === 429) {
    const n = Math.max(1, Math.ceil(Number(err?.retryAfter) || 0));
    return err?.retryAfter ? `Too many tries. Try again in ${n} s.` : 'Too many tries. Wait a few minutes and try again.';
  }
  const method = String(req.method || 'GET').toUpperCase();
  const lower = text.toLowerCase();
  for (const [m, p, s, sub, copy] of RULES) {
    if ((m === '*' || m === method) && s === status && pathMatches(p, req.path) && (!sub || lower.includes(sub.toLowerCase()))) {
      return fill(copy, { reason: text, ...vars });
    }
  }
  if (!text || isDecodeError(text)) {
    if (status >= 500) return 'VaporOS is busy or restarting. Try again in a moment.';
    return DECODE;
  }
  return capitalize(text);
}
