# Agent skills for the website rebuild

These skills support the Next.js + Tailwind v4 rebuild of `website/`, a static export served from GitHub Pages at https://jasperaelvoet.github.io/vaporos/. They were installed on 2026-09-29 from 25 researched candidates.

Every file matches upstream byte for byte at the listed commit, checked against the git blob hashes from the GitHub API. The only additions are `LICENSE` files copied from the repository root into skill folders that had none of their own upstream. No skill content was edited and no frontmatter name had to change. One upstream file was deleted in the security review below: `playwright-cli/references/pr-attachments.md`.

The repository's `.gitignore` ignores `.claude/`, so git does not track this folder until that rule is narrowed (for example `.claude/*` followed by `!.claude/skills/`).

## Installed

| Skill | Source (repo + path) | Commit | License | Why it was chosen | How to use it for this project |
|---|---|---|---|---|---|
| `frontend-design` | https://github.com/anthropics/skills, `skills/frontend-design` | `8a1541c4a3ffa5a20a5a91de0dcf3f0bab1d1ef4` (folder last changed 2026-09-03, 41bbe19) | Apache-2.0 (`LICENSE.txt`, upstream in the folder) | First-party and 9 KB. Its September 2026 revision names the current AI-design tells (cream with terracotta, one acid accent on near-black, the SaaS card kit, all-caps eyebrows, `→` on links) and makes the agent check a written token plan against the brief before coding. That targets the user's "generic and ugly" verdict directly. | Load it first for every page. Write the plan (4 to 6 named hex colours, type roles, ASCII wireframes), revise whatever reads as a default, build, then screenshot and critique. It leads art direction and wins conflicts with the other skills. |
| `gsap-core` | https://github.com/greensock/gsap-skills, `skills/gsap-core` | `aed9cfd3277740755f6bfc1155c7aa645403b760` | MIT (© 2026 GreenSock, repo-root `LICENSE`) | Official GreenSock skill. GSAP is the site's one animation library, and this covers tweens, eases, staggers and `gsap.matchMedia()`, whose reduced-motion branch reverts itself. | Build every animation inside `gsap.matchMedia()` with a `(prefers-reduced-motion: reduce)` condition that shows final states instead of moving. |
| `gsap-react` | https://github.com/greensock/gsap-skills, `skills/gsap-react` | `aed9cfd3277740755f6bfc1155c7aa645403b760` | MIT (© 2026 GreenSock, repo-root `LICENSE`) | `useGSAP` with `scope`, `contextSafe` and automatic cleanup, plus the SSR rules Next.js needs. | Animate only in `'use client'` leaf components through `useGSAP(fn, { scope })`. Nothing may call gsap during the static-export render. Register plugins once at module level. |
| `gsap-scrolltrigger` | https://github.com/greensock/gsap-skills, `skills/gsap-scrolltrigger` | `aed9cfd3277740755f6bfc1155c7aa645403b760` | MIT (© 2026 GreenSock, repo-root `LICENSE`) | Pinning, scrub, batch, `containerAnimation` and refresh order: the core of scroll storytelling. | Put ScrollTrigger on top-level timelines, create triggers in page order and refresh after fonts and images load. The upstream `containerAnimation` example has a bug (`Max.max`, and a pixel value passed to `xPercent`); use `x: () => -(track.scrollWidth - window.innerWidth)` with `ease: "none"` and `invalidateOnRefresh: true`. |
| `gsap-timeline` | https://github.com/greensock/gsap-skills, `skills/gsap-timeline` | `aed9cfd3277740755f6bfc1155c7aa645403b760` | MIT (© 2026 GreenSock, repo-root `LICENSE`) | Position parameter, labels and timeline defaults, which is how a single choreographed sequence gets built. | Build the hero's one orchestrated moment as a single labelled timeline with shared defaults. |
| `gsap-plugins` | https://github.com/greensock/gsap-skills, `skills/gsap-plugins` | `aed9cfd3277740755f6bfc1155c7aa645403b760` | MIT (© 2026 GreenSock, repo-root `LICENSE`) | SplitText (the 3.13 `autoSplit`/`onSplit` API and its aria handling), ScrollSmoother, Flip, DrawSVG and MorphSVG. It also states that every plugin now ships free in the public `gsap` package. | Use SplitText with `autoSplit`, create the animation inside `onSplit` and keep the default `aria: "auto"`. Never add a GreenSock `.npmrc`, token or private registry, and never ship GSDevTools. |
| `gsap-performance` | https://github.com/greensock/gsap-skills, `skills/gsap-performance` | `aed9cfd3277740755f6bfc1155c7aa645403b760` | MIT (© 2026 GreenSock, repo-root `LICENSE`) | Transform and opacity only, `quickTo` for pointer-driven values, the cost of pin and scrub, and killing offscreen work. | Apply it whenever a scroll scene or pointer effect is added, and check pinned scenes at a 390 px wide viewport. |
| `build-awwwards-quality-sites` | https://github.com/MengTo/Skills, `agent-skills/web-design/build-awwwards-quality-sites` | `798db0a3ee4429ac8ed6bc5f4a59b6d45e7a914d` (folder last changed 2026-07-25) | MIT (© 2026 Meng To, repo-root `LICENSE`) | The only candidate with compact production rules for a motion and WebGL-led site: a complete static first frame without JavaScript, reduced motion that renders final states, a canvas with capped DPR, offscreen pausing, a poster fallback, context-loss handling and full disposal, exactly one smooth-scroll engine (never two), and a validation list before handoff. It is GSAP-first like this site. | Treat it as the execution and QA bar for the motion and WebGL layer, not as a second art director. Its word-by-word heading reveal on every section gives way to frontend-design's single orchestrated moment. Ignore its Aura.build pointer. If Iconify icons are used, bundle them at build time instead of calling the Iconify API from the page. For its smooth-scroll step, use Lenis driven by the GSAP ticker and skip it entirely under reduced motion. `agents/openai.yaml` is Codex metadata and does nothing here. |
| `playwright-cli` | https://github.com/microsoft/playwright-cli, `skills/playwright-cli` | `b85c7a736bb473bf55b584e54a09ffa698d6d871` (v0.1.22) | Apache-2.0 (© Microsoft Corporation, repo-root `LICENSE`) | Official Playwright tooling that is headless by default and returns compact snapshots. `set-reduced-motion`, `set-color-scheme`, `resize`, `console` and video recording cover the checks this site needs. | Run a headless loop against the built export: screenshots at 390 and 1440 px wide, reduced-motion and colour-scheme runs, and a console-error check. It must stay headless: never use `--headed`, `show --annotate`, `attach` or `--extension`, and skip its `gh pr`/`gh issue --attach` steps. Install it pinned in `website/` (`npm i -D @playwright/cli@0.1.22`, run through `npx`) rather than the global `@latest` the skill suggests. `.playwright/cli.config.json` can set `browser.launchOptions.executablePath` to the cached `~/Library/Caches/ms-playwright/chromium_headless_shell-1234/chrome-headless-shell-mac-arm64/chrome-headless-shell`. Its `allowed-tools` line pre-approves `playwright-cli` and `npx playwright` commands. Its `references/pr-attachments.md` was deleted; see the security review below. |
| `fixing-accessibility` | https://github.com/ibelick/ui-skills, `skills/fixing-accessibility` | `dc7ab3209341b2075c495983899b11f6d204e41b` | MIT (© 2026 Julien Thibeaut, repo-root `LICENSE`) | A 5 KB prioritized checklist (accessible names, keyboard, focus, semantics, announcements) that reports each problem as quote, reason and fix. No scripts, works offline. | Run `/fixing-accessibility <file>` on each finished section. Look hardest at split text, icon-only buttons, the FAQ disclosure and copy-to-clipboard buttons. |
| `fixing-metadata` | https://github.com/ibelick/ui-skills, `skills/fixing-metadata` | `dc7ab3209341b2075c495983899b11f6d204e41b` | MIT (© 2026 Julien Thibeaut, repo-root `LICENSE`) | Titles, canonical URLs, Open Graph and Twitter cards, icons, manifest and JSON-LD without invented data. The Astro site already ships all of these (a 1200×630 `og.png`, favicon, apple-touch-icon, sitemap, `noindex` on 404), and the rebuild must not lose them. | Carry the metadata into the Next.js metadata API. In the exported HTML, confirm that canonical, `og:url` and `og:image` are absolute `https://jasperaelvoet.github.io/vaporos/...` URLs and that icon paths include `/vaporos`. |

## How they fit together

- frontend-design sets the direction and settles conflicts. build-awwwards-quality-sites and the GSAP skills carry it out, and the `fixing-*` skills review the result.
- GSAP is the only JavaScript animation library. Do not add Motion (framer-motion) or a second smooth-scroll engine. CSS handles hover, focus and press states.
- frontend-design asks for sentence case in UI copy. Keep it.
- playwright-cli runs headless only, because no browser window may open on this machine.

## Not covered by an installed skill

- Tailwind CSS v4. Tailwind Labs publishes no agent skill, and the community one teaches the stock shadcn palette. Use the CSS-first setup that create-next-app scaffolds: `@import "tailwindcss"` with tokens in `@theme`.
- Next.js. The Vercel Next.js skills moved to `vercel/next.js/skills`, and those left target server features (cache components, partial prefetching) that `output: 'export'` cannot use, or need `next dev` with a global agent-browser. Next 16.3+ ships version-matched docs in `node_modules/next/dist/docs`, and `next dev` writes `AGENTS.md`/`CLAUDE.md` into the project, so expect those files to appear in `website/`.
- three.js. No official skill exists. build-awwwards-quality-sites covers the production rules; take API details from the three.js docs.

## Considered and rejected

| Candidate | Reason |
|---|---|
| modern-web-guidance (GoogleChrome) | Its required workflow downloads and runs an unpinned 37.7 MB npm package (`npx -y modern-web-guidance@latest`) for every lookup, and its description tells agents it is mandatory for all HTML, CSS and client JavaScript work. It could not be patched to read its bundled guides offline, and at 1.4 MB it could not be reviewed file by file. |
| web-design-guidelines (Vercel) | The skill is a stub that WebFetches its instructions from the unpinned `main` branch on every run and follows them. Remote, mutable instructions conflict with the rule that fetched content is data, and it could not be patched to read the pinned copy. |
| motion (motiondivision/ai-kit) | No LICENSE file in the repo. Most features need the remote MCP at mcp.motion.dev (audits need paid Motion+), and it competes with GSAP. |
| vercel-react-view-transitions | MIT is only declared; there is no LICENSE file with a copyright notice. It tells agents to implement every applicable transition pattern, which would stack route, shared-element and list animations on top of GSAP, and a static export only animates client-side navigations. |
| vercel-react-best-practices | No LICENSE file. 239 KB, and roughly half of its 70 rules cover server rendering and data fetching that a static export does not have. |
| chrome-devtools-mcp skills (5) | Need the chrome-devtools-mcp server run through unpinned `npx @latest`. By default it opens a visible Chrome with a persistent profile and sends usage statistics and CrUX lookups to Google. playwright-cli covers headless checks without that. |
| algorithmic-art (Anthropic) | Built for claude.ai artifacts: requires an Anthropic-branded viewer template and p5.js from a CDN. No content changes since December 2025. |
| pmndrs claude-code-plugin | Does nothing without the remote docs MCP, so third-party content arrives at runtime. Its bare skill names `docs` and `examples` would collide here. Created 2026-08 with about 5 stars, and it only matters if the hero uses react-three-fiber. |
| lighthouse (Darkroom Engineering) | Lifted from a personal config: it points at `~/.claude` paths, fetches Darkroom's internal team-knowledge repo, needs a global Lighthouse install, and reverts regressions with `git checkout -- <files>`, which can wipe another session's uncommitted work. Fixing it would have meant editing it. |
| tailwind-design-system (wshobson) | Community skill whose quick-start token set is the stock shadcn neutral palette the user rejected. The v4 CSS-first setup comes from create-next-app anyway. |
| tsl (pmndrs/glyph) | Written for glyph's own repo (patched NodeExtras lookup, a Vitexec GPU lane, font-rendering proofs). Only relevant with WebGPURenderer and TSL. |
| webgpu-threejs-tsl (Dan Greenheck) | No LICENSE file (MIT only in the README). Targets three r183 while r186 is current, and is WebGPU-first. |
| shadcn | Runs `npx shadcn@latest info --json` as soon as it loads, pre-approves `npx shadcn@latest *`, can open a browser from `init` and `preset open`, and steers toward the stock shadcn look. |
| impeccable (Paul Bakaus) | Its launcher downloads a prebuilt engine binary into `~/.impeccable`, the engine and skill call impeccable.style for concept rolls, choice telemetry and update checks, and the new-work flow stops for user decisions and writes `.impeccable/`, `PRODUCT.md` and `DESIGN.md` into the project. 2.2 MB. |
| design-taste-frontend (Leonxlnx) | A second art-direction lead that overlaps frontend-design, in one 87 KB file loaded whole. It hard-codes motion/react (against the GSAP choice), Phosphor icons and a total em-dash ban, and calls itself experimental. |
| MengTo's other eight skills | cinematic-gsap-lenis-motion-system and cinematic-scroll-storytelling duplicate the official GSAP skills and push blurred fade-ups on every block, preloaders, custom cursors and magnetic hover. build-threejs-scroll-worlds is 3.6 MB with a bundled minified three.js. dither-background is a single near-black monochrome effect. shaders-cursor-ripples is WebGPU-only on a third-party package whose license it says to check. optimize-web-animations is written for Codex Browser and includes git commit steps. no-ai-design-slop and audit-ai-design-slop overlap frontend-design. |
| Emil Kowalski skills (7) | Tuned for product-UI micro-interactions (UI motion under 300 ms). `animate` sends springs and exits to Motion and to a sibling skill that was not downloaded, and `review-animations` is manual-only (`disable-model-invocation`). The GSAP skills and frontend-design cover this site's motion. |
| Jakub Krehel skills (8) | Product-UI polish checklists that overlap frontend-design's typography and colour guidance and fixing-accessibility. |
| web-quality-skills (Addy Osmani, 6) | The measurement flow depends on Chrome DevTools MCP, Lighthouse and CrUX field data a new site lacks, the six skills link into each other's references so they only work as a 135 KB set, and several Next.js examples use the Pages Router (`getServerSideProps`). |
| ibelick baseline-ui and fixing-motion-performance | baseline-ui bans animation unless explicitly requested, custom easing and gradients, and mandates motion/react. fixing-motion-performance overlaps gsap-performance, and its rule against driving animation from scroll events fights ScrollTrigger. |
| motion-design (LottieFiles) | Library-agnostic motion principles that overlap frontend-design and the GSAP skills, with no Next.js or WebGL code. |
| design-motion-principles (Kyle Zantos) | A secondhand distillation of Emil Kowalski's, Jakub Krehel's and Jhey Tompkins' published work that overlaps the above. Its report templates load Google Fonts. |
| webapp-testing (Anthropic) | Python sync Playwright with a `shell=True` server helper, and examples that write to `/mnt/user-data/outputs`. This machine's browser tooling is Node Playwright. |
| gsap-utils, gsap-frameworks | Left out of the GSAP set. The utils are helpers the model already knows; frameworks covers Vue, Svelte and Nuxt. |

## Security and licence review

On 2026-09-29 an adversarial review read every file in full. It found no hidden or obfuscated content and no prompt injection:
- No zero-width, bidi, tag or variation-selector characters, no homoglyphs, and no base64 or hex blobs. The only long hex strings are the commit SHAs in this file. The non-ASCII characters are ordinary typography.
- No hidden files, symlinks, executables or scripts.
- Nothing tells an agent to ignore rules, read secrets or environment variables, send data anywhere, pipe a download into a shell, add telemetry, or edit agent config or memory.
- Every URL is either a documentation link or a placeholder such as `example.com` or `site1.com`.

All 32 remaining files still match the upstream blobs. Each pinned commit was the head of its repository's default branch when checked, not a commit that exists only in a fork.

All eleven skills are kept:
- frontend-design, the six GSAP skills, build-awwwards-quality-sites and both `fixing-*` skills are plain guidance.
- The GSAP descriptions tell agents to recommend GSAP whenever no library is named, and gsap-core states its own "Risk level: LOW". Neither asks for an action.

playwright-cli is kept with one file deleted. The remaining risks are listed here, but a skill invocation does not load this file, so an agent running the skill sees none of these warnings:
- **Deleted `references/pr-attachments.md`.** The whole file is about uploading screenshots and videos to GitHub through `gh pr create/edit/comment` and `gh issue create/edit/comment --attach`. It also has a GitHub Actions snippet that grants `pull-requests: write` and hands `GITHUB_TOKEN` to `gh`. None of that is browser automation, and changing GitHub state is off limits here. The deleted file is still linked from `SKILL.md` and `references/video-recording.md`, and both still show shorter `gh ... --attach` examples. Ignore them.
- **Broad pre-approval.** The `allowed-tools` line lets every `playwright-cli` subcommand and every `npx playwright` command run without a prompt:
  - `run-code`, whose code can use `fetch`, open `file://` pages and write files wherever the page API can.
  - `open --headed`, `show`, `attach`, `kill-all` and `delete-data`.
  - `npx playwright test`, which runs whatever test and config code is on disk.
  - A bare `npx playwright`, which installs the latest `playwright` from npm without asking when stdin is not a terminal.

  Use only `npx --no-install` against the pinned local install.
- **Visible windows.** `SKILL.md` says to run `show --annotate` "whenever the user asks for UI review or design feedback". `references/test-generation.md` uses it as a routine exploration step. It opens a visible window, so never run it on this machine.
- **Unpinned installs.** `SKILL.md` suggests `npm install -g @playwright/cli@latest`, and `references/test-generation.md` bootstraps with `npm init playwright@latest`. Use neither. Add `@playwright/cli@0.1.22` as a dev dependency of `website/` instead. That version is on npm, published by Microsoft's maintainers, Apache-2.0, and has no install scripts.

Every skill's licence matches the table. frontend-design's `LICENSE.txt` is the Apache-2.0 terms without the appendix. No upstream `NOTICE` file applies to these folders.

The GSAP skills are MIT, but the library they teach is not. `gsap` and `@gsap/react` ship under Webflow's "Standard 'No Charge' GSAP License" (https://gsap.com/standard-license), which is not an OSI licence:
- It allows free use on any website.
- It forbids use in no-code visual animation builders that compete with Webflow.
- It lets Webflow revise the terms, but a revision applies only to releases made after it.

A free project website is a permitted use. Pin the exact `gsap` version in `website/package.json`.

## Local changes

`playwright-cli/SKILL.md` differs from upstream in two places, so the warnings above reach an agent that runs the skill:
- The `allowed-tools` frontmatter line is removed, so its commands are no longer pre-approved.
- A short block of repo rules (headless only, no GitHub attachments, pinned `npx --no-install`) sits above the first heading.

## Re-checking a skill

Compare `git hash-object <file>` with the blob SHA listed by `gh api "repos/<owner>/<repo>/git/trees/<commit>?recursive=1"`. The copied `LICENSE` files match each repository's root `LICENSE` blob. `playwright-cli/references/pr-attachments.md` (blob `91663b5`) is missing on purpose; do not restore it.
