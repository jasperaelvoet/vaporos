// A plain rendition of the web UI dashboard at vapor.local (content/mock.ts).
// Decorative: its caption (controlCenter.mockNote) says what it is.
import { dashboard } from '@/content/mock';
import { site } from '@/content/site';

export function DashboardMock() {
  return (
    <div aria-hidden="true" className="grid gap-3 rounded-3xl border border-line-2 bg-bg p-4 text-sm">
      <div className="flex items-center justify-between">
        <span className="font-semibold">
          {site.wordmark.text}
          <b className="text-brand">{site.wordmark.accent}</b>
        </span>
        <span className="inline-flex items-center gap-1.5 text-xs text-ok">
          <i className="size-1.5 rounded-full bg-ok" />
          {dashboard.live}
        </span>
      </div>
      <div className="grid gap-0.5 rounded-2xl bg-surface p-4">
        <span className="text-xs tracking-widest text-fg-3 uppercase">{dashboard.host}</span>
        <span className="text-lg font-semibold">{dashboard.status}</span>
        <span className="text-fg-2">{dashboard.detail}</span>
      </div>
      <div className="grid gap-1 rounded-2xl bg-surface p-4">
        <span className="text-xs text-fg-3">{dashboard.updates.title}</span>
        <span className="inline-flex items-center gap-2">
          <i className="size-2 rounded-full bg-ok" />
          {dashboard.updates.status}
        </span>
      </div>
      <div className="grid gap-1 rounded-2xl bg-surface p-4">
        <span className="text-xs text-fg-3">{dashboard.thisPc.title}</span>
        {dashboard.thisPc.rows.map((r) => (
          <span key={r.key} className="flex justify-between gap-4">
            <span className="text-fg-3">{r.key}</span>
            <span>{r.value}</span>
          </span>
        ))}
      </div>
      <div className="grid gap-2 rounded-2xl bg-surface p-4">
        <span className="text-xs text-fg-3">{dashboard.quickActions.title}</span>
        <div className="flex flex-wrap gap-2">
          {dashboard.quickActions.actions.map((a) => (
            <span key={a.label} className={`rounded-lg border px-2 py-1 ${a.danger ? 'border-danger/40 text-danger' : 'border-line-2'}`}>
              {a.label}
            </span>
          ))}
        </div>
      </div>
    </div>
  );
}
