// Empties Next's fetch cache before every build (package.json prebuild).
//
// `next build` keeps the answers of build-time fetch() calls in
// .next/cache/fetch-cache and hands them to later builds. The release lookup
// (src/lib/release.ts) must ask GitHub on every build: an anonymous answer
// kept there would show an old release in the nav and the download card
// (a request with GITHUB_TOKEN, as pages.yml sends, is never kept). Opting
// the fetch out with cache: 'no-store' would make every page dynamic, which a
// static export refuses, so the cache goes instead. Within one build Next
// still shares the answer between its workers, so every page shows the same
// release.
import { rmSync } from 'node:fs';

rmSync(new URL('../.next/cache/fetch-cache', import.meta.url), { recursive: true, force: true });
