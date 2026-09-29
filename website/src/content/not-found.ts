// The 404 page. GitHub Pages serves out/404.html for any missing path under
// /vaporos/. Ported from pages/404.astro.
import { routes } from './site';
import type { ButtonItem, Rich } from './types';

export const notFound = {
  code: '404',
  heading: 'This page evaporated.',
  lead: 'The link may be old, or the page moved. Try one of these instead.' as Rich,
  actions: [
    { label: 'Home', href: routes.home, icon: 'home', primary: true },
    { label: 'Download', href: routes.download, icon: 'download' },
    { label: 'FAQ', href: routes.faq, icon: 'question' },
  ] satisfies ButtonItem[],
};
