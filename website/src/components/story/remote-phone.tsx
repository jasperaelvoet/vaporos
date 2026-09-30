// The phone in B3 and B4: the control center's home page as it is today,
// ready to stream (content/story.ts remoteStory.phone), in the site's look:
// the state word in the warm cut over the ready field, the detail, the
// display's mode and the four quick actions. Same frame as the installer
// phone (story.css .phone, a 300 px-wide screen scaled with container
// units). Decorative (aria-hidden): the list beside it says the same in words.
import { LogoMark } from '@/components/ui/logo';
import { Icon } from '@/components/ui/icon';
import type { IconName } from '@/lib/icons';
import { remoteStory } from '@/content/story';
import { PhoneField } from './phone-field';

// The quick actions' icons on the page today (dashboard.html).
const ICONS: IconName[] = ['phone', 'coffee', 'restart', 'power'];

export function RemotePhone() {
  const p = remoteStory.phone;
  return (
    <div className="phone phone-cc" aria-hidden="true">
      <div className="phone-scr">
        <p className="phone-bar">
          <LogoMark size={20} />
          <span className="phone-host telemetry">{p.host}</span>
          <span className="phone-live">{p.live}</span>
        </p>
        <div className="phone-view">
          <PhoneField />
          <i className="phone-view-a" />
          <i className="phone-view-b" />
          <p className="phone-word cut-warm">
            <span>{p.status.verb}</span>
            <span>{p.status.rest}</span>
          </p>
        </div>
        <p className="phone-hint">{p.detail}</p>
        <p className="phone-chip telemetry">{p.mode}</p>
        <p className="phone-k">{p.actionsLabel}</p>
        <div className="phone-actions">
          {p.actions.map((a, i) => (
            <span key={a} className="phone-act">
              <Icon name={ICONS[i]} className="phone-act-icon" />
              {a}
            </span>
          ))}
        </div>
      </div>
    </div>
  );
}
