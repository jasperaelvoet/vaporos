import type { Requirement } from '@/content/requirements';
import { IconTile } from './plain';
import { Rich } from './rich';

export function RequirementList({ items }: { items: Requirement[] }) {
  return (
    <ul className="grid gap-4 sm:grid-cols-2">
      {items.map((r) => (
        <li key={r.id} className="flex gap-4 rounded-xl border border-line p-4">
          <IconTile name={r.icon} />
          <div>
            <h3 className="font-semibold">{r.title}</h3>
            <p className="text-sm text-fg-2">
              <Rich text={r.body} />
            </p>
          </div>
        </li>
      ))}
    </ul>
  );
}
