// validate.js: field rules equal to the server's (T6). Pure; jstest and
// TestValidationParity (VOS_WEB_STRICT=1) share the vectors. A rule returns
// '' or the words shown under the field.

const LONE_SURROGATE = /[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?:^|[^\uD800-\uDBFF])[\uDC00-\uDFFF]/;
const utf8Length = (s) => new TextEncoder().encode(s).length;

// cleanHostname is what the server checks: trimmed and lower-cased.
export function cleanHostname(s) {
  return String(s ?? '').trim().toLowerCase();
}

// hostnameError follows system.ValidateHostname: one RFC 1123 label of
// 1–63 lower-case letters, digits or dashes, no dash at either end, not
// "localhost". Settings and the installer both use it.
export function hostnameError(s) {
  const n = String(s ?? '');
  if (!n) return 'Enter a name.';
  if (n.length > 63) return 'Use at most 63 characters.';
  if (!/^[a-z0-9-]+$/.test(n)) return 'Use only lowercase letters, digits and dashes.';
  if (n.startsWith('-') || n.endsWith('-')) return "The name can't start or end with a dash.";
  if (n === 'localhost') return '"localhost" is reserved. Pick another name.';
  return '';
}

// passwordError follows auth.ValidatePassword: at least 8 characters
// (counted as code points), at most 1024 bytes of UTF-8. again is the
// repetition; optional allows both empty (installer repair).
export function passwordError(pw, again = pw, { optional = false } = {}) {
  pw = String(pw ?? '');
  if (!pw && !again && optional) return '';
  if (LONE_SURROGATE.test(pw)) return "That password has a character VaporOS can't store. Type it again.";
  if ([...pw].length < 8) return 'Use at least 8 characters.';
  if (utf8Length(pw) > 1024) return 'Use a shorter password (at most 1024 bytes).';
  if (pw !== again) return "The passwords don't match.";
  return '';
}

// pinError: exactly four digits (sunshine/handlers.go).
export function pinError(pin) {
  return /^[0-9]{4}$/.test(String(pin ?? '')) ? '' : 'Enter the 4 digits Moonlight shows.';
}

// cleanDeviceName keeps a device name to one line of at most 128 characters.
export function cleanDeviceName(s) {
  return [...String(s ?? '').replace(/[\r\n]+/g, ' ').trim()].slice(0, 128).join('');
}

// MODE_LIMITS are the limits the client checks (edid.Check); the pixel clock
// and line rate are left to the server and mapped by messages.js.
export const MODE_LIMITS = { minW: 320, maxW: 8192, minH: 200, maxH: 8192, minHz: 24, maxHz: 240 };

// modeError checks a resolution the way edid.Check does, minus CVT.
export function modeError(w, h, hz) {
  const L = MODE_LIMITS;
  [w, h, hz] = [w, h, hz].map(Number);
  if (![w, h, hz].every(Number.isInteger)) return 'Use whole numbers.';
  if (w === 4096 && h === 2160) return '4096 × 2160 can\'t be used; try 3840 × 2160.';
  if (w < L.minW || h < L.minH) return 'Too small: use at least 320 × 200.';
  if (w > L.maxW || h > L.maxH) return 'Too large: use at most 8192 wide or high.';
  if (hz < L.minHz || hz > L.maxHz) return 'Use 24 to 240 Hz.';
  return '';
}

// channelError follows update.ValidChannel (an OCI tag).
export function channelError(c) {
  return /^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$/.test(String(c ?? '')) ? '' : 'Use letters, digits, dots, dashes or underscores (at most 128), not starting with a dot or dash.';
}

// audioSinkError follows sunshine's audio_sink rule; empty is the default sink.
export function audioSinkError(s) {
  s = String(s ?? '');
  return s === '' || /^[A-Za-z0-9_.:@+-]{1,255}$/.test(s) ? '' : 'Use the sink name exactly as the system lists it.';
}

// bitrateError: whole megabits per second, 0 (no limit) to 1000.
export function bitrateError(mbps) {
  const n = Number(mbps);
  return Number.isInteger(n) && n >= 0 && n <= 1000 ? '' : 'Use a whole number from 0 to 1000.';
}

// idleMinutesError: 1–1440 (power.go).
export function idleMinutesError(m) {
  const n = Number(m);
  return Number.isInteger(n) && n >= 1 && n <= 1440 ? '' : 'Use 1 to 1440 minutes.';
}

// sshKeys splits a textarea into public keys, dropping blanks and comments.
export function sshKeys(text) {
  return String(text || '').split(/\r?\n/).map((l) => l.trim()).filter((l) => l && !l.startsWith('#'));
}

// sshKeyError returns why a key line does not look like an OpenSSH public key.
export function sshKeyError(line) {
  const [type, blob] = String(line || '').split(/\s+/);
  if (!/^(ssh-(ed25519|rsa|dss)|ecdsa-sha2-nistp(256|384|521)|sk-(ssh-ed25519|ecdsa-sha2-nistp256)@openssh\.com)$/.test(type || '')) {
    return 'Not an SSH public key (it should start with ssh-ed25519, ssh-rsa or ecdsa-…).';
  }
  if (!/^[A-Za-z0-9+/]+={0,3}$/.test(blob || '')) return 'The key data looks incomplete.';
  return '';
}

// normalizeCode tidies a typed setup code: upper case, no spaces, and the
// "ABCD-EFGH" dash when it was typed as eight characters in a row.
export function normalizeCode(s) {
  const c = String(s || '').toUpperCase().replace(/\s+/g, '');
  return /^[A-Z0-9]{8}$/.test(c) ? `${c.slice(0, 4)}-${c.slice(4)}` : c;
}

// safeNext keeps a ?next= redirect on this origin: a path, never "//host"
// or "/\host", which browsers treat as another origin. Control characters
// are refused outright: URL parsers drop tabs and newlines, so "/\t/evil"
// would become "//evil". The result must resolve on the same (placeholder)
// origin and never point back at sign-in or setup.
export function safeNext(next) {
  if (typeof next !== 'string' || /[\u0000-\u001f\u007f]/.test(next) || !/^\/(?![/\\])/.test(next)) return '/';
  const base = 'http://vaporos.invalid';
  let u;
  try {
    u = new URL(next, base);
  } catch {
    return '/';
  }
  if (u.origin !== base || /^\/(login|setup)(?:\/|$)/.test(u.pathname)) return '/';
  return u.pathname + u.search + u.hash;
}

// webNext lets ?next= go back to an extension's web UI, which VaporOS
// serves on a port of its own on this same host: an http URL with here's
// host name and another explicit port of 1024 or more. '' otherwise, and
// safeNext applies.
export function webNext(next, here) {
  if (typeof next !== 'string' || /[\u0000-\u001f\u007f\\]/.test(next) || !/^http:\/\/[^/?#@]+(?:[/?#]|$)/i.test(next)) return '';
  let u;
  try {
    u = new URL(next);
  } catch {
    return '';
  }
  const port = Number(u.port);
  if (u.protocol !== 'http:' || u.username || u.password || !u.port || port < 1024 ||
    u.port === String(here?.port ?? '') || u.hostname !== String(here?.hostname ?? '')) return '';
  return u.href;
}
