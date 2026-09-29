// @ts-check
import { defineConfig } from 'astro/config';
import sitemap from '@astrojs/sitemap';

// Served from GitHub Pages at https://jasperaelvoet.github.io/vaporos/.
export default defineConfig({
  site: 'https://jasperaelvoet.github.io',
  base: '/vaporos',
  trailingSlash: 'ignore',
  integrations: [sitemap({ filter: (page) => !page.includes('/404') })],
  build: { format: 'directory' },
});
