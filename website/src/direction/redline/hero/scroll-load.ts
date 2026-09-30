// The hero's load, 0.34 at rest to 1 over the first 62% of the hero's
// scroll. HeroStage writes it on scroll (no re-render); the thermal canvas
// reads it every frame and eases toward it. A module-level value, because
// both live in the one hero on the page.
import { LOAD_REST } from './geometry';

let load = LOAD_REST;

export const heroLoad = {
  get: () => load,
  set: (v: number) => {
    load = v;
  },
};
