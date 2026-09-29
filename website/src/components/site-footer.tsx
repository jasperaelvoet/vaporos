import { footer } from '@/content/site';
import { Logo } from './logo';
import { SmartLink } from './smart-link';

export function SiteFooter() {
  return (
    <footer className="mt-24 border-t border-line">
      <div className="mx-auto grid max-w-5xl gap-10 px-4 py-12 sm:grid-cols-[2fr_1fr_1fr] sm:px-6">
        <div className="grid content-start gap-4">
          <Logo size={30} />
          <p className="max-w-sm text-sm text-fg-2">{footer.blurb}</p>
        </div>
        {footer.groups.map((g) => (
          <nav key={g.title} aria-label={g.title}>
            <h2 className="mb-3 text-xs font-semibold tracking-widest text-fg-3 uppercase">{g.title}</h2>
            <ul className="grid gap-2 text-sm">
              {g.links.map((l) => (
                <li key={l.href}>
                  <SmartLink href={l.href} className="text-fg-2 hover:text-fg hover:underline">
                    {l.label}
                  </SmartLink>
                </li>
              ))}
            </ul>
          </nav>
        ))}
      </div>
      <div className="mx-auto flex max-w-5xl flex-wrap justify-between gap-x-6 gap-y-2 border-t border-line px-4 py-6 text-sm text-fg-3 sm:px-6">
        <p>{footer.license}</p>
        <p>{footer.disclaimer}</p>
      </div>
    </footer>
  );
}
