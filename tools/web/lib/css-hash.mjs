// The input hash in static/app.css's first line. internal/web/css_test.go
// computes the same value in Go (TestAppCSSFresh), so a stale stylesheet is
// caught by `go test` without Node. Both sides check one shared vector
// (test/testdata/css-hash).
//
// Definition (MASTER-PLAN §3.2 F2):
//   - the files: internal/web/styles/**/*.css,
//     internal/web/templates/layout.html, templates/pages/**,
//     templates/partials/**, internal/web/static/js/**/*.js and
//     tools/web/package-lock.json; never anything under a legacy/ directory,
//     and never a file whose name starts with "." (.DS_Store and friends are
//     not in git, so counting them would make the Mac and CI disagree);
//   - their repo-relative paths with "/", sorted byte-wise;
//   - for each: path + "\0" + hex(sha256(bytes)) + "\n";
//   - sha256 of the concatenation; the first 16 hex digits.

import { createHash } from 'node:crypto';
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';

export const HASH_LENGTH = 16;

// Each root is a repo-relative directory or file with the rule that selects
// files under it. Keep this list in the same order and meaning as
// cssInputRoots in internal/web/css_test.go.
export const INPUT_ROOTS = [
  { path: 'internal/web/styles', match: (name) => name.endsWith('.css') },
  { path: 'internal/web/templates/layout.html' },
  { path: 'internal/web/templates/pages', match: () => true },
  { path: 'internal/web/templates/partials', match: () => true },
  { path: 'internal/web/static/js', match: (name) => name.endsWith('.js') },
  { path: 'tools/web/package-lock.json' },
];

function isFile(p) {
  try {
    return statSync(p).isFile();
  } catch {
    return false;
  }
}

function isDir(p) {
  try {
    return statSync(p).isDirectory();
  } catch {
    return false;
  }
}

// walk returns the repo-relative paths of the files under dir that match.
function walk(root, dir, match, out) {
  for (const ent of readdirSync(join(root, dir), { withFileTypes: true })) {
    if (ent.name.startsWith('.')) continue;
    const rel = `${dir}/${ent.name}`;
    if (ent.isDirectory()) {
      if (ent.name === 'legacy' || ent.name === 'node_modules') continue;
      walk(root, rel, match, out);
    } else if (ent.isFile() && match(ent.name)) {
      out.push(rel);
    }
  }
}

// inputFiles lists the hashed files of the repo at root, sorted byte-wise.
export function inputFiles(root) {
  const out = [];
  for (const r of INPUT_ROOTS) {
    const abs = join(root, r.path);
    if (!r.match) {
      if (isFile(abs)) out.push(r.path);
    } else if (isDir(abs)) {
      walk(root, r.path, r.match, out);
    }
  }
  return out.sort((a, b) => Buffer.compare(Buffer.from(a), Buffer.from(b)));
}

// inputHash returns the 16-hex input hash of the repo at root.
export function inputHash(root) {
  const all = createHash('sha256');
  for (const rel of inputFiles(root)) {
    const sum = createHash('sha256').update(readFileSync(join(root, rel))).digest('hex');
    all.update(`${rel}\0${sum}\n`);
  }
  return all.digest('hex').slice(0, HASH_LENGTH);
}

// header is the first line of static/app.css.
export function header(hash, tailwind) {
  return `/*! vaporos-css inputs=${hash} tailwind=${tailwind} */`;
}

export const HEADER_RE = /^\/\*! vaporos-css inputs=([0-9a-f]{16}) tailwind=(\d+\.\d+\.\d+) \*\/$/;
