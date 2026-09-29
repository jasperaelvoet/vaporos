# VaporOS website

The site at https://jasperaelvoet.github.io/vaporos/: landing page, download,
install guide and FAQ. Next.js 16 (App Router, TypeScript, Tailwind CSS 4),
exported as static files for GitHub Pages.

## Commands

```sh
npm ci
npm run dev          # http://localhost:3000/vaporos/
npm run build        # static site in out/ (prebuild copies the release key to public/)
npm run lint
npm run typecheck
```

`npm run build` looks up the latest release on GitHub. Environment:

| Variable | Effect |
| --- | --- |
| `GITHUB_TOKEN` | Authenticated release lookup (the Pages workflow sets it). |
| `VAPOROS_RELEASE_FIXTURE=path.json` | Use a saved API answer instead (a release or a list); `/dev/null` gives the no-release state. |
| `VAPOROS_KEY_FILE=path` | Where `release.pub` is, when the site is built outside the repo. Inside the repo, `../keys/release.pub` is used. |

## Layout

```
src/content/   every word and fact on the site, typed (index.ts re-exports all)
src/lib/       base path, release lookup (build + browser), release key, metadata, icons
src/components/ plain placeholder components (download card, code block, rich text, …)
src/app/       routes: /, /download/, /install/, /faq/, not-found (404.html), sitemap.xml
scripts/       copy-key.mjs (prebuild/predev)
public/        favicon.svg, icon.svg, apple-touch-icon.png, og.png, .nojekyll
```

See AGENTS.md for the rules (facts, base path, static export, release state).
