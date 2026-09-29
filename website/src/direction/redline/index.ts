// REDLINE's kit for the website: the product seen through a thermal camera.
// Width is temperature, the Inferno ramp does all the signalling, and faults
// are the only cold colour. Import through '@/direction'.
//
//   heat.ts               the heat scale: ramps, heatRGB, infernoLUT (WebGL), HEAT, stateHeat
//   painter.ts            the 2D thermal painter shared by the small canvases:
//                         paintIsotherms, around (a rising, air-warped source), noise
//   use-thermal-canvas.ts useThermalCanvas: size and paint a canvas lazily (client)
//   heat-scale.tsx        <HeatScale />, the page's scale on the right edge (client; in the layout)
//   footer-ember.tsx      <FooterEmber />, slot D7 (client)
// The type side (cuts, fitting, friction) lives with the rest of the type in
// src/lib/type and src/components/ui/headline.tsx.
export * from './heat';
export * from './painter';
export { useThermalCanvas, type ThermalCanvasOptions, type ThermalPaintInfo } from './use-thermal-canvas';
export { HeatScale } from './heat-scale';
export { FooterEmber } from './footer-ember';
