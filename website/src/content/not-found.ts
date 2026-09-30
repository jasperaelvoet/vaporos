// The 404 page. GitHub Pages serves out/404.html for any missing path under
// /vaporos/. Ported from pages/404.astro.
import { demo } from './demo';
import { routes } from './site';
import type { ButtonItem, Rich } from './types';

export const notFound = {
  code: '404',
  heading: 'This page evaporated.',
  /** The heading as it is set, line by line. */
  headline: ['This page', 'evaporated.'],
  /** What the dead screen in the picture reads, under the code. */
  screen: 'No signal',
  lead: 'The link may be old, or the page moved. Try one of these instead.' as Rich,
  actions: [
    { label: 'Home', href: routes.home, icon: 'home', primary: true },
    { label: 'Download', href: routes.download, icon: 'download' },
    { label: 'Install guide', href: routes.install, icon: 'book' },
    ...(demo.enabled ? [{ label: 'Live demo', href: routes.demo, icon: 'phone' as const }] : []),
    { label: 'FAQ', href: routes.faq, icon: 'question' },
  ] satisfies ButtonItem[],
};
