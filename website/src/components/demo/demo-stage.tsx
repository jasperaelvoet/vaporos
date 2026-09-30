'use client';
// The live demo on a page (spec-website §8.6): the control center in an
// iframe, the buttons that make things happen on the pretend PC, and
// Moonlight on a Steam Deck, where the pairing PIN shows up.
//
//   <DemoStage variant="home" hash={…} />   the home page's #demo: a phone
//        frame beside the panel, loaded once it nears the viewport and the
//        browser is idle; phones and touch screens get a card instead (the
//        beat hides this stage for them, so it never loads there)
//   <DemoStage variant="page" hash={…} />   /demo/: loaded right away, with
//        the page's title; on phones, touch screens and short windows the
//        frame fills the screen under a bar and the panel is a bottom sheet
//
// The layout is CSS (demo-layouts.css) so the first paint is already the right one;
// this component only sets the frame's scale (--s) and runs the channel.
// The frame is our own page on the site's origin, and trusted as such: it
// needs allow-same-origin (sessionStorage, and the origin checks on both
// ends of the protocol), and with allow-scripts that lets it reach this page,
// so the sandbox is no wall. It only stops accidents: without top navigation,
// store links open in a new tab and a stray link can't take the site's page
// away. Chromium warns about the pair on every page the frame loads ("can
// escape its sandboxing"); that is expected.
import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState, useSyncExternalStore } from 'react';
import { Icon } from '@/components/ui/icon';
import { DEMO_OPEN, DEMO_UI, demoPage, demoStage as copy } from '@/content/demo';
import { withBase } from '@/lib/base-path';
import { MoonlightMock, Notes, ScenarioPanel } from './scenario-panel';
import { useDemoChannel } from './use-demo';
import './demo.css';
import './demo-layouts.css';

/** The control center's phone viewport height, in CSS pixels (390 wide: demo.css), and the bezel around it. */
const VIEW_H = 844;
const BEZEL = 12;
/** The smallest scale the phone frame is drawn at (below it, /demo/ fills the screen instead: demo.css). */
const MIN_SCALE = 0.7;

const noSubscribe = () => () => {};
const isClient = () => true;
const isServer = () => false;

/** The page `?open=` asks for, if the demo has it. */
function openFromQuery(): string {
  try {
    const open = new URLSearchParams(location.search).get('open')?.replace(/^\/+|\/+$/g, '') ?? '';
    return (DEMO_OPEN as readonly string[]).includes(open) ? open : '';
  } catch {
    return '';
  }
}

export function DemoStage({ variant, hash }: { variant: 'home' | 'page'; hash: string }) {
  const frame = useRef<HTMLIFrameElement>(null);
  const stage = useRef<HTMLDivElement>(null);
  const sheet = useRef<HTMLDialogElement>(null);
  const opener = useRef<HTMLButtonElement>(null);
  const [near, setNear] = useState(false);
  const mounted = useSyncExternalStore(noSubscribe, isClient, isServer);
  const ch = useDemoChannel(frame);
  const id = useId();
  const alone = withBase(`${DEMO_UI}?v=${hash}`);

  // The frame's scale: as big as the window lets the whole phone be.
  useLayoutEffect(() => {
    const el = stage.current;
    if (!el) return;
    const fit = () => {
      const nav = parseFloat(getComputedStyle(document.documentElement).getPropertyValue('--nav-h')) * 16 || 72;
      // Below the nav: /demo/ keeps 1.5rem above the phone and a little under
      // it; the home page a bit more, as the beat scrolls past.
      const room = innerHeight - nav - (variant === 'page' ? 40 : 64);
      const s = Math.max(MIN_SCALE, Math.min(1, room / (VIEW_H + 2 * BEZEL)));
      el.style.setProperty('--s', s.toFixed(4));
    };
    fit();
    addEventListener('resize', fit);
    return () => removeEventListener('resize', fit);
  }, [variant]);

  // When to load: /demo/ right away (after hydration, which knows the
  // page's ?open=); the home page once the stage nears the viewport (it has
  // no box on phones, so it never does there) and the browser is idle.
  useEffect(() => {
    const el = stage.current;
    if (variant !== 'home' || !el || typeof IntersectionObserver === 'undefined') return;
    let idle = 0;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const go = () => setNear(true);
    const io = new IntersectionObserver(
      (entries) => {
        if (!entries.some((e) => e.isIntersecting)) return;
        io.disconnect();
        if (typeof requestIdleCallback === 'function') idle = requestIdleCallback(go, { timeout: 1500 });
        else timer = setTimeout(go, 200);
      },
      { rootMargin: '200px 0px' },
    );
    io.observe(el);
    return () => {
      io.disconnect();
      if (idle && typeof cancelIdleCallback === 'function') cancelIdleCallback(idle);
      clearTimeout(timer);
    };
  }, [variant]);
  const src = !mounted ? null : variant === 'page' ? withBase(`${DEMO_UI}${openFromQuery()}?v=${hash}`) : near ? alone : null;

  const { armed } = ch;
  useEffect(() => {
    if (src) armed();
  }, [src, armed]);

  // The frame's focus ring (demo.css [data-focus]). Focus inside the frame
  // makes the iframe this document's activeElement, but it never matches
  // :focus here, and a Tab into its page fires nothing on it either: this
  // window's blur and focus are what change. A Tab into the frame doesn't
  // scroll this page either, so the frame is brought into view.
  useEffect(() => {
    const f = frame.current;
    if (!src || !f) return;
    const sync = () => {
      const on = document.activeElement === f;
      if (on && !('focus' in f.dataset)) {
        const r = f.getBoundingClientRect();
        if (r.top < 0 || r.bottom > innerHeight) f.scrollIntoView({ block: 'nearest', behavior: 'instant' });
      }
      if (on) f.dataset.focus = '';
      else delete f.dataset.focus;
    };
    const soon = () => {
      sync();
      setTimeout(sync, 0);
    };
    addEventListener('blur', soon);
    addEventListener('focus', sync);
    document.addEventListener('focusin', sync);
    return () => {
      removeEventListener('blur', soon);
      removeEventListener('focus', sync);
      document.removeEventListener('focusin', sync);
    };
  }, [src]);

  const closeSheet = useCallback(() => {
    const d = sheet.current;
    if (d?.open) d.close();
    opener.current?.focus();
  }, []);

  useEffect(() => {
    const d = sheet.current;
    if (!d) return;
    // A tap on the backdrop closes the sheet; Escape closes it natively.
    const onClick = (e: MouseEvent) => {
      if (e.target === d) closeSheet();
    };
    const onClose = () => opener.current?.focus();
    d.addEventListener('click', onClick);
    d.addEventListener('close', onClose);
    return () => {
      d.removeEventListener('click', onClick);
      d.removeEventListener('close', onClose);
    };
  }, [closeSheet]);

  const pairing = ch.state?.pairing ?? null;
  const panelId = `${id}-panel`;
  const endId = `${id}-end`;

  return (
    <div
      ref={stage}
      className={`demo-stage demo-${variant}`}
      data-started={ch.started ? '' : undefined}
      data-pin={variant === 'page' && pairing ? '' : undefined}
    >
      {variant === 'page' && (
        <header className="demo-head">
          <h1 className="demo-title cut-warm">{demoPage.headline.join(' ')}</h1>
          <p className="demo-lead">{demoPage.lead}</p>
          <button ref={opener} type="button" className="demo-open-sheet" aria-haspopup="dialog" onClick={() => sheet.current?.showModal()}>
            <Icon name="sliders" className="size-4 shrink-0" />
            {copy.mobile.scenarios}
          </button>
        </header>
      )}
      {variant === 'page' && pairing && (
        <p className="demo-pinbar">
          <span>{copy.moonlight.label}</span>
          <span className="demo-pinbar-pin telemetry">
            <span className="sr-only">{copy.moonlight.waitingShort} </span>
            {pairing.pin}
          </span>
        </p>
      )}

      <a href={`#${panelId}`} className="demo-skip demo-skip-panel">
        {copy.skip}
      </a>
      <a href={`#${endId}`} className="demo-skip demo-skip-end">
        {copy.skip}
      </a>
      <div className="demo-phone">
        <div className="demo-screen">
          {src && (
            <iframe
              ref={frame}
              className="demo-frame"
              src={src}
              title={copy.frameTitle}
              sandbox="allow-scripts allow-same-origin allow-forms allow-popups allow-popups-to-escape-sandbox"
              referrerPolicy="same-origin"
            />
          )}
          {!ch.started && (
            <div className="demo-skel" role="status">
              {ch.failed ? (
                <p>
                  {copy.failed}{' '}
                  <a href={src ?? alone} target="_blank" rel="noopener">
                    {copy.openAlone}
                    <span className="sr-only"> (opens in a new tab)</span>
                  </a>
                </p>
              ) : (
                <p>{copy.starting}</p>
              )}
            </div>
          )}
        </div>
      </div>

      <div className="demo-side" id={panelId} tabIndex={-1}>
        <ScenarioPanel ch={ch} headingId={`${id}-sc`}>
          <MoonlightMock ch={ch} />
        </ScenarioPanel>
        <Notes />
      </div>
      <span id={endId} tabIndex={-1} className="demo-end" />

      {variant === 'page' && (
        <dialog ref={sheet} className="demo-sheet" aria-labelledby={`${id}-sheet-sc`}>
          <div className="demo-sheet-in">
            <button type="button" className="demo-sheet-close" onClick={closeSheet}>
              <Icon name="close" className="size-4 shrink-0" />
              {copy.mobile.close}
            </button>
            <ScenarioPanel ch={ch} headingId={`${id}-sheet-sc`} onRun={closeSheet}>
              <MoonlightMock ch={ch} />
            </ScenarioPanel>
            <Notes />
          </div>
        </dialog>
      )}

      <p className="sr-only" aria-live="polite" aria-atomic="true">
        {ch.said}
      </p>
    </div>
  );
}
