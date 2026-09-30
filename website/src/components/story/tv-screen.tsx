// A small welcome screen (slot D3): what a monitor on the PC shows, in the
// Go renderer's layout (internal/display/welcome) on its 1920 × 1080 grid,
// halved to 960 × 540 and scaled to the box with container units, so it
// needs no script. The words are the renderer's own (content/story.ts): the
// brand row with the version, the heat scale with its marker, the status on
// one line as the TV sets it (it breaks only when a status would shrink by a
// fifth), the address, and the hostname's mark before "Scan to open". The QR
// code is real and opens this website. Decorative: the beat says the same
// in words, so the picture is aria-hidden.
import type { CSSProperties } from 'react';
import { LogoMark, Wordmark } from '@/components/ui/logo';
import { siteQR, tvInstaller, tvReady } from '@/content/story';
import { tokens } from '@/lib/tokens.gen';
import { TvField } from './tv-field';
import { TvHandshake } from './tv-handshake';

// The field's peak per state, as on the TV: ready peaks at magenta, the
// installer a step cooler.
const READY_PEAK = 0.45;
const INSTALLER_PEAK = 0.36;

export function TvScreen({ state, className = '' }: { state: 'installer' | 'ready'; className?: string }) {
  const t = state === 'installer' ? tvInstaller : tvReady;
  const code = state === 'installer' ? tvInstaller.code : null;
  const scale = state === 'installer' ? tokens.states.installing : tokens.states.ready;
  return (
    <div className={`tv ${className}`} data-tv={state} aria-hidden="true">
      <div className="tv-inner">
        <TvField peak={state === 'installer' ? INSTALLER_PEAK : READY_PEAK} />
        <div className="tv-brand">
          <LogoMark size={29} />
          <Wordmark height={20} />
          <span className="tv-ver telemetry">{t.version}</span>
        </div>
        <div className="tv-hud telemetry" style={{ '--t': scale.heat } as CSSProperties}>
          <b>{scale.signage}</b>
          <i />
          <span>{tokens.states.asleep.signage}</span>
          <span>{tokens.states.streaming.signage}</span>
        </div>
        <div className="tv-text">
          <p className={`tv-status ${state === 'installer' ? 'cut-cold' : 'cut-warm'}`}>{t.status}</p>
          <p className="tv-detail">{t.detail}</p>
          <p className="tv-url telemetry">
            <span>{t.url.scheme}</span>
            {t.url.host}
          </p>
          <p className="tv-ip">
            {t.ipLead} <b className="telemetry">{t.ip}</b>
          </p>
          {code && (
            <div className="tv-code">
              <small>{code.label}</small>
              <b className="telemetry">{code.value}</b>
            </div>
          )}
        </div>
        <div className="tv-qr">
          <i className="a" />
          <i className="b" />
          <i className="c" />
          <i className="d" />
          <svg viewBox={`-4 -4 ${siteQR.size + 8} ${siteQR.size + 8}`} shapeRendering="crispEdges">
            <rect x={-4} y={-4} width={siteQR.size + 8} height={siteQR.size + 8} rx={1} className="fill-h9" />
            <path className="fill-ash" d={siteQR.d} />
          </svg>
        </div>
        <p className="tv-cap">
          <span>
            <TvHandshake mark={t.handshake} className="tv-hs" />
            {t.qrCaption}
          </span>
        </p>
      </div>
    </div>
  );
}
