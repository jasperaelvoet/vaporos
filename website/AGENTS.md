<!-- BEGIN:nextjs-agent-rules -->

# This is NOT the Next.js you know

This version has breaking changes — APIs, conventions, and file structure may all differ from your training data. Read the relevant guide in `node_modules/next/dist/docs/` (resolved from this file's directory; in monorepos the `next` package may not be visible from the repo root) before writing any code. Heed deprecation notices.

This block is written and re-added by `next dev` — verify at `node_modules/next/dist/server/lib/generate-agent-files.js`. Removing it from a diff only re-creates the uncommitted change; committing it with your work keeps the tree clean.

<!-- END:nextjs-agent-rules -->

# VaporOS website: rules for this codebase

- **Facts live in `src/content/`** and were fact-checked against the VaporOS code. Restyle and reorder freely; rephrase only without changing a claim; never invent product facts, numbers or features. Each module offers `short` variants where a tighter line helps: use those before writing new copy. Commands (`DD_LINUX`, `DD_MAC`, `CHECKSUM_COMMAND`, `signatureCommand()`) are exact: render them verbatim. Entries marked `STALE?` (the idle-shutdown copy) predate a code change: keep their wording as is until the owner rewrites them.
- **Rich text** in content uses a tiny markup (`` `code` ``, `**strong**`, `[label](href)`, `++Key++`): render it with `<Rich>` (`src/components/rich.tsx`), flatten it with `plain()` (`src/lib/rich-text.ts`).
- **Base path `/vaporos`.** `next/link` and metadata add it; everything else the browser fetches must go through `withBase()` (`src/lib/base-path.ts`): `<img>`, `<video>`, CSS `url()`, `fetch()`, 3D models, textures, fonts, `/release.pub`. `SmartLink` picks the right one for content hrefs.
- **Static export** (`output: 'export'`): no server features (no route handlers reading requests, cookies, rewrites, redirects, server actions, image optimization). Browser-only code goes in client components, inside `useEffect` or behind `typeof window`.
- **Release state**: server components call `getLatestRelease()` (`src/lib/release.ts`, build time), which answers `ready` (a release with an ISO), `none` (GitHub has no release) or `unknown` (the lookup failed); client components pass that as `initial` to `useLatestRelease()` (`src/lib/use-latest-release.ts`), which only ever upgrades it. With no release, never show a download link (`downloadCard.none`); when the lookup failed, never claim there is no release (`downloadCard.unknown` links to GitHub). `VAPOROS_RELEASE_FIXTURE=tests/fixtures/release-latest.json npm run build` bakes in the real `v20260929.172712` answer; `/dev/null` gives `none`. `VAPOROS_RELEASE_REQUIRED=1` (set in `pages.yml`) fails the build on `unknown`.
- **Release key**: `RELEASE_KEY` (`src/lib/key.ts`) is server-only; build the verify data with `verifySteps(RELEASE_KEY)` / `releaseKeyCard(RELEASE_KEY)` in a server component and pass it down.
- **Metadata**: every page exports `metadata = pageMetadata(pageMeta.<page>)` (`src/lib/metadata.ts`); Next merges metadata shallowly, so don't hand-write a partial `openGraph`.
- **Fonts**: prefer self-hosted (`next/font/local` or `@fontsource/*`); `next/font/google` downloads at build time and fails offline.
- **Check your work**: `npm run check` (lint, typecheck, unit tests), then `npm run verify -- --out=<dir outside the repo>`: it builds the site in each release state (`none`, the fixture's `ready`, `unknown`; `--live` adds the real API), runs `tests/e2e/static-checks.mjs`, the download card's browser checks (`tests/e2e/check-release-hook.mjs`) and `scripts/shoot.mjs --strict` at 1440, 768 and 390 px plus reduced motion. By hand: `node scripts/serve.mjs out 4342` and `node scripts/shoot.mjs http://127.0.0.1:4342/vaporos/ <dir>` (WebGL works headless). Keep console errors and horizontal overflow at zero.
- **Browsers are headless only.** The tools find Chrome through `CHROME_PATH` first (CI sets it), then Playwright's cached headless shell (`scripts/chrome.mjs`). Never `--headed`, `show` or `attach`. Screenshots and reports go to a directory outside the repo.
