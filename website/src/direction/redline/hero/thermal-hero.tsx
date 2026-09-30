// B1, the hero (slot D1): a thermal view of a gaming PC under "Leave the
// heat in the other room.". The server renders the whole first frame: the
// poster (the PC as flat isotherms), the HUD, the spot meter on the GPU and
// the words; nothing in it waits for JavaScript, and the headline is the
// Largest Contentful Paint. HeroStage (client) then runs the scroll and, on
// desktops, loads the live WebGL view over the poster.
//
// The headline's lines arrive from the cold cut (hero.css), the scroll
// stretches it toward the hot cut, and friction heat warms it on desktop.
// Two invisible markers tell the page's heat scale where the hero is ready
// and where the scroll has started a game.
import type { ReactNode } from 'react';
import { ReleaseChip } from '@/components/site/release-chip';
import { Headline } from '@/components/ui/headline';
import { LinkButton } from '@/components/ui/button';
import { Rich } from '@/components/ui/rich';
import { hero } from '@/content/home';
import { heroView } from '@/content/story';
import type { ReleaseLookup } from '@/lib/release-shape';
import { HEAT } from '../heat';
import { HeroPoster } from './poster';
import { HeroStage } from './hero-stage';
import './hero.css';

export function ThermalHero({ release }: { release: ReleaseLookup }) {
  return (
    <section className="hero" data-hero aria-labelledby="hero-title">
      <span className="hero-mark hero-mark-ready" data-heat={HEAT.ready} data-heat-label="ready" aria-hidden="true" />
      <span className="hero-mark hero-mark-hot" data-heat={HEAT.streaming} data-heat-label="streaming" aria-hidden="true" />
      <div className="hero-rig" aria-hidden="true">
        <HeroPoster />
      </div>
      <HeroStage />
      <div className="hero-shade" aria-hidden="true" />
      <div className="hero-rig hero-rig-top" aria-hidden="true">
        <span className="hero-spot">
          <span className="hero-spot-cross" />
          <span className="hero-spot-tag">
            <b>{heroView.spot.label}</b>
            <span className="hero-spot-ready">{heroView.spot.ready}</span>
            <span className="hero-spot-hot">
              {heroView.spot.hot}
              <span className="hero-spot-mode">{heroView.spot.hotMode}</span>
            </span>
          </span>
        </span>
      </div>
      <div className="hero-hud" aria-hidden="true">
        <i className="tl" />
        <i className="tr" />
        <i className="bl" />
        <i className="br" />
        <p className="hero-hud-cap telemetry">
          {heroView.hud.map((s) => (
            <span key={s}>{s}</span>
          ))}
        </p>
      </div>

      <div className="hero-copy">
        <p className="hero-kicker">{hero.kicker}</p>
        <Headline
          as="h1"
          id="hero-title"
          cut="warm"
          size="hero"
          stretch={24}
          stretchNarrow={8}
          stretchG={100}
          friction={8}
          lines={hero.title.lines}
          narrow={hero.title.narrow}
          fitK={hero.title.fitK}
          fitKNarrow={hero.title.fitKNarrow}
          className="hero-title"
        />
        <p className="hero-lead">
          <Lead long={<Rich text={hero.lead} />} short={<Rich text={hero.leadShort} />} />
        </p>
        <div className="hero-ctas">
          {hero.ctas.map((c) => (
            <LinkButton key={c.href} href={c.href} variant={c.primary ? 'hot' : 'ghost'} icon={c.icon}>
              {c.label}
            </LinkButton>
          ))}
        </div>
        <div className="hero-facts">
          <ul aria-label={hero.factsLabel} className="telemetry">
            {hero.facts.map((f) => (
              <li key={f.label}>{f.label}</li>
            ))}
          </ul>
          <ReleaseChip initial={release} className="desk:hidden" />
        </div>
      </div>
    </section>
  );
}

/** The lead in full from 480 px, and its short form below. */
function Lead({ long, short }: { long: ReactNode; short: ReactNode }) {
  return (
    <>
      <span className="hero-lead-long">{long}</span>
      <span className="hero-lead-short">{short}</span>
    </>
  );
}
