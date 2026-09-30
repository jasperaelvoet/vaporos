# VaporOS website

The site at https://jasperaelvoet.github.io/vaporos/: landing page, download,
install guide and FAQ. Next.js 16 (App Router, TypeScript, Tailwind CSS 4),
exported as static files for GitHub Pages.

## Commands

```sh
npm ci               # Node 24 (.nvmrc)
npm run dev          # http://localhost:3000/vaporos/
npm run build        # static site in out/ (prebuild copies the release key to public/)
npm run lint
npm run typecheck
npm test             # unit tests (tests/unit), straight from the TypeScript source
npm run check        # lint + typecheck + test: what pages.yml runs on every push
npm run verify -- --out=/tmp/vaporos-verify   # builds, static checks, browser checks, screenshots
```

`npm run build` looks up the latest release on GitHub. Environment:

| Variable | Effect |
| --- | --- |
| `GITHUB_TOKEN` | Authenticated release lookup (the Pages workflow sets it). |
| `VAPOROS_RELEASE_FIXTURE=path.json` | Use a saved API answer instead (a release or a list); `/dev/null` gives the no-release state. `tests/fixtures/release-latest.json` is GitHub's real answer for `v20260929.172712`. |
| `VAPOROS_RELEASE_REQUIRED=1` | Fail the build when the release lookup fails, instead of building the "couldn't load" card (pages.yml sets it). |
| `VAPOROS_SITE_COMMIT=sha` | Adds `<meta name="vaporos-commit">` (pages.yml sets it). |
| `VAPOROS_KEY_FILE=path` | Where `release.pub` is, when the site is built outside the repo. Inside the repo, `../keys/release.pub` is used. |

## Layout

```
src/content/   every word and fact on the site, typed (index.ts re-exports all)
src/lib/       base path, release lookup (build + browser), release key, metadata, icons
src/components/ plain placeholder components (download card, code block, rich text, …)
src/app/       routes: /, /download/, /install/, /faq/, not-found (404.html), sitemap.xml
scripts/       copy-key.mjs (prebuild/predev); serve.mjs, shoot.mjs, og.mjs, chrome.mjs (headless tools)
tests/         unit/ (npm test), e2e/ (npm run verify), fixtures/
public/        favicon.svg, apple-touch-icon.png, icon-192/512 and maskable PNGs (generated from design/logo.svg), .nojekyll
```

See AGENTS.md for the rules (facts, base path, static export, release state).
