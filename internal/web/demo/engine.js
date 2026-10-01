// demo/engine.js: vosd's API as the website's live demo answers it.
//
// The control center's pages run unchanged in the demo: core/api.js and
// core/live.js reach the network only through globalThis.vosTransport,
// which runtime.js fills with transport(createEngine(fixtures)). The engine
// answers /api/v1 as the dev server does (TestDevServer: the real api core
// with the fake services of devserver_*_test.go), from the same fixtures
// (fixtures/{base,presets,scripts}, exported as fixtures.js):
//
//   - the core: sessions and their CSRF token, setup codes, the login and
//     setup-code limits, JSON errors, 404/405 for unknown routes, and the
//     event stream with its replay (internal/api, internal/events);
//   - the services: each resource's document, its checks and messages, and
//     the events a change publishes, as the Go fake keeps them.
//
// TestFakeEnginesAgree (fake_engines_test.go) plays fixtures/vectors against
// both fakes and wants one transcript. A change to the Go fake that the
// vectors see needs the same change here.
//
// Time runs on a clock: the real one in the demo, a virtual one in the
// vectors (advance). What the services do later (a stage's progress, a
// restart, the idle policy) is a list of tasks, and everything the engine
// keeps is plain data, so save() and createEngine(fx, {restore}) carry a
// running update or a restart across the demo's page loads.
//
// Where it differs from the Go fake on purpose: no Host or source-address
// checks (there is one origin), setup cookies only from GET /setup (the
// installer pages set them; the engine reads the header or a cookie the
// transport holds), certificates among SSH keys are refused, and Go's JSON
// decoder's words after "bad request body:" are approximated.

export const DEV_PASSWORD = 'vaporvapor';
export const DEV_SETUP_CODE = 'ABCD-EFGH';
export const DEV_PIN = '1234';

const PREFIX = '/api/v1';

// Access levels (internal/api).
const PUBLIC = 'public';
const AUTHED = 'authed';
const SETUP = 'setup';
const AUTHED_OR_SETUP = 'authed-or-setup';

// ---------- plain data ----------

const isObj = (v) => v !== null && typeof v === 'object' && !Array.isArray(v);
const asObj = (v) => (isObj(v) ? v : {});
const asList = (v) => (Array.isArray(v) ? v : []);
const asStr = (v) => (typeof v === 'string' ? v : '');
const asNum = (v) => (typeof v === 'number' ? v : 0);
const clone = (v) => (v === undefined ? undefined : JSON.parse(JSON.stringify(v)));
const has = (o, k) => Object.prototype.hasOwnProperty.call(o, k);

// mergePatch is RFC 7386: objects merge key by key, null deletes a key,
// anything else (arrays too) replaces the target. It changes target.
export function mergePatch(target, patch) {
  if (!isObj(patch)) return clone(patch);
  const out = isObj(target) ? target : {};
  for (const [k, v] of Object.entries(patch)) {
    if (v === null) delete out[k];
    else out[k] = mergePatch(out[k], v);
  }
  return out;
}

const UNIT = { s: 1e3, m: 60e3, h: 3600e3, d: 86400e3 };

// rfc3339 is a time as vosd writes it: UTC, whole seconds.
export const rfc3339 = (ms) => new Date(Math.floor(ms / 1000) * 1000).toISOString().replace('.000Z', 'Z');

// resolveTimes replaces "@now" and "@±<n><s|m|h|d>" with times relative to
// now, in place (devserver_fixtures_test.go resolveTimes).
export function resolveTimes(v, now) {
  if (Array.isArray(v)) {
    for (let i = 0; i < v.length; i++) v[i] = resolveTimes(v[i], now);
  } else if (isObj(v)) {
    for (const k of Object.keys(v)) v[k] = resolveTimes(v[k], now);
  } else if (typeof v === 'string') {
    const m = /^@(now|([+-])(\d+)([smhd]))$/.exec(v);
    if (m) return rfc3339(m[1] === 'now' ? now : now + (m[2] === '-' ? -1 : 1) * Number(m[3]) * UNIT[m[4]]);
  }
  return v;
}

const RFC3339_RE = /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?(Z|[+-]\d\d:\d\d)$/;
const parseTime = (s) => (typeof s === 'string' && RFC3339_RE.test(s) ? Date.parse(s) : NaN);

// ---------- Go's words ----------

const SPACE = '\\t\\n\\v\\f\\r \\u0085\\u00a0\\u1680\\u2000-\\u200a\\u2028\\u2029\\u202f\\u205f\\u3000';
const TRIM_RE = new RegExp(`^[${SPACE}]+|[${SPACE}]+$`, 'g');
const FIELDS_RE = new RegExp(`[${SPACE}]+`);
// goTrim is strings.TrimSpace.
const goTrim = (s) => s.replace(TRIM_RE, '');
const goFields = (s) => goTrim(s).split(FIELDS_RE).filter(Boolean);
const PRINT_RE = /^[\p{L}\p{M}\p{N}\p{P}\p{S} ]$/u;
const utf8Len = (s) => new TextEncoder().encode(s).length;
const runeLen = (s) => Array.from(s).length;

// goQuote is %q (strconv.Quote).
export function goQuote(s) {
  let out = '"';
  for (const ch of s) {
    const c = ch.codePointAt(0);
    if (ch === '"' || ch === '\\') out += '\\' + ch;
    else if (PRINT_RE.test(ch)) out += ch;
    else if (c === 7) out += '\\a';
    else if (c === 8) out += '\\b';
    else if (c === 12) out += '\\f';
    else if (c === 10) out += '\\n';
    else if (c === 13) out += '\\r';
    else if (c === 9) out += '\\t';
    else if (c === 11) out += '\\v';
    else if (c < 0x20 || c === 0x7f) out += '\\x' + c.toString(16).padStart(2, '0');
    else if (c < 0x10000) out += '\\u' + c.toString(16).padStart(4, '0');
    else out += '\\U' + c.toString(16).padStart(8, '0');
  }
  return out + '"';
}

// goDuration is time.Duration.String for whole seconds.
function goDuration(secs) {
  if (secs < 60) return `${secs}s`;
  const h = Math.floor(secs / 3600);
  const m = Math.floor((secs % 3600) / 60);
  const s = secs % 60;
  return h > 0 ? `${h}h${m}m${s}s` : `${m}m${s}s`;
}

// fixed1 is %.1f: exact ties round to even, where toFixed rounds up.
function fixed1(x) {
  if (Number.isInteger(x * 4) && !Number.isInteger(x * 2)) {
    const lo = Math.floor(x * 10);
    return ((lo % 2 === 0 ? lo : lo + 1) / 10).toFixed(1);
  }
  return x.toFixed(1);
}

// ---------- checks the Go fake borrows from the real packages ----------

const ERR_HOSTNAME = 'the name must be 1-63 letters, digits or hyphens, and cannot start or end with a hyphen';

// validateHostname is system.ValidateHostname.
export function validateHostname(name) {
  if (name.length < 1 || utf8Len(name) > 63 || name[0] === '-' || name[name.length - 1] === '-' || !/^[a-z0-9-]+$/.test(name)) {
    return ERR_HOSTNAME;
  }
  if (name === 'localhost') return '"localhost" is reserved';
  return '';
}

const ENCODERS = ['vulkan', 'vaapi', 'software', 'nvenc'];
const GAMEPADS = ['auto', 'xone', 'xseries', 'x360', 'ds4', 'ds5', 'switch', 'generic'];

// validateSettings is sunshine.Settings.Validate.
function validateSettings(s) {
  if (!ENCODERS.includes(s.encoder)) return `encoder must be one of ${ENCODERS.join(', ')}`;
  if (s.bitrate_kbps_max < 0 || s.bitrate_kbps_max > 1000000) return 'bitrate_kbps_max must be between 0 (no limit) and 1000000';
  if (s.audio_sink !== '' && !/^[A-Za-z0-9_.:@+-]{1,255}$/.test(s.audio_sink)) return 'audio_sink is not a valid sink name';
  if (!GAMEPADS.includes(s.gamepad)) return `gamepad must be one of ${GAMEPADS.join(', ')}`;
  return '';
}

// validatePassword is auth.ValidatePassword.
function validatePassword(pw) {
  if (runeLen(pw) < 8 || utf8Len(pw) > 1024) return 'the password must be 8 to 1024 characters long';
  return '';
}

const validVersion = (v) => /^[0-9A-Za-z][0-9A-Za-z._-]{0,63}$/.test(v); // manifest.ValidVersion
const validChannel = (c) => /^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$/.test(c); // update.ValidChannel
const LIBRARY_FS = ['ext4', 'ext3', 'ext2', 'btrfs', 'xfs', 'f2fs', 'ntfs3'];
const libraryFS = (fs) => LIBRARY_FS.includes(fs === 'ntfs' ? 'ntfs3' : fs); // storage.LibraryFS

const isDigit = (c) => c >= '0' && c <= '9';
const isVersionSep = (c) => !/[0-9A-Za-z]/.test(c);

// compareVersions is boot.CompareVersions: runs of digits compare as
// numbers, runs of letters as text, and a number is newer than letters.
export function compareVersions(a, b) {
  const trim = (s) => {
    let i = 0;
    while (i < s.length && isVersionSep(s[i])) i++;
    return s.slice(i);
  };
  const run = (s) => {
    const digit = isDigit(s[0]);
    let i = 1;
    while (i < s.length && !isVersionSep(s[i]) && isDigit(s[i]) === digit) i++;
    return [s.slice(0, i), s.slice(i)];
  };
  const cmp = (x, y) => (x < y ? -1 : x > y ? 1 : 0);
  for (;;) {
    a = trim(a);
    b = trim(b);
    if (!a && !b) return 0;
    if (!a) return -1;
    if (!b) return 1;
    let [ra, restA] = run(a);
    let [rb, restB] = run(b);
    const da = isDigit(ra[0]);
    const db = isDigit(rb[0]);
    if (da && db) {
      ra = ra.replace(/^0+/, '');
      rb = rb.replace(/^0+/, '');
      if (ra.length !== rb.length) return ra.length < rb.length ? -1 : 1;
      const c = cmp(ra, rb);
      if (c) return c;
    } else if (da) {
      return 1;
    } else if (db) {
      return -1;
    } else {
      const c = cmp(ra, rb);
      if (c) return c;
    }
    a = restA;
    b = restB;
  }
}

// ---------- display modes (internal/display/edid) ----------

// CATALOGUE is edid.Catalogue: the modes every VaporOS EDID carries.
const CATALOGUE = [
  [1920, 1080, 60], [3840, 2160, 60], [2560, 1440, 60], [1920, 1080, 120], [2560, 1600, 60], [1280, 800, 60],
  [1920, 1200, 60], [1280, 720, 60], [2560, 1440, 120], [2560, 1600, 120], [1920, 1080, 144], [1920, 1200, 120],
  [1280, 800, 90], [1280, 720, 120], [3440, 1440, 60], [3440, 1440, 100], [3840, 2160, 30], [2796, 1290, 60],
  [2796, 1290, 120], [2556, 1179, 60], [2556, 1179, 120], [2732, 2048, 60],
];

const modeString = (m) => `${m.w}x${m.h}@${m.r}`;
const sameMode = (a, b) => a.w === b.w && a.h === b.h && a.r === b.r;
const inCatalogue = (m) => CATALOGUE.some(([w, h, r]) => w === m.w && h === m.h && r === m.r);

function atoi(s) {
  if (!/^[+-]?\d+$/.test(s)) return null;
  return Number(s);
}

function parseFloatGo(s) {
  if (/^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?$/.test(s)) return Number(s);
  if (/^[+-]?(inf|infinity)$/i.test(s)) return s[0] === '-' ? -Infinity : Infinity;
  return NaN;
}

// parseMode is edid.ParseMode: {mode} or {error}.
export function parseMode(input) {
  const s = goTrim(String(input));
  const at = s.indexOf('@');
  const size = (at < 0 ? s : s.slice(0, at)).toLowerCase();
  const x = size.indexOf('x');
  if (x < 0) return { error: `mode ${goQuote(s)}: want WxH@R` };
  const w = atoi(size.slice(0, x));
  const h = atoi(size.slice(x + 1));
  if (w === null || h === null || w <= 0 || h <= 0) return { error: `mode ${goQuote(s)}: bad size` };
  let r = 60;
  if (at >= 0) {
    const f = parseFloatGo(s.slice(at + 1));
    if (!(f > 0) || f > 1000) return { error: `mode ${goQuote(s)}: bad refresh rate` };
    r = Math.round(f);
  }
  return { mode: { w, h, r } };
}

// checkMode is edid.Check, with CVT reduced blanking v2 (edid.CVTRB2).
export function checkMode(m) {
  const s = modeString(m);
  if (m.w === 4096 && m.h === 2160) return `${s}: 4096x2160 is never offered`;
  if (m.w < 320 || m.h < 200) return `${s}: too small`;
  if (m.w > 8192 || m.h > 8192) return `${s}: too large`;
  if (m.r < 24 || m.r > 240) return `${s}: refresh outside 24-240 Hz`;
  const den = Math.max(1000000 - 460 * m.r, 1);
  const vbi = Math.max(Math.trunc((460 * m.h * m.r) / den) + 1, 15);
  const htotal = m.w + 80;
  const clock = Math.trunc((m.r * (m.h + vbi) * htotal) / 1000);
  if (clock > 600000) return `${s}: needs a ${fixed1(clock / 1000)} MHz pixel clock (max 600 MHz)`;
  const line = clock / htotal;
  if (line < 15 || line > 400) return `${s}: line rate ${fixed1(line)} kHz outside 15-400 kHz`;
  return '';
}

function hasMode(list, m) {
  return list.some((v) => {
    const p = parseMode(asStr(v));
    return p.mode && sameMode(p.mode, m);
  });
}

// sortedModes orders modes as GET /display lists them: by area, then
// width, then refresh rate, largest first; each once.
function sortedModes(list) {
  const ms = [];
  for (const v of list) {
    const p = parseMode(asStr(v));
    if (p.mode && !ms.some((m) => sameMode(m, p.mode))) ms.push(p.mode);
  }
  ms.sort((a, b) => b.w * b.h - a.w * a.h || b.w - a.w || b.r - a.r);
  return ms.map(modeString);
}

// learnedModes is added plus every device's valid mode beyond the
// catalogue, once each, sorted (display.Manager.learnedModes).
function learnedModes(d) {
  const extra = asList(d.added).slice();
  for (const dev of asList(d.devices)) extra.push(asObj(dev).mode);
  const beyond = [];
  for (const v of extra) {
    const p = parseMode(asStr(v));
    if (p.mode && !inCatalogue(p.mode) && !checkMode(p.mode)) beyond.push(modeString(p.mode));
  }
  return sortedModes(beyond);
}

const listsEqual = (a, b) => a.length === b.length && a.every((v, i) => v === b[i]);

// ---------- SSH keys (system.NormalizeKeys over x/crypto/ssh) ----------

function b64decode(s) {
  if (s.length % 4 !== 0 || !/^[A-Za-z0-9+/]*={0,2}$/.test(s)) return null;
  let bin;
  try {
    bin = atob(s);
  } catch {
    return null;
  }
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

function b64encode(bytes) {
  let bin = '';
  for (const b of bytes) bin += String.fromCharCode(b);
  return btoa(bin);
}

function wireReader(buf) {
  let off = 0;
  return {
    string() {
      if (off + 4 > buf.length) return null;
      const n = ((buf[off] << 24) | (buf[off + 1] << 16) | (buf[off + 2] << 8) | buf[off + 3]) >>> 0;
      if (off + 4 + n > buf.length) return null;
      const s = buf.subarray(off + 4, off + 4 + n);
      off += 4 + n;
      return s;
    },
    rest: () => buf.length - off,
  };
}

const latin1 = (b) => String.fromCharCode(...b);
const bigOf = (b) => (b.length ? BigInt('0x' + Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('')) : 0n);
const bitLen = (n) => (n === 0n ? 0 : n.toString(2).length);

function wireString(parts) {
  const bytes = [];
  for (const p of parts) {
    const b = typeof p === 'string' ? Array.from(p, (c) => c.charCodeAt(0)) : Array.from(p);
    bytes.push((b.length >>> 24) & 255, (b.length >>> 16) & 255, (b.length >>> 8) & 255, b.length & 255, ...b);
  }
  return new Uint8Array(bytes);
}

// mpint is the SSH encoding of a non-negative big integer.
function mpint(n) {
  if (n === 0n) return new Uint8Array(0);
  let hex = n.toString(16);
  if (hex.length % 2) hex = '0' + hex;
  const b = hex.match(/../g).map((x) => parseInt(x, 16));
  if (b[0] & 0x80) b.unshift(0);
  return new Uint8Array(b);
}

// The NIST curves' prime and b, for the point check of elliptic.Unmarshal.
const CURVES = {
  nistp256: {
    p: 2n ** 256n - 2n ** 224n + 2n ** 192n + 2n ** 96n - 1n,
    b: 0x5ac635d8aa3a93e7b3ebbd55769886bc651d06b0cc53b0f63bce3c3e27d2604bn,
    len: 32,
  },
  nistp384: {
    p: 2n ** 384n - 2n ** 128n - 2n ** 96n + 2n ** 32n - 1n,
    b: 0xb3312fa7e23ee7e4988e056be3f82d19181d9c6efe8141120314088f5013875ac656398d8a2ed19d2a85c8edd3ec2aefn,
    len: 48,
  },
  nistp521: {
    p: 2n ** 521n - 1n,
    b: 0x0051953eb9618e1c9a1f929a21a0b68540eea2da725b99b315f3b8b489918ef109e156193951ec7e937b1652c0bd3bb1bf073573df883d2c34f1ef451fd46b503f00n,
    len: 66,
  },
};

function onCurve(curve, point) {
  const c = CURVES[curve];
  if (point.length !== 1 + 2 * c.len || point[0] !== 4) return false;
  const x = bigOf(point.subarray(1, 1 + c.len));
  const y = bigOf(point.subarray(1 + c.len));
  if (x >= c.p || y >= c.p) return false;
  const mod = (n) => ((n % c.p) + c.p) % c.p;
  return mod(y * y) === mod(x * x * x - 3n * x + c.b);
}

// parsePublicKey is ssh.ParsePublicKey for the key types sshd takes (no
// certificates): {type, blob (re-marshalled), bits?} or null.
function parsePublicKey(buf) {
  const r = wireReader(buf);
  const algo = r.string();
  if (!algo) return null;
  const name = latin1(algo);
  let key = null;
  switch (name) {
    case 'ssh-rsa': {
      const e = r.string();
      const n = r.string();
      if (!e || !n || (e.length && e[0] & 0x80) || (n.length && n[0] & 0x80)) return null;
      const ev = bigOf(e);
      if (bitLen(ev) > 24 || ev < 3n || ev % 2n === 0n) return null;
      const nv = bigOf(n);
      key = { type: name, blob: wireString([name, mpint(ev), mpint(nv)]), bits: bitLen(nv) };
      break;
    }
    case 'ssh-dss': {
      const ps = [r.string(), r.string(), r.string(), r.string()];
      if (ps.some((p) => !p)) return null;
      key = { type: name, blob: wireString([name, ...ps.map((p) => mpint(bigOf(p)))]) };
      break;
    }
    case 'ecdsa-sha2-nistp256':
    case 'ecdsa-sha2-nistp384':
    case 'ecdsa-sha2-nistp521':
    case 'sk-ecdsa-sha2-nistp256@openssh.com': {
      const sk = name.startsWith('sk-');
      const curve = r.string();
      const point = r.string();
      const app = sk ? r.string() : new Uint8Array(0);
      if (!curve || !point || !app) return null;
      const cv = latin1(curve);
      if (!CURVES[cv] || (sk && cv !== 'nistp256') || !onCurve(cv, point)) return null;
      const type = sk ? name : `ecdsa-sha2-${cv}`;
      key = { type, blob: wireString(sk ? [type, cv, point, app] : [type, cv, point]) };
      break;
    }
    case 'ssh-ed25519':
    case 'sk-ssh-ed25519@openssh.com': {
      const sk = name.startsWith('sk-');
      const k = r.string();
      const app = sk ? r.string() : new Uint8Array(0);
      if (!k || !app || k.length !== 32) return null;
      key = { type: name, blob: wireString(sk ? [name, k, app] : [name, k]) };
      break;
    }
    default:
      return null;
  }
  return r.rest() === 0 ? key : null;
}

const firstSpace = (s) => {
  const a = s.indexOf(' ');
  const b = s.indexOf('\t');
  return a < 0 ? b : b < 0 ? a : Math.min(a, b);
};

// keyField is ssh's parseAuthorizedKey: base64 and an optional comment.
function keyField(s) {
  s = goTrim(s);
  let i = firstSpace(s);
  if (i < 0) i = s.length;
  const blob = b64decode(s.slice(0, i));
  const key = blob && parsePublicKey(blob);
  return key ? { key, comment: goTrim(s.slice(i)) } : null;
}

// authorizedKey is ssh.ParseAuthorizedKey on one line: {key, comment,
// options} or null.
function authorizedKey(line) {
  const s = goTrim(line);
  if (!s || s[0] === '#') return null;
  let i = firstSpace(s);
  if (i < 0) return null;
  const direct = keyField(s.slice(i));
  if (direct) return { ...direct, options: [] };
  // No key type recognised: maybe an options field comes first.
  const options = [];
  let inQuote = false;
  let start = 0;
  let broke = false;
  for (i = 0; i < s.length; i++) {
    const b = s[i];
    const isEnd = !inQuote && (b === ' ' || b === '\t');
    if ((b === ',' && !inQuote) || isEnd) {
      if (i - start > 0) options.push(s.slice(start, i));
      start = i + 1;
    }
    if (isEnd) {
      broke = true;
      break;
    }
    if (b === '"' && (i === 0 || s[i - 1] !== '\\')) inQuote = !inQuote;
  }
  if (!broke) i = s.length - 1;
  while (i < s.length && (s[i] === ' ' || s[i] === '\t')) i++;
  if (i === s.length) return null;
  const rest = s.slice(i);
  const j = firstSpace(rest);
  if (j < 0) return null;
  const withOptions = keyField(rest.slice(j));
  return withOptions ? { ...withOptions, options } : null;
}

// cleanComment keeps a key comment printable, on one line and at most 200
// bytes long.
function cleanComment(c) {
  c = Array.from(c, (ch) => (PRINT_RE.test(ch) ? ch : ' ')).join('');
  c = goFields(c).join(' ');
  const bytes = new TextEncoder().encode(c);
  if (bytes.length <= 200) return c;
  let end = 200;
  while (end > 0 && (bytes[end] & 0xc0) === 0x80) end--;
  return new TextDecoder().decode(bytes.subarray(0, end));
}

// normalizeKeys is system.NormalizeKeys: {keys} or {error}.
export function normalizeKeys(input) {
  const out = [];
  const seen = new Set();
  for (let i = 0; i < input.length; i++) {
    const k = goTrim(asStr(input[i]));
    if (!k) continue;
    const n = i + 1;
    if (/[\r\n\0]/.test(k)) return { error: `key ${n}: one key per entry, on a single line` };
    const parsed = authorizedKey(k);
    if (!parsed) {
      return { error: `key ${n} is not an OpenSSH public key (it should start with ssh-ed25519, ecdsa-sha2-… or ssh-rsa)` };
    }
    if (parsed.options.length) return { error: `key ${n}: authorized_keys options are not supported` };
    if (parsed.key.type === 'ssh-dss') return { error: `key ${n}: DSA keys are no longer accepted by sshd; use ed25519` };
    if (parsed.key.type === 'ssh-rsa' && parsed.key.bits < 2048) return { error: `key ${n}: RSA keys need at least 2048 bits` };
    let line = `${parsed.key.type} ${b64encode(parsed.key.blob)}`;
    if (seen.has(line)) continue;
    seen.add(line);
    const c = cleanComment(parsed.comment);
    if (c) line += ' ' + c;
    out.push(line);
  }
  if (out.length > 64) return { error: 'at most 64 keys' };
  return { keys: out };
}

// ---------- request bodies (api.ReadJSON) ----------

const GO_TYPES = { string: 'string', bool: 'bool', int: 'int', strings: '[]string' };

function jsonKind(v) {
  if (Array.isArray(v)) return 'array';
  if (v === null) return 'null';
  return typeof v === 'object' ? 'object' : typeof v;
}

// readJSON decodes body into the fields of spec ({name: 'string' | 'bool' |
// 'int' | 'strings'}) the way encoding/json fills a struct: names match
// case-insensitively, null leaves a field alone, and a value of the wrong
// type fails. Absent fields are undefined. {v} or {error, eof}.
function readJSON(body, spec) {
  const text = body == null ? '' : String(body);
  if (!goTrim(text)) return { error: 'bad request body: EOF', eof: true };
  let data;
  try {
    data = JSON.parse(text);
  } catch {
    return { error: 'bad request body: invalid character in the JSON body' };
  }
  const v = {};
  if (data === null) return { v };
  if (!isObj(data)) return { error: `bad request body: json: cannot unmarshal ${jsonKind(data)} into Go value of type struct` };
  const names = Object.keys(spec);
  let bad = '';
  for (const [key, val] of Object.entries(data)) {
    const field = names.includes(key) ? key : names.find((n) => n.toLowerCase() === key.toLowerCase());
    if (!field || val === null) continue;
    const t = spec[field];
    const ok =
      (t === 'string' && typeof val === 'string') ||
      (t === 'bool' && typeof val === 'boolean') ||
      (t === 'int' && Number.isInteger(val)) ||
      (t === 'strings' && Array.isArray(val) && val.every((x) => x === null || typeof x === 'string'));
    if (!ok) {
      bad ||= `bad request body: json: cannot unmarshal ${jsonKind(val)} into Go struct field .${key} of type ${GO_TYPES[t]}`;
      continue;
    }
    v[field] = t === 'strings' ? val.map((x) => x ?? '') : val;
  }
  return bad ? { error: bad } : { v };
}

// ---------- responses ----------

const SECURITY = {
  'content-security-policy': "default-src 'self'; img-src 'self' data:; frame-ancestors 'none'",
  'x-content-type-options': 'nosniff',
  'referrer-policy': 'same-origin',
  'x-frame-options': 'DENY',
};

// An answer is {status, headers, body}; a handler returns one, or a value
// that is answered as JSON with 200.
const ANSWER = Symbol('answer');

function rawAnswer(status, headers, body) {
  return { [ANSWER]: true, status, headers: { ...SECURITY, ...headers }, body };
}

function jsonAnswer(status, v, extra = {}) {
  return rawAnswer(status, { 'content-type': 'application/json', 'cache-control': 'no-store', ...extra }, JSON.stringify(v) + '\n');
}

const isAnswer = (v) => isObj(v) && v[ANSWER] === true;

const errorAnswer = (status, msg, extra) => jsonAnswer(status, { error: goTrim(msg) }, extra);

// ---------- the idle policy and the update stage (devserver_power/update) ----------

const POWER_INTERVAL = 15000; // power.interval
const WEB_WINDOW = 5 * 60000; // power.webActivityWindow
const MAX_KEEP_AWAKE = 7 * 24 * 60; // minutes, power.maxKeepAwake
const WEB_REASON = 'web UI in use';
const HEARTBEAT = 15000; // the event stream's heartbeat

const UPDATE_STATE_KEYS = ['booted', 'staged', 'failed', 'available', 'checked', 'last_error', 'held'];

// STAGE_PHASES are Stage's phases, the share of the overall percent each
// covers, the bytes each counts (0: the image's size) and how long the
// fake takes for it.
const STAGE_PHASES = [
  { name: 'download', from: 0, to: 2, bytes: 64000000, took: 1000 },
  { name: 'write', from: 2, to: 80, bytes: 0, took: 8000 },
  { name: 'verify', from: 80, to: 98, bytes: 0, took: 3000 },
  { name: 'install', from: 98, to: 100, bytes: 64000000, took: 500 },
];
const STAGE_TICK = 250;

const ERR_ON_TRIAL = 'the running version is still on trial; try again once it has fully started';
const ERR_ROLLBACK_PENDING = 'the other slot starts next; restart first';
const ERR_FAILED_BEFORE = 'this version failed to start before';
const ERR_BUSY = 'another update is in progress';

// REALISTIC is how long the slow calls take on a real box (setSlow); other
// calls then take 80 ms.
const REALISTIC = {
  'GET /sunshine': 1200,
  'POST /sunshine/pair': 2500,
  'GET /sunshine/clients': 400,
  'GET /storage': 900,
  'POST /storage/libraries': 1500,
  'GET /power': 800,
  'POST /update/check': 4000,
  'GET /install/probe': 2500,
};

// ---------- the engine ----------

// clocks: real (the demo) or virtual (the vectors; time moves with advance).
function realClock() {
  return { now: () => Date.now(), virtual: false };
}

export function virtualClock(start = Date.now()) {
  let t = start;
  return { now: () => t, set: (x) => (t = x), virtual: true };
}

function randomBytes(n) {
  const b = new Uint8Array(n);
  globalThis.crypto.getRandomValues(b);
  return b;
}

const hex = (b) => Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('');
const b64url = (b) => b64encode(b).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');

// createEngine makes an engine over fixtures ({base, presets, scripts}, as
// fixtures.js exports them). opts: preset (default idle), clock (the real
// one by default; virtualClock() moves only with advance), restore (a
// save(), from an earlier page), slow (realistic latency), random (n →
// Uint8Array). The demo's runtime then calls signIn() and installs
// transport(engine); watch(), publish(), runScript(), setDown(),
// loadPreset() and state() are the dev server's /__dev controls.
export function createEngine(fixtures, opts = {}) {
  return new Engine(fixtures, opts);
}

export class Engine {
  constructor(fixtures, opts = {}) {
    this.fx = fixtures;
    this.clock = opts.clock || realClock();
    this.random = opts.random || randomBytes;
    this.watchers = new Set();
    this.streams = new Set(); // open event streams (never saved)
    this.timer = 0;
    this.taskAt = null;
    if (opts.restore) {
      this.s = clone(opts.restore);
    } else {
      this.s = {
        v: 1,
        preset: '',
        slow: !!opts.slow,
        auth: { admin: true, password: DEV_PASSWORD }, // auth.json
        sessions: {}, // sessions.json: token → {csrf}
        client: { cookies: {} }, // the demo's one browser (transport)
        down: false,
        boot: null, // what a woken box boots with
        world: null, // this boot of the box
        worldSeq: 0,
        tasks: [], // [{seq, at, kind, world, data}], in time order
        taskSeq: 0,
      };
      this.loadPreset(opts.preset || 'idle');
    }
    this.arm();
  }

  now() {
    return this.taskAt ?? this.clock.now();
  }

  get w() {
    return this.s.world;
  }

  // ---------- tasks ----------

  schedule(kind, delay, data = {}, scope = 'world') {
    const t = { seq: ++this.s.taskSeq, at: this.now() + delay, kind, world: scope === 'world' ? this.w.id : 0, data };
    const i = this.s.tasks.findIndex((x) => x.at > t.at);
    if (i < 0) this.s.tasks.push(t);
    else this.s.tasks.splice(i, 0, t);
    this.arm();
    return t;
  }

  cancelTasks(fn) {
    this.s.tasks = this.s.tasks.filter((t) => !fn(t));
  }

  runTask(t) {
    if (t.world && (!this.w || t.world !== this.w.id || this.s.down)) return;
    this.taskAt = t.at;
    try {
      TASKS[t.kind](this, t.data, t);
    } finally {
      this.taskAt = null;
    }
  }

  // advance moves a virtual clock on by ms, running each task at its time.
  advance(ms) {
    const end = this.clock.now() + ms;
    for (;;) {
      const t = this.s.tasks[0];
      if (!t || t.at > end) break;
      this.s.tasks.shift();
      if (this.clock.set) this.clock.set(Math.max(t.at, this.clock.now()));
      this.runTask(t);
    }
    if (this.clock.set) this.clock.set(Math.max(end, this.clock.now()));
  }

  // nextTask is when the next task runs (Infinity: none).
  nextTask() {
    return this.s.tasks.length ? this.s.tasks[0].at : Infinity;
  }

  // pump runs every task that is due (on a real clock, late ones in order
  // at their own times: a page that comes back catches up).
  pump() {
    if (this.clock.virtual) return;
    const now = this.clock.now();
    while (this.s.tasks.length && this.s.tasks[0].at <= now) this.runTask(this.s.tasks.shift());
    this.arm();
  }

  arm() {
    if (this.clock.virtual || typeof setTimeout !== 'function') return;
    clearTimeout(this.timer);
    this.timer = 0;
    if (!this.s.tasks.length) return;
    const wait = Math.max(0, Math.min(this.s.tasks[0].at - this.clock.now(), 2 ** 31 - 1));
    this.timer = setTimeout(() => this.pump(), wait);
  }

  // stop drops the real timer (the engine can be restored later).
  stop() {
    clearTimeout(this.timer);
    this.timer = 0;
  }

  save() {
    return clone(this.s);
  }

  // ---------- watchers (the demo's page and its parent) ----------

  // watch calls fn with {type: 'event', topic, data} for every event,
  // {type: 'down'} and {type: 'up'}; it returns the unwatch function.
  watch(fn) {
    this.watchers.add(fn);
    return () => this.watchers.delete(fn);
  }

  tell(msg) {
    for (const fn of this.watchers) {
      try {
        fn(msg);
      } catch (e) {
        console.error('demo engine watcher:', e);
      }
    }
  }

  // ---------- the box: presets, boots, restarts (devserver_test.go) ----------

  presetDocs(p, now) {
    const docs = {};
    for (const [name, v] of Object.entries(this.fx.base)) docs[name] = clone(v);
    for (const [name, patch] of Object.entries(p.patch || {})) docs[name] = mergePatch(docs[name], resolveTimes(clone(patch), now));
    for (const name of Object.keys(docs)) docs[name] = resolveTimes(docs[name], now);
    return docs;
  }

  // loadPreset resets everything to base plus the preset.
  loadPreset(name) {
    const p = this.fx.presets[name];
    if (!p) throw new Error(`no preset ${name}`);
    const docs = this.presetDocs(p, this.now());
    if (p.auth === 'first-run') this.s.auth = { admin: false, password: '' };
    else this.s.auth = { admin: true, password: DEV_PASSWORD };
    if (p.auth === 'signed-out') this.s.sessions = {};
    this.cancelTasks((t) => t.kind === 'script' || t.kind === 'wake');
    this.endWorld();
    this.s.preset = name;
    this.s.down = false;
    this.s.boot = null;
    this.bootWorld(p, docs, p.mode === 'installer', true);
    this.tell({ type: 'preset', name });
  }

  // bootWorld starts a boot of the box (newWorld): a new api core with an
  // empty event hub, and the fake services on docs.
  bootWorld(p, docs, installer, fresh) {
    const now = this.now();
    const setupCode = installer || !this.s.auth.admin ? DEV_SETUP_CODE : '';
    this.s.world = {
      id: ++this.s.worldSeq,
      installer,
      waived: installer && !!p.headless,
      setupCode,
      version: asStr(asObj(docs.update).booted) || 'dev',
      docs,
      errs: clone(p.errors || {}),
      lat: clone(p.latency || {}),
      webActivity: asObj(p.sim).web_activity !== false,
      trial: !!asObj(p.sim).trial,
      installFail: asStr(asObj(p.sim).install_fail),
      booted: now - asNum(asObj(docs.system).uptime_s) * 1000,
      lastTouch: 0,
      idleSince: now - asNum(asObj(p.sim).idle_seconds) * 1000,
      powerSent: '',
      restMode: clone(asObj(docs.display).current ?? null),
      stage: null,
      installing: false,
      installed: null,
      last: {}, // the hub's last event of each state topic: topic → JSON
      limits: { login: {}, setup: {} },
    };
    if (!installer) {
      // vosd publishes pairing.state once its first poll of Sunshine is done.
      this.schedule('first-pairing', 1000);
      this.schedule('power-tick', POWER_INTERVAL);
      const st = asObj(p.sim).stage;
      if (st) this.startStage('', st);
    }
    this.schedule('heartbeat', HEARTBEAT);
    if (fresh) for (const [topic, data] of p.events || []) this.publishRaw(topic, data);
    this.tell({ type: 'up' });
  }

  // endWorld ends this boot: its streams close and its tasks stop.
  endWorld() {
    const w = this.w;
    if (!w) return;
    this.cancelTasks((t) => t.world === w.id);
    for (const st of Array.from(this.streams)) if (st.world === w.id) st.end();
  }

  // settle publishes the first pairing.state now instead of a second
  // after the boot (the vectors' start; a no-op once one is published).
  settle() {
    if (this.w && !this.s.down && !this.w.installer) this.firstPairingState();
  }

  // goDown takes the box down with what it boots with next: for ms, or
  // until woken when ms < 0.
  goDown(ms, boot) {
    if (this.s.down) return;
    this.s.down = true;
    this.s.boot = boot;
    this.cancelTasks((t) => t.kind === 'wake');
    if (ms >= 0) this.schedule('wake', ms, {}, 'server');
    this.endWorld();
    this.tell({ type: 'down' });
  }

  // wake boots the box again with the state it went down with.
  wake() {
    if (!this.s.down) return;
    const b = this.s.boot;
    this.cancelTasks((t) => t.kind === 'wake');
    if (b.preset) this.s.preset = b.preset;
    const p = this.fx.presets[this.s.preset];
    if (b.password) this.s.auth = { admin: true, password: b.password };
    this.s.down = false;
    this.s.boot = null;
    this.bootWorld(p, b.docs, b.installer, false);
    this.w.errs = clone(b.errs);
  }

  // setDown restarts (seconds > 0), wakes (0) or powers off (< 0).
  setDown(seconds) {
    if (seconds === 0) {
      this.wake();
      return;
    }
    if (this.s.down) return;
    this.goDown(seconds < 0 ? -1 : seconds * 1000, this.bootState(null));
  }

  // bootState is what the box boots with next: the documents as they are,
  // with a fresh uptime and nothing running, into next_boot.
  bootState(change) {
    const w = this.w;
    const docs = clone(w.docs);
    asObj(docs.system).uptime_s = 0;
    const up = docs.update || (docs.update = {});
    up.busy = false;
    up.progress = null;
    const sun = docs.sunshine || (docs.sunshine = {});
    const disp = docs.display || (docs.display = {});
    if (sun.streaming === true) {
      sun.streaming = false;
      sun.session = null;
      disp.state = 'welcome';
      disp.current = clone(w.restMode);
    }
    disp.reboot_needed = false;
    sun.pairings = [];
    sun.pending_pairing = false;
    if (!w.installer) bootNext(docs);
    if (change) change(docs);
    return { docs, errs: clone(w.errs), installer: w.installer, preset: '', password: '' };
  }

  // restart answers first, then takes the box down a second later for ms
  // (or until woken when ms < 0), as system.handlePower does; boot, when
  // given, is the system it starts instead of this one.
  restart(ms, boot = null) {
    this.schedule('restart', 1000, { ms, boot });
  }

  // runScript runs fixtures/scripts/<name> on the clock (POST /__dev/script).
  runScript(name) {
    const s = this.fx.scripts[name];
    if (!s) throw new Error(`no script ${name}`);
    if (!s.steps.length) return;
    this.schedule('script', s.steps[0].after || 0, { name, i: 0, preset: this.s.preset }, 'server');
  }

  // scriptStep is one step of a script: a reset, a restart or wake, then
  // its patch and events (skipped while the box is down).
  scriptStep(st, preset) {
    if (st.reset) {
      this.loadPreset(preset);
      return false;
    }
    if (st.down !== undefined && st.down !== null) this.setDown(st.down);
    if (!this.s.down) this.applyStep(st);
    return true;
  }

  // applyStep applies a patch (and update.state when it patches the
  // update) and publishes events.
  applyStep(st) {
    const docs = this.w.docs;
    for (const [name, patch] of Object.entries(st.patch || {})) {
      docs[name] = mergePatch(docs[name], resolveTimes(clone(patch), this.now()));
    }
    if (st.patch && has(st.patch, 'update')) this.emit('update.state', this.updateState());
    for (const [topic, data] of st.events || []) this.publishRaw(topic, data);
  }

  // publish is POST /__dev/event: the services follow it, then it is
  // published. False while the box is down.
  publish(topic, data = {}) {
    if (this.s.down || !topic) return false;
    this.publishRaw(topic, data);
    return true;
  }

  setSlow(on) {
    this.s.slow = !!on;
  }

  // state is GET /__dev/state.
  state() {
    const st = {
      preset: this.s.preset,
      presets: Object.keys(this.fx.presets).sort(),
      scripts: Object.keys(this.fx.scripts).sort(),
      down: this.s.down,
      latency: this.s.slow,
    };
    if (!this.s.down) {
      st.installer = this.w.installer;
      st.docs = clone(this.w.docs);
    }
    return st;
  }

  // ---------- the event hub (internal/events) ----------

  publishRaw(topic, data) {
    this.emit(topic, resolveTimes(clone(data === undefined ? null : data), this.now()));
  }

  // emit lets the services follow an event, then publishes it.
  emit(topic, data) {
    this.follow(topic, data);
    this.hubPublish(topic, data);
  }

  hubPublish(topic, data) {
    const w = this.w;
    const json = JSON.stringify(data === undefined ? null : data);
    if (topic !== 'system.message' && topic !== 'pairing.pending') w.last[topic] = json;
    if (topic === 'session.begin') delete w.last['session.end'];
    if (topic === 'session.end') delete w.last['session.begin'];
    for (const st of Array.from(this.streams)) if (st.world === w.id) st.send(topic, json);
    this.tell({ type: 'event', topic, data: JSON.parse(json) });
  }

  // replay is the hub's last event of each state topic, sorted by topic.
  replay() {
    return Object.keys(this.w.last)
      .sort()
      .map((topic) => ({ topic, data: this.w.last[topic] }));
  }

  doc(name) {
    const docs = this.w.docs;
    if (!isObj(docs[name])) docs[name] = {};
    return docs[name];
  }

  // follow is what the real services do when an event is published.
  follow(topic, data) {
    const m = asObj(data);
    const sun = this.doc('sunshine');
    const disp = this.doc('display');
    switch (topic) {
      case 'session.begin': {
        const sess = clone(m);
        if (!asStr(sess.since)) sess.since = rfc3339(this.now());
        if (disp.state !== 'streaming') this.w.restMode = clone(disp.current ?? null);
        sun.streaming = true;
        sun.session = sess;
        disp.state = 'streaming';
        if (asStr(m.mode)) disp.current = m.mode;
        this.rememberDevice(asStr(m.client), asStr(m.mode), m.hdr === true);
        this.powerCheck(this.now(), false);
        break;
      }
      case 'session.end':
        sun.streaming = false;
        sun.session = null;
        disp.state = asList(disp.connectors).some((c) => asObj(c).physical === true) ? 'welcome' : 'none';
        disp.current = clone(this.w.restMode);
        this.powerCheck(this.now(), false);
        break;
      case 'pairing.state': {
        const ps = Array.isArray(m.pairings) ? m.pairings : [];
        sun.pairings = clone(ps);
        sun.pending_pairing = ps.length > 0;
        break;
      }
      case 'sunshine.state':
        sun.running = m.running === true;
        break;
      case 'update.state': {
        const up = this.doc('update');
        for (const k of UPDATE_STATE_KEYS) {
          delete up[k];
          if (has(m, k)) up[k] = clone(m[k]);
        }
        break;
      }
    }
  }

  // rememberDevice records a client's last mode, newest first.
  rememberDevice(name, mode, hdr) {
    if (!name || !mode) return;
    const disp = this.doc('display');
    const devs = [{ name, mode, hdr, last_seen: rfc3339(this.now()) }];
    for (const d of asList(disp.devices)) if (asStr(asObj(d).name) !== name) devs.push(d);
    disp.devices = devs;
  }

  firstPairingState() {
    if (has(this.w.last, 'pairing.state')) return;
    this.emit('pairing.state', { pairings: this.sunshineAnswer().pairings });
  }

  // ---------- HTTP ----------

  // latency is how long a request waits before it is answered.
  latency(method, path) {
    if (this.s.down || !this.w) return 0;
    const route = this.route(method.toUpperCase(), path.split('?')[0].slice(PREFIX.length));
    if (!route || !route.fake) return 0;
    const key = `${route.method} ${route.path}`;
    if (has(this.w.lat, key)) return this.w.lat[key];
    return this.s.slow ? REALISTIC[key] ?? 80 : 0;
  }

  // handle answers one request: {method, path (with the query), headers,
  // body} → {status, headers, body}, or null while the box is down (the
  // connection drops). Headers are matched case-insensitively.
  handle(req) {
    this.pump();
    if (this.s.down) return null;
    const method = String(req.method || 'GET').toUpperCase();
    const url = new URL(req.path, 'http://vapor.local');
    const headers = {};
    for (const [k, v] of Object.entries(req.headers || {})) headers[k.toLowerCase()] = String(v);
    const r = { method, path: url.pathname, query: url.searchParams, headers, body: req.body, cookies: parseCookies(headers.cookie) };
    if (!url.pathname.startsWith(PREFIX + '/')) return rawAnswer(404, { 'content-type': 'text/plain; charset=utf-8' }, '404 page not found\n');
    r.rest = url.pathname.slice(PREFIX.length);
    // A write from another page is refused (middleware.go sameOrigin).
    if (method !== 'GET' && method !== 'HEAD' && headers.origin && headers.host) {
      let same = false;
      try {
        same = new URL(headers.origin).host === headers.host;
      } catch {
        same = false;
      }
      if (!same) return errorAnswer(403, 'cross-origin request refused');
    }
    const route = this.route(method, r.rest);
    if (!route) return this.noRoute(r);
    r.params = route.params;
    const res = { cookies: [] };
    const denied = this.guard(route.access, r, res);
    if (denied) return withCookies(denied, res.cookies);
    if (route.fake) {
      const e = this.w.errs[`${route.method} ${route.path}`];
      if (e) return withCookies(errorAnswer(e[0], String(e[1])), res.cookies);
    }
    let out = route.h.call(this, r, res);
    if (!isAnswer(out)) out = jsonAnswer(200, out);
    return withCookies(out, res.cookies);
  }

  routes() {
    return this.w.installer ? ROUTES_INSTALLER : ROUTES_OS;
  }

  route(method, rest) {
    const m = method === 'HEAD' ? 'GET' : method;
    for (const rt of this.routes()) {
      if (rt.method !== m) continue;
      const params = matchPath(rt.path, rest);
      if (params) return { ...rt, params };
    }
    return null;
  }

  // noRoute is 405 when the path exists for another method, else 404.
  noRoute(r) {
    const allow = [];
    for (const rt of this.routes()) {
      if (matchLoose(rt.path, r.rest)) {
        allow.push(rt.method);
        if (rt.method === 'GET') allow.push('HEAD');
      }
    }
    if (allow.length) return errorAnswer(405, `method ${r.method} not allowed`, { allow: allow.join(', ') });
    return errorAnswer(404, 'no such API endpoint');
  }

  // ---------- the core's access control (middleware.go, setup.go) ----------

  // session is the request's live session, or null (a stale cookie is
  // cleared).
  session(r, res) {
    const token = r.cookies.vos_session;
    if (!token) return null;
    const rec = this.s.sessions[token];
    if (!rec) {
      if (res) res.cookies.push(clearCookie('vos_session'));
      return null;
    }
    return { token, csrf: rec.csrf };
  }

  startSession(res) {
    const tokens = Object.keys(this.s.sessions);
    while (tokens.length >= 32) delete this.s.sessions[tokens.shift()];
    const token = b64url(this.random(32));
    const csrf = b64url(this.random(32));
    this.s.sessions[token] = { csrf };
    if (res) res.cookies.push(`vos_session=${token}; Path=/; Max-Age=2592000; HttpOnly; SameSite=Strict`);
    return { token, csrf };
  }

  // signIn gives the demo's browser a session, as signing in does (without
  // counting as activity).
  signIn() {
    const s = this.startSession(null);
    this.s.client.cookies.vos_session = s.token;
    return s;
  }

  guard(access, r, res) {
    switch (access) {
      case PUBLIC:
        return null;
      case AUTHED: {
        const info = this.session(r, res);
        if (!info) return errorAnswer(401, 'login required');
        if (r.method !== 'GET' && r.method !== 'HEAD' && r.headers['x-vos-csrf'] !== info.csrf) {
          return errorAnswer(403, 'missing or invalid CSRF token');
        }
        r.session = info;
        this.touchUnlessPassive(r);
        return null;
      }
      case SETUP:
        return this.requireSetup(r, res);
      case AUTHED_OR_SETUP: {
        const info = this.session(r, res);
        if (info) {
          r.session = info;
          this.touchUnlessPassive(r);
          return null;
        }
        return this.requireSetup(r, res);
      }
    }
    return errorAnswer(403, 'forbidden');
  }

  requireSetup(r, res) {
    const c = this.checkSetup(r);
    if (c.res === 'ok') {
      this.touchUnlessPassive(r);
      return null;
    }
    if (c.res === 'limited') return tooMany(c.retry);
    if (c.res === 'wrong') {
      if (c.fromCookie) res.cookies.push(clearCookie('vos_setup'));
      return errorAnswer(403, 'wrong or expired setup code');
    }
    return errorAnswer(403, 'setup code required');
  }

  checkSetup(r) {
    const w = this.w;
    if (w.waived) return { res: 'ok' };
    let given = r.headers['x-vos-setup'] || '';
    let fromCookie = false;
    if (!given && r.cookies.vos_setup) {
      given = r.cookies.vos_setup;
      fromCookie = true;
    }
    if (!given) return { res: 'none' };
    if (!w.setupCode) return { res: 'wrong', fromCookie };
    const lim = limitCheck(w.limits.setup, this.now());
    if (lim) return { res: 'limited', retry: lim, fromCookie };
    if (codesEqual(given, w.setupCode)) return { res: 'ok', fromCookie };
    limitFail(w.limits.setup, this.now());
    return { res: 'wrong', fromCookie };
  }

  touchUnlessPassive(r) {
    if (r.headers['x-vos-passive'] === '1' || r.query.get('passive') === '1') return;
    this.touch();
  }

  // touch is the api's activity hook: a signed-in request that is not
  // passive keeps the box busy for five minutes.
  touch() {
    const w = this.w;
    if (w.installer || !w.webActivity) return;
    w.lastTouch = this.now();
    this.powerCheck(this.now(), false);
  }

  // checkPassword is the core's password check under the login limit:
  // null when right, else the answer.
  checkPassword(password, wrongStatus, wrongMsg) {
    const lim = this.w.limits.login;
    const now = this.now();
    const locked = limitCheck(lim, now);
    if (locked) return tooMany(locked);
    if (!this.s.auth.admin) return errorAnswer(409, 'no admin password is set yet; finish setup first');
    if (password !== this.s.auth.password) {
      limitFail(lim, now);
      return errorAnswer(wrongStatus, wrongMsg);
    }
    limitReset(lim);
    return null;
  }

  // ---------- event streams ----------

  // openStream is GET /events: the answer when refused (or null while the
  // box is down), else a stream {replay, onevent, onend, close}.
  openStream(req) {
    this.pump();
    if (this.s.down) return null;
    const headers = {};
    for (const [k, v] of Object.entries(req.headers || {})) headers[k.toLowerCase()] = String(v);
    const url = new URL(req.path || PREFIX + '/events', 'http://vapor.local');
    const r = { method: 'GET', path: url.pathname, query: url.searchParams, headers, cookies: parseCookies(headers.cookie) };
    const res = { cookies: [] };
    const denied = this.guard(AUTHED_OR_SETUP, r, res);
    if (denied) return { answer: withCookies(denied, res.cookies) };
    const eng = this;
    const st = {
      world: this.w.id,
      session: r.session ? r.session.token : '',
      setup: r.session ? '' : headers['x-vos-setup'] || r.cookies.vos_setup || '',
      replay: this.replay(),
      onevent: null,
      onend: null,
      closed: false,
      send(topic, data) {
        if (!st.closed && st.onevent) st.onevent({ topic, data });
      },
      end() {
        if (st.closed) return;
        st.closed = true;
        eng.streams.delete(st);
        if (st.onend) st.onend();
      },
      close() {
        st.closed = true;
        eng.streams.delete(st);
      },
    };
    this.streams.add(st);
    return { stream: st };
  }

  // stillAllowed is the heartbeat's check of a stream's credential.
  stillAllowed(st) {
    if (st.session) return has(this.s.sessions, st.session);
    const w = this.w;
    if (w.waived) return true;
    return !!w.setupCode && codesEqual(st.setup, w.setupCode);
  }

  // ---------- documents as the routes answer them ----------

  systemAnswer() {
    const s = { ...this.doc('system') };
    const hn = asStr(s.hostname);
    s.hostname = hn;
    s.mdns = hn.split('.')[0] + '.local';
    s.uptime_s = Math.trunc((this.now() - this.w.booted) / 1000);
    return s;
  }

  // sunshineAnswer is GET /sunshine: the session only while streaming, and
  // nobody waiting while Sunshine is stopped.
  sunshineAnswer() {
    const s = { ...this.doc('sunshine') };
    let ps = Array.isArray(s.pairings) ? s.pairings : [];
    if (!Array.isArray(s.pairings) || s.running === false) ps = [];
    s.pairings = ps;
    s.pending_pairing = ps.length > 0;
    if (s.streaming !== true) s.session = null;
    return s;
  }

  updateAnswer() {
    const u = { ...this.doc('update') };
    u.busy = !!this.w.stage;
    u.progress = this.w.stage ? this.w.stage.progress : null;
    return u;
  }

  // updateState is the update-state part (held and checked only when set).
  updateState() {
    const u = this.doc('update');
    const st = {};
    for (const k of UPDATE_STATE_KEYS) {
      if (!has(u, k)) continue;
      const v = u[k];
      if ((k === 'held' || k === 'checked') && (v === null || v === '')) continue;
      st[k] = clone(v);
    }
    return st;
  }

  // displayDoc is the display document with learned rebuilt from added and
  // devices; a change asks for a restart.
  displayDoc() {
    const d = this.doc('display');
    const learned = learnedModes(d);
    if (!listsEqual(learned, asList(d.learned))) {
      d.learned = learned;
      d.reboot_needed = true;
    }
    return d;
  }

  // ---------- power (devserver_power_test.go) ----------

  busy(now) {
    const p = this.doc('power');
    const until = parseTime(p.keep_awake_until);
    if (now < until) return { reason: 'keep-awake', web: false };
    const s = this.doc('sunshine');
    if (s.streaming === true) return { reason: 'streaming to ' + asStr(asObj(s.session).client), web: false };
    const stage = this.w.stage;
    if (stage) {
      const pr = stage.progress;
      if (pr.phase !== 'check' && asStr(pr.version) !== '') {
        return { reason: `installing update ${asStr(pr.version)} (${Math.trunc(asNum(pr.percent))}%)`, web: false };
      }
      return { reason: 'checking for updates', web: false };
    }
    if (this.webUntil(now) > now) return { reason: WEB_REASON, web: true };
    return { reason: '', web: false };
  }

  webUntil(now) {
    const t = this.w.lastTouch;
    if (!t || now >= t + WEB_WINDOW) return 0;
    return t + WEB_WINDOW;
  }

  powerState(now) {
    const p = { ...this.doc('power') };
    if (!(now < parseTime(p.keep_awake_until))) delete p.keep_awake_until;
    delete p.web_until;
    const until = this.webUntil(now);
    if (until) p.web_until = rfc3339(until);
    p.busy = null;
    p.idle_seconds = 0;
    p.shutdown_in = null;
    const b = this.busy(now);
    if (b.reason) {
      p.busy = fakeBusy(b);
      return p;
    }
    const idle = now - this.w.idleSince;
    p.idle_seconds = Math.trunc(idle / 1000);
    const left = this.shutdownIn(idle);
    if (left !== null) p.shutdown_in = left;
    return p;
  }

  shutdownIn(idle) {
    const p = this.doc('power');
    const limit = asNum(p.idle_minutes) * 60000;
    if (p.idle_shutdown !== true || limit <= 0) return null;
    return Math.max(Math.trunc((limit - idle) / 1000), 0);
  }

  // powerCheck is power.tick: busy publishes power.idle once per reason;
  // idle publishes it on every pass, and powers off once idle for
  // idle_minutes.
  powerCheck(now, tick) {
    const w = this.w;
    if (w.installer) return;
    const b = this.busy(now);
    if (b.reason) {
      w.idleSince = now;
      if (w.powerSent !== b.reason) {
        w.powerSent = b.reason;
        this.hubPublish('power.idle', { idle_seconds: 0, shutdown_in: null, busy: fakeBusy(b) });
      }
      return;
    }
    if (!tick && w.powerSent === 'idle') return;
    w.powerSent = 'idle';
    const idle = now - w.idleSince;
    const ev = { idle_seconds: Math.trunc(idle / 1000), shutdown_in: null, busy: null };
    const left = this.shutdownIn(idle);
    if (left !== null) ev.shutdown_in = left;
    this.hubPublish('power.idle', ev);
    if (left === 0 && tick) {
      const minutes = Math.trunc(asNum(this.doc('power').idle_minutes));
      this.emit('system.message', {
        level: 'info',
        text: `Nothing has happened for ${minutes} minutes, so VaporOS is switching off. Wake it with Wake-on-LAN (Moonlight does this for you).`,
      });
      this.restart(-1);
    }
  }

  // ---------- the update stage (devserver_update_test.go) ----------

  startStage(version, from) {
    const w = this.w;
    w.stage = { progress: { phase: 'check', percent: 0, bytes: 0, total: 0, version: '' }, cancelled: false, version, from: from || null };
    if (!from) this.emit('update.progress', w.stage.progress);
    this.schedule('stage-begin', from ? 0 : 600);
  }

  // stageBegin refuses what vosd refuses, then walks the phases.
  stageBegin() {
    const w = this.w;
    const stage = w.stage;
    if (!stage) return;
    const u = this.doc('update');
    const avail = asObj(u.available);
    let target = stage.version;
    const size = asNum(avail.size) || 1420000000;
    if (!target) target = asStr(avail.version);
    const refuse = (p) => {
      w.stage = null;
      this.emit('update.progress', p);
    };
    const staged = asStr(asObj(u.staged).version);
    if (w.trial) {
      refuse({ phase: 'error', percent: 0, bytes: 0, total: 0, version: '', error: `${ERR_ON_TRIAL} (${asStr(u.booted)}, 2 tries left)` });
      return;
    }
    if (u.next_boot !== undefined && u.next_boot !== null && staged === '') {
      refuse({ phase: 'error', percent: 0, bytes: 0, total: 0, version: '', error: ERR_ROLLBACK_PENDING });
      return;
    }
    if (target === '' || target === staged || target === asStr(u.booted)) {
      refuse({ phase: 'idle', percent: 0, bytes: 0, total: 0, version: '' });
      return;
    }
    const start = stage.from ? STAGE_PHASES.findIndex((p) => p.name === stage.from.phase) : -1;
    stage.walk = { target, size, start, i: Math.max(start, 0), s: 0 };
    this.stageTick();
  }

  // stageTick is one step of a phase: update.progress with the percent of
  // the whole update and the bytes of this phase.
  stageTick() {
    const w = this.w;
    const stage = w.stage;
    if (!stage) return;
    const k = stage.walk;
    if (k.i >= STAGE_PHASES.length) {
      this.stageDone(k.target);
      return;
    }
    const ph = STAGE_PHASES[k.i];
    if (stage.cancelled && ph.name !== 'install') {
      const p = { phase: 'cancelled', percent: stage.progress.percent, bytes: 0, total: 0, version: k.target };
      w.stage = null;
      if (ph.name !== 'download') {
        // Writing began by unhooking the idle slot: no rollback target
        // remains until the next stage.
        const u = this.doc('update');
        u.other_slot = null;
        u.next_boot = null;
      }
      this.emit('update.progress', p);
      return;
    }
    const total = ph.bytes || k.size;
    const first = k.i === k.start ? Math.trunc(((stage.from.percent - ph.from) * 100) / Math.max(ph.to - ph.from, 1)) : 0;
    const steps = Math.trunc(ph.took / STAGE_TICK);
    const own = first + Math.trunc(((100 - first) * k.s) / Math.max(steps, 1));
    stage.progress = {
      phase: ph.name,
      percent: ph.from + Math.trunc(((ph.to - ph.from) * own) / 100),
      bytes: Math.trunc((total * own) / 100),
      total,
      version: k.target,
    };
    this.emit('update.progress', stage.progress);
    if (k.s < steps) k.s++;
    else {
      k.i++;
      k.s = 0;
    }
    this.schedule('stage-tick', STAGE_TICK);
  }

  stageDone(target) {
    const w = this.w;
    const u = this.doc('update');
    const other = u.booted_slot === 'b' ? 'a' : 'b';
    u.staged = { version: target, slot: other, at: rfc3339(this.now()) };
    u.last_error = '';
    u.other_slot = { version: target, running: false, bootable: true, counting: true, tries_left: 3, tries_done: 0, entry: `vos-${target}+3.conf` };
    u.next_boot = { slot: other, version: target };
    const held = asObj(u.held);
    if (Object.keys(held).length && compareVersions(target, asStr(held.version)) >= 0) delete u.held;
    w.stage = null;
    this.emit('update.state', this.updateState());
    this.emit('update.progress', { phase: 'done', percent: 100, bytes: 0, total: 0, version: target });
  }

  rollback() {
    const u = this.doc('update');
    const other = isObj(u.other_slot) ? u.other_slot : {};
    const slot = u.booted_slot === 'b' ? 'a' : 'b';
    const v = asStr(other.version);
    if (v === '') return `slot ${slot} has nothing bootable`;
    if (asList(u.failed).includes(v)) return `${ERR_FAILED_BEFORE}: ${v} in slot ${slot}`;
    if (other.bootable !== true && asNum(other.tries_done) > 1) return `slot ${slot} (${v}) used up its boot attempts without starting`;
    const booted = asStr(u.booted);
    const held = asObj(u.held);
    if (compareVersions(v, booted) < 0) u.held = { version: booted, rollback_index: 1790684100 };
    else if (Object.keys(held).length && compareVersions(v, asStr(held.version)) >= 0) delete u.held;
    other.bootable = true;
    u.next_boot = { slot, version: v };
    this.emit('update.state', this.updateState());
    return '';
  }

  // ---------- the installer (devserver_install_test.go) ----------

  setInstall(state, step, percent, message, error) {
    this.w.docs['install-status'] = { state, step, percent, message, error };
    this.emit('install.progress', { step, percent, message, state });
  }

  checkInstall(req) {
    const disk = goTrim(asStr(req.disk));
    if (!disk) return { error: 'no disk given' };
    let mode = goTrim(asStr(req.mode)).toLowerCase();
    if (mode === '') mode = 'erase';
    else if (mode !== 'erase' && mode !== 'repair') return { error: `mode must be "erase" or "repair", not ${goQuote(mode)}` };
    let hostname = goTrim(asStr(req.hostname)).toLowerCase();
    if (hostname === '' && mode === 'erase') hostname = 'vapor';
    if (hostname !== '' && !/^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/.test(hostname)) {
      return { error: `invalid hostname ${goQuote(hostname)}: use 1-63 letters, digits and hyphens` };
    }
    if (hostname === 'localhost') return { error: 'invalid hostname: "localhost" is reserved' };
    const password = asStr(req.password);
    if (password !== '') {
      if (runeLen(password) < 8) return { error: 'the admin password needs at least 8 characters' };
      if (utf8Len(password) > 1024 || password.includes('\0')) return { error: 'the admin password is not valid' };
    }
    const tz = goTrim(asStr(req.timezone));
    if (tz !== '' && !/^[A-Za-z0-9_+-]+(\/[A-Za-z0-9_+-]+){0,3}$/.test(tz)) return { error: `invalid timezone ${goQuote(tz)}` };
    for (let u of req.libraries || []) {
      u = goTrim(u);
      if (u !== '' && !/^[A-Za-z0-9-]{1,64}$/.test(u)) return { error: `invalid filesystem UUID ${goQuote(u)}` };
    }
    const dev = '/dev/' + (disk.startsWith('/dev/') ? disk.slice(5) : disk);
    for (const x of asList(this.doc('install-probe').disks)) {
      const d = asObj(x);
      if (asStr(d.path) !== dev) continue;
      if (d.is_live === true) return { error: `${dev} holds the VaporOS installer itself; choose another disk` };
      if (mode === 'repair' && d.has_vaporos !== true) return { error: `${dev} holds no VaporOS installation to repair` };
      return { disk: d, mode, hostname, password };
    }
    return { error: `${dev} is not a disk VaporOS can be installed on` };
  }

  // installSteps are the real steps and messages (install/install.go).
  installSteps(dev, mode, hostname) {
    const part = dev + (dev.includes('nvme') ? 'p2' : '2');
    const version = asStr(this.doc('install-probe').version);
    const steps = [
      ['probe', 0, 'Checking the system'],
      ['probe', 1, 'Reading the VaporOS image from /run/vos/medium/vos'],
      ['probe', 2, `Ready to install VaporOS ${version} on ${dev}`],
    ];
    if (mode === 'repair') {
      steps.push(['partition', 3, 'Checking the data partition'], ['partition', 6, 'Formatting the boot partition'], ['partition', 10, 'Disk prepared']);
    } else {
      steps.push(['partition', 3, 'Erasing ' + dev], ['partition', 5, 'Creating partitions'], ['partition', 8, 'Creating filesystems'], ['partition', 10, 'Disk prepared']);
    }
    for (let pct = 10; pct < 75; pct += 8) steps.push(['write', pct, `Writing VaporOS ${version} to ${part}`]);
    steps.push(
      ['verify', 75, 'Verifying ' + part],
      ['verify', 90, 'Image verified'],
      ['bootloader', 90, 'Installing the bootloader'],
      ['bootloader', 92, 'Writing the boot entry'],
      ['bootloader', 95, 'Bootloader installed'],
      ['configure', 95, 'Configuring ' + (hostname !== '' ? hostname : 'the system')],
      ['configure', 99, 'Finishing'],
    );
    return steps;
  }

  installStep(data) {
    const w = this.w;
    const steps = this.installSteps(data.dev, data.mode, data.hostname);
    const [step, pct, msg] = steps[data.i];
    // An image the probe could not trust fails the install once it is read
    // (install.Service.runJob).
    const badImage = asStr(this.doc('install-probe').source_error);
    if (badImage && pct === 2) {
      w.installing = false;
      this.setInstall('failed', step, 1, 'Installation failed: ' + badImage, badImage);
      return;
    }
    if (w.installFail && step === 'write') {
      w.installing = false;
      this.setInstall('failed', step, pct, 'Installation failed: ' + w.installFail, w.installFail);
      return;
    }
    this.setInstall('running', step, pct, msg, '');
    if (data.i + 1 < steps.length) {
      this.schedule('install-step', 700, { ...data, i: data.i + 1 });
      return;
    }
    w.installing = false;
    this.setInstall('done', 'done', 100, `VaporOS ${asStr(this.doc('install-probe').version)} is installed on ${data.dev}`, '');
    // What the restart boots: a fresh system (the empty preset) with the
    // name and password from the wizard. A repair keeps the disk's name.
    const docs = this.presetDocs(this.fx.presets.empty, this.now());
    const hostname = data.hostname || data.diskHostname || 'vapor';
    const sys = docs.system || (docs.system = {});
    sys.hostname = hostname;
    sys.mdns = hostname + '.local';
    sys.uptime_s = 0;
    w.installed = { docs, errs: {}, installer: false, preset: 'empty', password: data.password };
  }
}

// ---------- tasks ----------

const TASKS = {
  'first-pairing': (e) => e.firstPairingState(),
  'power-tick': (e) => {
    e.powerCheck(e.now(), true);
    if (!e.s.down) e.schedule('power-tick', POWER_INTERVAL);
  },
  heartbeat: (e) => {
    for (const st of Array.from(e.streams)) if (st.world === e.w.id && !e.stillAllowed(st)) st.end();
    e.schedule('heartbeat', HEARTBEAT);
  },
  'stage-begin': (e) => e.stageBegin(),
  'stage-tick': (e) => e.stageTick(),
  'end-stream': (e) => {
    if (e.doc('sunshine').streaming === true) {
      e.emit('session.end', {});
      e.emit('display.changed', {});
    }
  },
  restart: (e, d) => e.goDown(d.ms, d.boot ? clone(d.boot) : e.bootState(null)),
  wake: (e) => e.wake(),
  'install-step': (e, d) => e.installStep(d),
  script: (e, d) => {
    const s = e.fx.scripts[d.name];
    const st = s.steps[d.i];
    if (!e.scriptStep(st, d.preset)) return;
    if (d.i + 1 < s.steps.length) e.schedule('script', s.steps[d.i + 1].after || 0, { ...d, i: d.i + 1 }, 'server');
  },
};

// bootNext is what a restart starts: next_boot when set (a staged update or
// a rollback), which becomes the running version, with the old one in the
// other slot.
function bootNext(docs) {
  const u = docs.update || (docs.update = {});
  const sys = docs.system || (docs.system = {});
  const next = asObj(u.next_boot);
  const version = asStr(next.version);
  const slot = asStr(next.slot);
  if (!version) return;
  const old = asStr(u.booted);
  u.booted = version;
  u.booted_slot = slot;
  sys.version = version;
  sys.booted_slot = slot;
  u.staged = null;
  u.next_boot = null;
  // Going back marks the version it leaves bad (boot.MarkBad): its entry
  // counts, with one try done and none left.
  const other = { version: old, running: false, bootable: true, counting: false, tries_left: 0, tries_done: 0, entry: `vos-${old}.conf` };
  if (asStr(asObj(u.held).version) === old) {
    Object.assign(other, { bootable: false, counting: true, tries_done: 1, entry: `vos-${old}+0-1.conf` });
  }
  u.other_slot = other;
  const a = asObj(u.available);
  if (Object.keys(a).length && compareVersions(asStr(a.version), version) <= 0) u.available = null;
}

const fakeBusy = (b) => (b.web ? { reason: b.reason, web: true } : { reason: b.reason });

// ---------- the core's helpers ----------

function withCookies(ans, cookies) {
  if (!cookies.length) return ans;
  return { ...ans, headers: { ...ans.headers, 'set-cookie': cookies.slice() } };
}

function clearCookie(name) {
  return `${name}=; Path=/; Max-Age=0; HttpOnly; SameSite=Strict`;
}

function parseCookies(h) {
  const out = {};
  for (const part of String(h || '').split(';')) {
    const i = part.indexOf('=');
    if (i > 0) out[part.slice(0, i).trim()] = part.slice(i + 1).trim();
  }
  return out;
}

// normalizeCode reads a setup code as a person types it: case, dashes and
// spaces ignored, O as 0 and I or L as 1 (api.NormalizeCode).
function normalizeCode(code) {
  if (utf8Len(code) > 64) return '';
  let out = '';
  for (let ch of code.toUpperCase()) {
    if (ch === '-' || ch === ' ' || ch === '\t') continue;
    if (ch === 'O') ch = '0';
    else if (ch === 'I' || ch === 'L') ch = '1';
    out += ch;
  }
  return out;
}

function codesEqual(given, want) {
  const g = normalizeCode(given);
  const w = normalizeCode(want);
  return g !== '' && w !== '' && g === w;
}

// The limit of wrong passwords and setup codes (api/ratelimit.go): five
// free failures, then 15 s, 30 s, 1 min … up to 15 min each.
function limitCheck(m, now) {
  const st = m.client;
  if (st && now < st.until) return st.until - now;
  return 0;
}

function limitFail(m, now) {
  let st = m.client;
  if (!st || (now >= st.until && now - st.last > 3600000)) st = m.client = { fails: 0, last: 0, until: 0 };
  st.fails++;
  st.last = now;
  if (st.fails >= 5) {
    const shift = st.fails - 5;
    let d = 15 * 60000;
    if (shift < 30) {
      const b = 15000 * 2 ** shift;
      if (b < d) d = b;
    }
    st.until = now + d;
  }
}

function limitReset(m) {
  delete m.client;
}

function tooMany(retryMs) {
  const secs = Math.max(1, Math.ceil(retryMs / 1000));
  return errorAnswer(429, `too many failed attempts; try again in ${goDuration(secs)}`, { 'retry-after': String(secs) });
}

// matchPath matches a ServeMux pattern with {name} segments: the
// parameters, or null.
function matchPath(pattern, path) {
  const ps = pattern.split('/');
  const xs = path.split('/');
  if (ps.length !== xs.length) return null;
  const params = {};
  for (let i = 0; i < ps.length; i++) {
    const p = ps[i];
    if (p.startsWith('{') && p.endsWith('}')) {
      if (!xs[i]) return null;
      try {
        params[p.slice(1, -1)] = decodeURIComponent(xs[i]);
      } catch {
        return null;
      }
    } else if (p !== xs[i]) {
      return null;
    }
  }
  return params;
}

// matchLoose is the core's matchPattern for 405s (slashes trimmed).
function matchLoose(pattern, path) {
  const trim = (s) => s.replace(/^\/+|\/+$/g, '');
  return matchPath('/' + trim(pattern), '/' + trim(path)) !== null;
}

// ---------- routes ----------

// fail writes an error answer the way api.Error does.
const fail = errorAnswer;

const CORE = [
  [
    'GET', '/ping', PUBLIC, function () {
      return { ok: true, mode: this.w.installer ? 'installer' : 'os', version: this.w.version };
    },
  ],
  [
    'GET', '/auth/me', PUBLIC, function (r, res) {
      const info = this.session(r, res);
      return { authenticated: !!info, csrf: info ? info.csrf : '', needs_setup: !this.s.auth.admin, installer: this.w.installer };
    },
  ],
  [
    'POST', '/auth/login', PUBLIC, function (r, res) {
      const b = readJSON(r.body, { password: 'string' });
      if (b.error) return fail(400, b.error);
      const refused = this.checkPassword(b.v.password ?? '', 401, 'wrong password');
      if (refused) return refused;
      // A session cookie the browser arrived with does not survive a login.
      const old = this.session(r, null);
      if (old) delete this.s.sessions[old.token];
      const { csrf } = this.startSession(res);
      this.touch();
      return { csrf };
    },
  ],
  [
    'POST', '/auth/logout', AUTHED, function (r, res) {
      delete this.s.sessions[r.session.token];
      res.cookies.push(clearCookie('vos_session'));
      return {};
    },
  ],
  [
    'POST', '/auth/setup', SETUP, function (r, res) {
      const b = readJSON(r.body, { password: 'string' });
      if (b.error) return fail(400, b.error);
      if (this.w.installer) return fail(409, 'the installer sets the admin password of the installed system');
      const bad = validatePassword(b.v.password ?? '');
      if (bad) return fail(400, bad);
      if (this.s.auth.admin) return fail(409, 'an admin password is already set');
      this.s.auth = { admin: true, password: b.v.password };
      const { csrf } = this.startSession(res);
      res.cookies.push(clearCookie('vos_setup'));
      this.w.setupCode = '';
      return { csrf };
    },
  ],
  [
    'POST', '/auth/password', AUTHED, function (r) {
      const b = readJSON(r.body, { current: 'string', new: 'string' });
      if (b.error) return fail(400, b.error);
      const refused = this.checkPassword(b.v.current ?? '', 403, 'the current password is wrong');
      if (refused) return refused;
      const bad = validatePassword(b.v.new ?? '');
      if (bad) return fail(400, bad);
      this.s.auth = { admin: true, password: b.v.new };
      for (const t of Object.keys(this.s.sessions)) if (t !== r.session.token) delete this.s.sessions[t];
      return {};
    },
  ],
  [
    'GET', '/events', AUTHED_OR_SETUP, function () {
      // Only the replay: a stream is openStream's.
      let body = 'retry: 3000\n\n';
      for (const ev of this.replay()) body += `event: ${ev.topic}\ndata: ${ev.data}\n\n`;
      return rawAnswer(200, { 'content-type': 'text/event-stream', 'cache-control': 'no-store' }, body);
    },
  ],
];

const SYSTEM = [
  ['GET', '/system', AUTHED, function () {
    return this.systemAnswer();
  }],
  ['PUT', '/system/hostname', AUTHED, function (r) {
    const b = readJSON(r.body, { hostname: 'string' });
    if (b.error) return fail(400, b.error);
    const name = goTrim(b.v.hostname ?? '').toLowerCase();
    const bad = validateHostname(name);
    if (bad) return fail(400, bad);
    const sys = this.doc('system');
    sys.hostname = name;
    sys.mdns = name + '.local';
    return {};
  }],
  ['POST', '/system/reboot', AUTHED, function () {
    this.emit('system.message', { level: 'info', text: 'Restarting…' });
    this.restart(8000);
    return {};
  }],
  ['POST', '/system/poweroff', AUTHED, function () {
    this.emit('system.message', { level: 'info', text: 'Shutting down…' });
    this.restart(-1);
    return {};
  }],
  ['GET', '/ssh', AUTHED, function () {
    return this.doc('ssh');
  }],
  ['PUT', '/ssh', AUTHED, function (r) {
    const b = readJSON(r.body, { enabled: 'bool', keys: 'strings' });
    if (b.error) return fail(400, b.error);
    const n = normalizeKeys(b.v.keys || []);
    if (n.error) return fail(400, n.error);
    if (b.v.enabled && !n.keys.length) return fail(400, 'add at least one public key before enabling SSH: the vapor account has no password');
    this.w.docs.ssh = { enabled: !!b.v.enabled, keys: n.keys };
    return this.w.docs.ssh;
  }],
];

const DISPLAY = [
  ['GET', '/display', AUTHED, function () {
    return this.displayDoc();
  }],
  ['POST', '/display/modes', AUTHED, function (r) {
    const b = readJSON(r.body, { mode: 'string' });
    if (b.error) return fail(400, b.error);
    const p = parseMode(b.v.mode ?? '');
    const bad = p.error || checkMode(p.mode);
    if (bad) return fail(400, bad);
    const d = this.displayDoc();
    if (!inCatalogue(p.mode) && !hasMode(asList(d.added), p.mode)) d.added = sortedModes([...asList(d.added), modeString(p.mode)]);
    this.displayDoc();
    this.emit('display.changed', {});
    return { reboot_needed: d.reboot_needed === true };
  }],
  ['DELETE', '/display/modes/{mode}', AUTHED, function (r) {
    const p = parseMode(r.params.mode);
    if (p.error) return fail(400, p.error);
    const d = this.displayDoc();
    const added = asList(d.added);
    const devs = asList(d.devices).filter((dev) => {
      const q = parseMode(asStr(asObj(dev).mode));
      return !q.mode || !sameMode(q.mode, p.mode);
    });
    if (!hasMode(added, p.mode) && devs.length === asList(d.devices).length) {
      return fail(404, `${modeString(p.mode)} is neither an added nor a learned mode`);
    }
    d.added = added.filter((v) => {
      const q = parseMode(asStr(v));
      return !(q.mode && sameMode(q.mode, p.mode));
    });
    d.devices = devs;
    this.displayDoc();
    this.emit('display.changed', {});
    return { reboot_needed: d.reboot_needed === true };
  }],
  ['PUT', '/display/settings', AUTHED, function (r) {
    const b = readJSON(r.body, { hdr: 'bool', virtual_connector: 'string' });
    if (b.error) return fail(400, b.error);
    const d = this.doc('display');
    const c = b.v.virtual_connector;
    if (c !== undefined && c !== asStr(d.virtual_connector)) {
      if (d.profile === 'none') return fail(409, 'no supported GPU for a virtual display');
      const conn = asList(d.connectors).find((x) => asStr(asObj(x).name) === c);
      if (!conn || !(c.startsWith('DP-') || c.startsWith('HDMI-'))) {
        return fail(400, `${goQuote(c)} is not a DP or HDMI connector of ${asStr(asObj(this.doc('system').gpu).name)}`);
      }
      // c becomes the virtual connector from the next boot; the old one is free.
      const old = asStr(d.virtual_connector);
      const free = [...asList(d.available_connectors), old].filter((n) => n !== c && n !== '');
      free.sort((x, y) => (asStr(x) < asStr(y) ? -1 : asStr(x) > asStr(y) ? 1 : 0));
      d.virtual_connector = c;
      d.available_connectors = free;
      d.reboot_needed = true;
    }
    if (b.v.hdr !== undefined) d.hdr = b.v.hdr;
    this.emit('display.changed', {});
    return {};
  }],
];

const UPDATE = [
  ['GET', '/update', AUTHED, function () {
    return this.updateAnswer();
  }],
  ['POST', '/update/check', AUTHED, function () {
    const u = this.doc('update');
    const now = rfc3339(this.now());
    u.checked = now;
    if (isObj(u.available) && Object.keys(u.available).length) u.available.checked = now;
    if (asStr(u.last_error).startsWith('check: ')) u.last_error = '';
    this.emit('update.state', this.updateState());
    return { available: u.available ?? null };
  }],
  ['POST', '/update/stage', AUTHED, function (r) {
    const b = readJSON(r.body, { version: 'string' });
    if (b.error && !b.eof) return fail(400, b.error);
    const version = (b.v && b.v.version) || '';
    if (version !== '' && !validVersion(version)) return fail(400, `invalid version ${goQuote(version)}`);
    if (this.w.stage) return fail(409, ERR_BUSY);
    this.startStage(version, null);
    return {};
  }],
  ['POST', '/update/cancel', AUTHED, function () {
    const st = this.w.stage;
    if (!st) return fail(409, 'no update is running');
    if (st.progress.phase === 'install' || st.progress.phase === 'done') {
      return fail(409, 'VaporOS is already installing the update; it takes a few seconds');
    }
    st.cancelled = true;
    return {};
  }],
  ['POST', '/update/activate', AUTHED, function () {
    const u = this.doc('update');
    if (this.w.stage) return fail(409, 'an update is still being installed');
    if (asStr(asObj(u.staged).version) === '') return fail(409, 'no update is staged');
    this.restart(8000);
    return {};
  }],
  ['POST', '/update/rollback', AUTHED, function () {
    const bad = this.rollback();
    return bad ? fail(409, bad) : {};
  }],
  ['PUT', '/update/settings', AUTHED, function (r) {
    const b = readJSON(r.body, { channel: 'string', auto: 'string' });
    if (b.error) return fail(400, b.error);
    const { channel, auto } = b.v;
    if (channel !== undefined && channel !== '' && !validChannel(channel)) return fail(400, `invalid channel ${goQuote(channel)}`);
    if (auto !== undefined && auto !== 'stage' && auto !== 'off') return fail(400, 'auto must be "stage" or "off"');
    const u = this.doc('update');
    const cfg = isObj(u.config) ? u.config : {};
    if (channel !== undefined) {
      const ch = channel || 'main';
      if (cfg.channel !== ch) {
        // What the old channel offered says nothing about the new one.
        u.available = null;
        this.emit('update.state', this.updateState());
      }
      cfg.channel = ch;
    }
    if (auto !== undefined) cfg.auto = auto;
    u.config = cfg;
    return {};
  }],
];

const SUNSHINE = [
  ['GET', '/sunshine', AUTHED, function () {
    return this.sunshineAnswer();
  }],
  ['POST', '/sunshine/pair', AUTHED, function (r) {
    const b = readJSON(r.body, { pin: 'string', name: 'string', pairing_id: 'string' });
    if (b.error) return fail(400, b.error);
    const pin = goTrim(b.v.pin ?? '');
    let name = goTrim(b.v.name ?? '');
    const pid = b.v.pairing_id ?? '';
    if (!/^[0-9]{4}$/.test(pin)) return fail(400, 'the PIN is the 4 digits Moonlight shows');
    if (utf8Len(name) > 128 || /[\0\r\n]/.test(name)) return fail(400, 'the device name must be at most 128 characters on one line');
    if (pid !== '' && !/^[0-9A-Fa-f]{32}$/.test(pid)) return fail(400, 'invalid pairing_id');
    const s = this.doc('sunshine');
    const ps = asList(s.pairings);
    let id = pid;
    if (id === '') {
      if (ps.length === 0) return fail(409, 'no device is waiting to pair: start pairing in Moonlight, then enter the PIN it shows');
      if (ps.length > 1) return fail(409, `${ps.length} devices are waiting to pair; choose which one this PIN is for`);
      id = asStr(asObj(ps[0]).id);
    }
    let waiting = null;
    const rest = [];
    for (const p of ps) {
      if (asStr(asObj(p).id) === id) waiting = asObj(p);
      else rest.push(p);
    }
    // Sunshine answers false for a wrong PIN and for a device that stopped
    // waiting alike.
    if (!waiting || pin !== DEV_PIN) return fail(400, 'pairing failed: check the PIN and try again');
    if (name === '') name = goTrim(asStr(waiting.name));
    if (name === '') name = 'Moonlight';
    const c = this.doc('sunshine-clients');
    c.clients = [...asList(c.clients), { uuid: this.uuid(), name, enabled: true }];
    this.emit('pairing.state', { pairings: rest });
    return {};
  }],
  ['GET', '/sunshine/clients', AUTHED, function () {
    return this.doc('sunshine-clients');
  }],
  ['DELETE', '/sunshine/clients/{uuid}', AUTHED, function (r) {
    const uuid = r.params.uuid;
    if (!/^[0-9A-Za-z-]{1,64}$/.test(uuid)) return fail(400, 'invalid client uuid');
    const c = this.doc('sunshine-clients');
    const all = asList(c.clients);
    const keep = all.filter((cl) => asStr(asObj(cl).uuid) !== uuid);
    if (keep.length === all.length) return fail(404, `no paired client ${uuid}`);
    c.clients = keep;
    return {};
  }],
  ['GET', '/sunshine/settings', AUTHED, function () {
    return this.doc('sunshine-settings');
  }],
  ['PUT', '/sunshine/settings', AUTHED, function (r) {
    const b = readJSON(r.body, { encoder: 'string', bitrate_kbps_max: 'int', audio_sink: 'string', gamepad: 'string' });
    if (b.error) return fail(400, b.error);
    const doc = this.doc('sunshine-settings');
    const set = {
      encoder: asStr(doc.encoder),
      bitrate_kbps_max: Math.trunc(asNum(doc.bitrate_kbps_max)),
      audio_sink: asStr(doc.audio_sink),
      gamepad: asStr(doc.gamepad),
    };
    if (b.v.encoder !== undefined) set.encoder = b.v.encoder;
    if (b.v.bitrate_kbps_max !== undefined) set.bitrate_kbps_max = b.v.bitrate_kbps_max;
    if (b.v.audio_sink !== undefined) set.audio_sink = goTrim(b.v.audio_sink);
    if (b.v.gamepad !== undefined) set.gamepad = b.v.gamepad;
    const bad = validateSettings(set);
    if (bad) return fail(400, bad);
    Object.assign(doc, set);
    return doc;
  }],
  ['GET', '/sunshine/logs', AUTHED, function () {
    let lines = asList(this.w.docs['sunshine-logs']);
    if (lines.length > 2000) lines = lines.slice(lines.length - 2000);
    return rawAnswer(200, { 'content-type': 'text/plain; charset=utf-8', 'cache-control': 'no-store' }, lines.map((l) => asStr(l) + '\n').join(''));
  }],
  ['POST', '/sunshine/restart', AUTHED, function () {
    // Sunshine comes back: its API answers again.
    for (const k of Object.keys(this.w.errs)) if (k.includes(' /sunshine')) delete this.w.errs[k];
    this.emit('sunshine.state', { running: true });
    return {};
  }],
  ['POST', '/sunshine/end-stream', AUTHED, function () {
    if (this.doc('sunshine').streaming !== true) return fail(409, 'nothing is streaming');
    // Closing the app ends the stream; the prep-cmd undo ends the session.
    this.schedule('end-stream', 500);
    return {};
  }],
];

const STORAGE = [
  ['GET', '/storage', AUTHED, function () {
    return this.doc('storage');
  }],
  ['POST', '/storage/libraries', AUTHED, function (r) {
    const b = readJSON(r.body, { uuid: 'string' });
    if (b.error) return fail(400, b.error);
    const uuid = b.v.uuid ?? '';
    if (!/^[0-9A-Za-z][0-9A-Za-z-]{0,63}$/.test(uuid)) return fail(400, 'invalid filesystem uuid');
    let d = null;
    for (const x of asList(this.doc('storage').disks)) {
      const y = asObj(x);
      if (asStr(y.uuid) === uuid && asStr(y.fstype) !== '' && y.missing !== true) d = y;
    }
    if (!d) return fail(404, `no filesystem with uuid ${uuid} is attached`);
    if (d.is_system === true) return fail(400, `${asStr(d.path)} is part of the VaporOS system disk`);
    if (!libraryFS(asStr(d.fstype))) {
      return fail(400, `${asStr(d.fstype)} filesystems cannot hold a Steam library (use ext4, btrfs, xfs, f2fs or NTFS)`);
    }
    const mp = '/var/mnt/' + asStr(d.label);
    let library = mp;
    const hadGames = d.steam_library === true;
    const dir = asStr(d.library_dir);
    if (hadGames && dir !== '' && dir !== '.') library = joinPath(mp, dir);
    else if (!hadGames) {
      library = joinPath(mp, 'SteamLibrary'); // made on adoption, owned by vapor
      d.steam_library = true;
      d.library_dir = 'SteamLibrary';
    }
    const pending = this.doc('sunshine').streaming === true;
    d.adopted = true;
    d.mounted_at = mp;
    d.registered = !pending;
    if (d.free === undefined || d.free === null) d.free = asNum(d.size) * 0.6;
    delete d.registration_pending;
    if (pending) d.registration_pending = true;
    return { mountpoint: mp, library, registered: !pending, registration_pending: pending, hint: adoptHint(library, hadGames, !pending, pending) };
  }],
  ['DELETE', '/storage/libraries/{uuid}', AUTHED, function (r) {
    const uuid = r.params.uuid;
    if (!/^[0-9A-Za-z][0-9A-Za-z-]{0,63}$/.test(uuid)) return fail(400, 'invalid filesystem uuid');
    const st = this.doc('storage');
    const keep = [];
    let found = false;
    for (const x of asList(st.disks)) {
      const d = asObj(x);
      if (asStr(d.uuid) !== uuid || d.adopted !== true) {
        keep.push(x);
        continue;
      }
      found = true;
      if (d.missing === true) continue; // an adopted drive that is not attached is simply forgotten
      d.adopted = false;
      d.registered = false;
      delete d.mounted_at;
      delete d.free;
      delete d.registration_pending;
      keep.push(d);
    }
    if (!found) return fail(404, `no adopted library with uuid ${uuid}`);
    st.disks = keep;
    return {};
  }],
];

const POWER = [
  ['GET', '/power', AUTHED, function () {
    return this.powerState(this.now());
  }],
  ['PUT', '/power', AUTHED, function (r) {
    const b = readJSON(r.body, { idle_shutdown: 'bool', idle_minutes: 'int' });
    if (b.error) return fail(400, b.error);
    const m = b.v.idle_minutes;
    if (m !== undefined && (m < 1 || m > 1440)) return fail(400, 'idle_minutes must be between 1 and 1440');
    const p = this.doc('power');
    if (b.v.idle_shutdown !== undefined) p.idle_shutdown = b.v.idle_shutdown;
    if (m !== undefined) p.idle_minutes = m;
    return this.powerState(this.now());
  }],
  ['POST', '/power/keep-awake', AUTHED, function (r) {
    const b = readJSON(r.body, { minutes: 'int' });
    if (b.error) return fail(400, b.error);
    const m = b.v.minutes;
    if (m === undefined || m < 0 || m > MAX_KEEP_AWAKE) return fail(400, `minutes must be between 0 and ${MAX_KEEP_AWAKE}`);
    const p = this.doc('power');
    const now = this.now();
    delete p.keep_awake_until;
    if (m > 0) {
      p.keep_awake_until = rfc3339(now + m * 60000);
      this.w.idleSince = now;
    }
    this.powerCheck(now, false);
    return {};
  }],
];

const STATUS = [
  ['GET', '/status', AUTHED, function () {
    const sun = this.sunshineAnswer();
    const stream = sun.streaming === true ? sun.session : null;
    delete sun.session;
    const pw = this.powerState(this.now());
    delete pw.wol;
    const up = this.updateAnswer();
    const disp = this.doc('display');
    return { system: this.systemAnswer(), sunshine: sun, stream, display: disp, update: up, power: pw, restart: restartReasons(up, disp) };
  }],
];

const INSTALL = [
  ['GET', '/install/probe', SETUP, function (r) {
    const p = { ...this.doc('install-probe') };
    p.source = r.query.get('source') || '';
    delete p.channel;
    const ch = r.query.get('channel') || '';
    if (ch) p.channel = ch;
    return p;
  }],
  ['POST', '/install', SETUP, function (r) {
    const b = readJSON(r.body, {
      disk: 'string', mode: 'string', hostname: 'string', password: 'string',
      timezone: 'string', libraries: 'strings', source: 'string', channel: 'string',
    });
    if (b.error) return fail(400, b.error);
    if (this.w.installing) return fail(409, 'an installation is already running');
    const c = this.checkInstall(b.v);
    if (c.error) return fail(400, c.error);
    this.w.installing = true;
    this.setInstall('running', 'probe', 0, 'Starting the installation', '');
    this.schedule('install-step', 700, {
      i: 0, dev: asStr(c.disk.path), mode: c.mode, hostname: c.hostname, password: c.password, diskHostname: asStr(c.disk.hostname),
    });
    return jsonAnswer(202, { job: hex(this.random(8)) });
  }],
  ['GET', '/install/status', SETUP, function () {
    return this.doc('install-status');
  }],
  ['POST', '/install/reboot', SETUP, function () {
    if (this.w.installing) return fail(409, 'an installation is running');
    // The ISO restarts into the installed system, or into itself.
    this.restart(20000, this.w.installed);
    return {};
  }],
];

const route = (fake) => ([method, path, access, h]) => ({ method, path, access, h, fake });

// The routes vosd serves, in the order it registers them: the core, then
// the display and the machine always, the installer on the live ISO, the
// rest installed (internal/daemon/daemon.go).
const ROUTES_OS = [
  ...CORE.map(route(false)),
  ...[...SYSTEM, ...DISPLAY, ...UPDATE, ...SUNSHINE, ...STORAGE, ...POWER, ...STATUS].map(route(true)),
];
const ROUTES_INSTALLER = [...CORE.map(route(false)), ...[...SYSTEM, ...DISPLAY, ...INSTALL].map(route(true))];

// restartReasons is daemon.restartFor.
function restartReasons(up, disp) {
  const reasons = [];
  const next = asObj(up.next_boot);
  if (asStr(next.version) !== '') {
    reasons.push({ kind: compareVersions(asStr(next.version), asStr(up.booted)) > 0 ? 'update' : 'rollback', version: next.version });
  }
  if (disp.reboot_needed === true) reasons.push({ kind: 'display' });
  return { needed: reasons.length > 0, reasons };
}

function adoptHint(library, hadGames, registered, pending) {
  const games = hadGames ? ' Its installed games appear in Steam without downloading.' : '';
  if (registered) return `${library} is one of Steam's game libraries.${games}`;
  if (pending) {
    return (
      `VaporOS adds ${library} to Steam's game libraries the next time Steam is not running (at the latest after a restart).${games} ` +
      `To use it right away, open Steam > Settings > Storage > Add Drive and choose ${library}.`
    );
  }
  return `In Steam, open Settings > Storage > Add Drive and choose ${library}.${games}`;
}

// joinPath is path.Join for an absolute base.
function joinPath(base, rel) {
  const out = [];
  for (const seg of `${base}/${rel}`.split('/')) {
    if (!seg || seg === '.') continue;
    if (seg === '..') out.pop();
    else out.push(seg);
  }
  return '/' + out.join('/');
}

Engine.prototype.uuid = function () {
  const h = hex(this.random(16)).toUpperCase();
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`;
};

// ---------- the transport (globalThis.vosTransport) ----------

// transport is {fetch, EventSource} over an engine, for core/api.js and
// core/live.js: /api/v1 goes to the engine, anything else to the real
// fetch. Cookies live in the engine's client jar (the demo's one browser).
export function transport(engine, { base = 'http://vapor.local/' } = {}) {
  const jar = engine.s.client.cookies;
  const cookieHeader = () =>
    Object.entries(jar)
      .map(([k, v]) => `${k}=${v}`)
      .join('; ');
  const setCookies = (list) => {
    for (const c of list || []) {
      const [pair] = c.split(';');
      const i = pair.indexOf('=');
      const name = pair.slice(0, i);
      if (/;\s*Max-Age=(0|-)/i.test(c) || pair.slice(i + 1) === '') delete jar[name];
      else jar[name] = pair.slice(i + 1);
    }
  };
  const here = () => (typeof location !== 'undefined' ? location.href : base);
  const later = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

  async function fetchFn(input, init = {}) {
    const req = typeof Request !== 'undefined' && input instanceof Request ? input : null;
    const url = new URL(req ? req.url : String(input), here());
    if (!url.pathname.startsWith(PREFIX + '/')) return globalThis.fetch(input, init);
    const method = String(init.method || (req && req.method) || 'GET').toUpperCase();
    const headers = {};
    new Headers(init.headers || (req && req.headers) || {}).forEach((v, k) => (headers[k] = v));
    headers.cookie = cookieHeader();
    const body = init.body !== undefined && init.body !== null ? String(init.body) : req ? await req.text() : undefined;
    await later(engine.latency(method, url.pathname));
    const res = engine.handle({ method, path: url.pathname + url.search, headers, body });
    if (!res) throw new TypeError('Failed to fetch');
    setCookies(res.headers['set-cookie']);
    const h = new Headers();
    for (const [k, v] of Object.entries(res.headers)) if (k !== 'set-cookie') h.set(k, v);
    h.set('date', new Date(engine.now()).toUTCString());
    return new Response(res.body === '' ? null : res.body, { status: res.status, headers: h });
  }

  class FakeEventSource extends EventTarget {
    constructor(url, init = {}) {
      super();
      this.url = new URL(String(url), here()).href;
      this.withCredentials = !!init.withCredentials;
      this.readyState = 0;
      this.onopen = null;
      this.onmessage = null;
      this.onerror = null;
      this.stream = null;
      this.retry = 3000;
      this.timer = setTimeout(() => this.connect(), 0);
    }

    fire(type) {
      const e = new Event(type);
      this.dispatchEvent(e);
      const fn = this['on' + type];
      if (typeof fn === 'function') fn.call(this, e);
    }

    deliver(topic, data) {
      if (this.readyState !== 1) return;
      const e = new MessageEvent(topic, { data, origin: new URL(this.url).origin, lastEventId: '' });
      this.dispatchEvent(e);
      if (topic === 'message' && typeof this.onmessage === 'function') this.onmessage.call(this, e);
    }

    connect() {
      this.timer = 0;
      if (this.readyState === 2) return;
      const url = new URL(this.url);
      const out = engine.openStream({ path: url.pathname + url.search, headers: { cookie: cookieHeader() } });
      if (!out) {
        // The box is down: the browser keeps trying.
        this.readyState = 0;
        this.fire('error');
        this.timer = setTimeout(() => this.connect(), this.retry);
        return;
      }
      if (out.answer) {
        setCookies(out.answer.headers['set-cookie']);
        // Refused: the connection fails for good.
        this.readyState = 2;
        this.fire('error');
        return;
      }
      const st = out.stream;
      this.stream = st;
      this.readyState = 1;
      this.fire('open');
      for (const ev of st.replay) this.deliver(ev.topic, ev.data);
      st.onevent = (ev) => setTimeout(() => this.stream === st && this.deliver(ev.topic, ev.data), 0);
      st.onend = () => {
        if (this.stream !== st || this.readyState === 2) return;
        this.stream = null;
        this.readyState = 0;
        this.fire('error');
        this.timer = setTimeout(() => this.connect(), this.retry);
      };
    }

    close() {
      this.readyState = 2;
      clearTimeout(this.timer);
      if (this.stream) this.stream.close();
      this.stream = null;
    }
  }
  FakeEventSource.CONNECTING = 0;
  FakeEventSource.OPEN = 1;
  FakeEventSource.CLOSED = 2;

  return { fetch: fetchFn, EventSource: FakeEventSource };
}
