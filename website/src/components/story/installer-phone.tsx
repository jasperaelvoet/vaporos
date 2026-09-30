// The phone beside the setup beat's TV: the web installer's first step as it
// is today (content/story.ts setupStory.phone), drawn on a 300 × 633 screen
// scaled with container units. Decorative (aria-hidden): the steps beside it
// say the same in words.
import { setupStory } from '@/content/story';

export function InstallerPhone() {
  const p = setupStory.phone;
  return (
    <div className="phone" aria-hidden="true">
      <div className="phone-scr">
        <p className="phone-top telemetry">
          <span>{p.host}</span>
          <span>{p.step}</span>
        </p>
        <div className="phone-stepper">
          {Array.from({ length: p.steps }, (_, i) => (
            <i key={i} className={i === 0 ? 'on' : undefined} />
          ))}
        </div>
        <p className="phone-title">{p.title}</p>
        <p className="phone-hint">{p.hint}</p>
        {p.drives.map((d) => (
          <div key={d.name} className={`phone-opt ${d.selected ? 'sel' : ''}`}>
            <b>{d.name}</b>
            <span className="telemetry">{d.meta}</span>
            {d.tag && <em className="telemetry">{d.tag}</em>}
          </div>
        ))}
        <div className="phone-go">{p.next}</div>
      </div>
    </div>
  );
}
