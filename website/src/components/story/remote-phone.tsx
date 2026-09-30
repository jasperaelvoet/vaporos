// The phone in B3: the control center's home page, ready to stream
// (content/story.ts remoteStory.phone), in the site's look: the state word
// in the warm cut over the ready field, the detail, the display's mode and
// the controls. The control center it draws follows content/demo.ts
// uiLabels: the dashboard's four quick actions (legacy), or Home with the
// PC's name and GPU on the field, its three Controls and the tab bar
// (next). Same frame as the installer phone (story.css .phone, a 300
// px-wide screen scaled with container units). Decorative (aria-hidden):
// the list beside it says the same in words.
import { LogoMark } from '@/components/ui/logo';
import { Icon } from '@/components/ui/icon';
import { remoteStory } from '@/content/story';
import { PhoneField } from './phone-field';

export function RemotePhone() {
  const p = remoteStory.phone;
  return (
    <div className={`phone phone-cc ${p.tabs ? 'phone-cc-next' : ''}`} aria-hidden="true">
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
          {p.tags && (
            <p className="phone-tags telemetry">
              {p.tags.map((t) => (
                <span key={t}>{t}</span>
              ))}
            </p>
          )}
          <p className="phone-word cut-warm">
            <span>{p.status.verb}</span>
            <span>{p.status.rest}</span>
          </p>
        </div>
        <p className="phone-hint">{p.detail}</p>
        <p className="phone-chip telemetry">{p.mode}</p>
        <p className="phone-k">{p.actionsLabel}</p>
        <div className="phone-actions">
          {p.actions.map((a) => (
            <span key={a.label} className="phone-act">
              <Icon name={a.icon} className="phone-act-icon" />
              {a.label}
            </span>
          ))}
        </div>
        {p.tabs && (
          <p className="phone-tabs">
            {p.tabs.map((t, i) => (
              <span key={t.label} className={i === 0 ? 'phone-tab on' : 'phone-tab'}>
                {t.icon === 'logo' ? <LogoMark size={18} /> : <Icon name={t.icon} className="phone-tab-icon" />}
                {t.label}
              </span>
            ))}
          </p>
        )}
      </div>
    </div>
  );
}
