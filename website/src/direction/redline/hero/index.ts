// REDLINE's hero (slot D1): the thermal view of a gaming PC.
//
//   thermal-hero.tsx   <ThermalHero release />: the server-rendered first frame
//   poster.tsx         the PC as flat isotherms (SVG), for everyone first
//   hero-stage.tsx     the scroll, and the gate that loads the live view (client)
//   thermal-canvas.tsx the live WebGL2 view, loaded with next/dynamic (client)
//   engine.ts          the stable-fluids heat solver and the display pass
//   shaders.ts         its GLSL
//   geometry.ts        where the PC stands, shared by the poster and the canvas
//   scroll-load.ts     the scroll's load, handed from the stage to the canvas
export { ThermalHero } from './thermal-hero';
