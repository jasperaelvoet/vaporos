// The input hash is computed twice, here and in internal/web/css_test.go.
// Both check the same vector: testdata/css-hash is a miniature repo whose
// expected hash was worked out once with shasum, independently of either.
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

import { header, HEADER_RE, inputFiles, inputHash } from '../lib/css-hash.mjs';

const vector = join(dirname(fileURLToPath(import.meta.url)), 'testdata', 'css-hash');

test('the shared vector hashes to its expected value', () => {
  const want = readFileSync(join(vector, 'expected.txt'), 'utf8').trim();
  assert.equal(inputHash(vector), want);
});

test('the hashed files are exactly the definition, byte-sorted', () => {
  assert.deepEqual(inputFiles(vector), [
    'internal/web/static/js/fmt.js',
    'internal/web/static/js/pages/home.js',
    'internal/web/styles/app.css',
    'internal/web/styles/sub/b.css',
    'internal/web/templates/layout.html',
    'internal/web/templates/pages/home.html',
    'internal/web/templates/pages/notes.txt',
    'internal/web/templates/partials/logo.html',
    'tools/web/package-lock.json',
  ]);
});

test('the header round-trips through the pattern the Go test uses', () => {
  const line = header('0123456789abcdef', '4.3.3');
  assert.equal(line, '/*! vaporos-css inputs=0123456789abcdef tailwind=4.3.3 */');
  assert.deepEqual(line.match(HEADER_RE).slice(1), ['0123456789abcdef', '4.3.3']);
  assert.equal(HEADER_RE.test('/*! vaporos-css inputs=0123 tailwind=4.3.3 */'), false);
});
