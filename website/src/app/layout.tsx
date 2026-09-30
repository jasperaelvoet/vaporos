import type { Metadata, Viewport } from 'next';
import { SiteFooter } from '@/components/site/site-footer';
import { SiteHeader } from '@/components/site/site-header';
import { nav } from '@/content/site';
import { HeatScale } from '@/direction';
import { chromeScript } from '@/lib/chrome-script';
import { fontVariables } from '@/lib/fonts';
import { rootMetadata, rootViewport } from '@/lib/metadata';
import { MotionRuntimeLazy } from '@/lib/motion/runtime-lazy';
import { getLatestRelease } from '@/lib/release';
import './globals.css';

export const metadata: Metadata = rootMetadata;
export const viewport: Viewport = rootViewport;

// The chrome every page shares. Pages start under the fixed, transparent nav:
// a page's first section clears it itself (padding-top: var(--nav-h) and
// more), except a full-bleed hero that wants the nav over its first frame.
export default async function RootLayout({ children }: LayoutProps<'/'>) {
  // Baked in at build time; the header's chip and the footer upgrade it live.
  const release = await getLatestRelease();
  return (
    // The inline script sets data-nav and data-motion on <html> before React
    // hydrates; suppressHydrationWarning covers exactly those attributes.
    <html lang="en" className={fontVariables} suppressHydrationWarning>
      <body className="flex min-h-dvh flex-col">
        <script dangerouslySetInnerHTML={{ __html: chromeScript }} />
        {/* Without JavaScript the nav keeps its scrim, so nothing scrolls under bare links. */}
        <noscript>
          <style>{'.site-nav::before{opacity:1}'}</style>
        </noscript>
        <a href="#main" className="skip-link">
          {nav.skipLink}
        </a>
        <SiteHeader release={release} />
        <main id="main" tabIndex={-1} className="flex-1">
          {children}
        </main>
        <SiteFooter release={release} />
        <HeatScale />
        <MotionRuntimeLazy />
      </body>
    </html>
  );
}
