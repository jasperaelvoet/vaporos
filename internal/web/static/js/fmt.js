// fmt.js: pure helpers for formatting and validation. No DOM access, so the
// Go tests can run them under Node (internal/web/jstest).

const UNITS = ['B', 'kB', 'MB', 'GB', 'TB', 'PB'];

// bytes formats a size the way drive vendors print it (decimal units).
export function bytes(n) {
  n = Number(n);
  if (!Number.isFinite(n) || n < 0) return '–';
  let i = 0;
  while (n >= 1000 && i < UNITS.length - 1) { n /= 1000; i++; }
  return `${n.toFixed(i > 0 && n < 10 ? 1 : 0)} ${UNITS[i]}`;
}

// duration formats seconds as the two most significant units.
export function duration(seconds) {
  let s = Math.max(0, Math.floor(Number(seconds) || 0));
  const d = Math.floor(s / 86400); s -= d * 86400;
  const h = Math.floor(s / 3600); s -= h * 3600;
  const m = Math.floor(s / 60); s -= m * 60;
  if (d) return h ? `${d} d ${h} h` : `${d} d`;
  if (h) return m ? `${h} h ${m} min` : `${h} h`;
  if (m) return `${m} min`;
  return `${s} s`;
}

// ago describes an RFC 3339 time relative to now ("5 min ago").
export function ago(iso, now = Date.now()) {
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return '';
  const s = Math.round((now - t) / 1000);
  if (s < 45) return 'just now';
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  if (s < 86400) return `${Math.round(s / 3600)} h ago`;
  if (s < 86400 * 30) return `${Math.round(s / 86400)} d ago`;
  return new Date(t).toLocaleDateString();
}

// clock formats an RFC 3339 time as a local wall-clock time, with the date
// when it is not today.
export function clock(iso, now = new Date()) {
  const t = new Date(iso);
  if (Number.isNaN(t.getTime())) return '';
  const time = t.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  return t.toDateString() === now.toDateString() ? time : `${t.toLocaleDateString([], { weekday: 'short' })} ${time}`;
}

// parseMode parses "WxH@R" (the API's mode format) into numbers.
export function parseMode(s) {
  const m = /^(\d{2,5})x(\d{2,5})@(\d{1,3}(?:\.\d+)?)$/.exec(String(s || '').trim());
  if (!m) return null;
  return { w: Number(m[1]), h: Number(m[2]), r: Number(m[3]) };
}

// modeLabel turns "2560x1600@120" into "2560 × 1600 · 120 Hz".
export function modeLabel(s) {
  const m = parseMode(s);
  return m ? `${m.w} × ${m.h} · ${m.r} Hz` : String(s || '');
}

// groupModes groups modes by resolution, widest first, rates ascending.
export function groupModes(modes) {
  const by = new Map();
  for (const s of modes || []) {
    const m = parseMode(s);
    if (!m) continue;
    const key = `${m.w}x${m.h}`;
    if (!by.has(key)) by.set(key, { w: m.w, h: m.h, rates: [] });
    const g = by.get(key);
    if (!g.rates.includes(m.r)) g.rates.push(m.r);
  }
  const out = [...by.values()];
  for (const g of out) g.rates.sort((a, b) => a - b);
  out.sort((a, b) => b.w - a.w || b.h - a.h);
  return out;
}

// safeNext keeps a ?next= redirect on this origin: a path, never "//host"
// or "/\host", which browsers treat as another origin.
export function safeNext(next) {
  if (typeof next !== 'string' || !/^\/(?![/\\])/.test(next)) return '/';
  if (/^\/(login|setup)(?:[/?#]|$)/.test(next)) return '/';
  return next;
}

// normalizeCode tidies a typed setup code: upper case, no spaces, and the
// "ABCD-EFGH" dash when it was typed as eight characters in a row.
export function normalizeCode(s) {
  const c = String(s || '').toUpperCase().replace(/\s+/g, '');
  return /^[A-Z0-9]{8}$/.test(c) ? `${c.slice(0, 4)}-${c.slice(4)}` : c;
}

// hostnameError returns why s is not a valid single-label hostname, or ''.
export function hostnameError(s) {
  if (!s) return 'Enter a name.';
  if (s.length > 63) return 'Use at most 63 characters.';
  if (!/^[a-z0-9-]+$/.test(s)) return 'Use only lowercase letters, digits and dashes.';
  if (s.startsWith('-') || s.endsWith('-')) return "The name can't start or end with a dash.";
  return '';
}

// passwordError checks a new password and its repetition.
export function passwordError(pw, again, { optional = false } = {}) {
  if (!pw && !again && optional) return '';
  if (pw.length < 8) return 'Use at least 8 characters.';
  if (pw !== again) return "The passwords don't match.";
  return '';
}

// sshKeys splits a textarea into public keys, dropping blanks and comments.
export function sshKeys(text) {
  return String(text || '').split(/\r?\n/).map((l) => l.trim()).filter((l) => l && !l.startsWith('#'));
}

// sshKeyError returns why a key line does not look like an OpenSSH public key.
export function sshKeyError(line) {
  const [type, blob] = line.split(/\s+/);
  if (!/^(ssh-(ed25519|rsa|dss)|ecdsa-sha2-nistp(256|384|521)|sk-(ssh-ed25519|ecdsa-sha2-nistp256)@openssh\.com)$/.test(type || '')) {
    return 'Not an SSH public key (it should start with ssh-ed25519, ssh-rsa or ecdsa-…).';
  }
  if (!/^[A-Za-z0-9+/]+={0,3}$/.test(blob || '')) return 'The key data looks incomplete.';
  return '';
}

// phaseLabel names an update or install phase for people.
export function phaseLabel(phase) {
  const known = {
    check: 'Checking', checking: 'Checking', download: 'Downloading', downloading: 'Downloading',
    write: 'Writing', writing: 'Writing', verify: 'Verifying', verifying: 'Verifying',
    kernel: 'Installing the kernel', boot: 'Setting up start-up', entry: 'Setting up start-up',
    partition: 'Partitioning', format: 'Formatting', copy: 'Copying', config: 'Configuring',
    done: 'Done', staged: 'Ready', error: 'Failed', failed: 'Failed',
  };
  const p = String(phase || '').toLowerCase();
  if (known[p]) return known[p];
  return p ? p.charAt(0).toUpperCase() + p.slice(1).replace(/[_-]+/g, ' ') : 'Working';
}

// percent clamps a value to an integer 0–100.
export function percent(v) {
  const n = Math.round(Number(v));
  return Number.isFinite(n) ? Math.min(100, Math.max(0, n)) : 0;
}

// plural returns "1 device" / "3 devices".
export function plural(n, one, many = `${one}s`) {
  return `${n} ${n === 1 ? one : many}`;
}
