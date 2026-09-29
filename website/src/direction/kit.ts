// The direction slots of the website (WEB §5, §6.2). Pages import visuals
// through '@/direction' and never from a direction's folder, so a direction
// is a fill, not a rewrite. The chosen direction is REDLINE.
import type { StateName } from '@/lib/tokens.gen';

/** The device-state vocabulary the site's visuals react to (tokens: states). */
export type DeviceState = StateName;

export type DirectionId = 'field-unit' | 'on-air' | 'sodium' | 'redline';

export interface DirectionInfo {
  id: DirectionId;
  /** D12: REDLINE is dark only, with no theme switch. */
  theme: { default: 'dark' | 'light' | 'auto'; toggle: boolean };
  /** D13: no sound anywhere. */
  sound: false;
}

export const direction: DirectionInfo = { id: 'redline', theme: { default: 'dark', toggle: false }, sound: false };
