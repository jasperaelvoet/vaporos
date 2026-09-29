// The 404 page: exported as out/404.html, which GitHub Pages serves for any
// missing path under /vaporos/. Links and assets are absolute
// (/vaporos/...), so it works at any depth.
import type { Metadata } from 'next';
import { LogoMark } from '@/components/logo';
import { Button, Container } from '@/components/plain';
import { Rich } from '@/components/rich';
import { notFound, pageMeta } from '@/content';
import { pageMetadata } from '@/lib/metadata';

// Next adds <meta name="robots" content="noindex"> to this page by itself.
export const metadata: Metadata = { ...pageMetadata(pageMeta.notFound), robots: undefined };

export default function NotFound() {
  return (
    <Container className="grid min-h-[70vh] place-items-center py-16 text-center">
      <div className="grid justify-items-center gap-5">
        <LogoMark size={72} />
        <p className="text-vapor text-8xl font-bold tracking-tighter">{notFound.code}</p>
        <h1 className="text-4xl font-bold">{notFound.heading}</h1>
        <p className="text-lg text-fg-2">
          <Rich text={notFound.lead} />
        </p>
        <div className="flex flex-wrap justify-center gap-3">
          {notFound.actions.map((a) => (
            <Button key={a.href} item={a} />
          ))}
        </div>
      </div>
    </Container>
  );
}
