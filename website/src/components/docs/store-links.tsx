// Where to get Moonlight: one link per store, named by the devices it covers.
// External links open in the same tab.
import { Icon } from '@/components/ui';
import type { Store } from '@/content/install';

export function StoreLinks({ stores }: { stores: readonly Store[] }) {
  return (
    <ul className="grid gap-3 desk:grid-cols-3">
      {stores.map((s) => (
        <li key={s.url}>
          <a
            href={s.url}
            className="group flex h-full min-h-16 items-center gap-3.5 rounded-xl px-4 py-3 no-underline inset-ring-1 inset-ring-line transition-shadow hover:inset-ring-smoke"
          >
            <Icon name={s.icon} className="size-5 shrink-0 text-smoke" />
            <span className="mr-auto grid min-w-0">
              <span className="font-semibold text-bone">{s.name}</span>
              <span className="text-fine text-smoke">{s.where}</span>
            </span>
            <Icon name="external" className="size-4 shrink-0 text-dim transition-colors group-hover:text-bone" />
          </a>
        </li>
      ))}
    </ul>
  );
}
