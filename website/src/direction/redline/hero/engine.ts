// The hero's heat engine: WebGL2 only, no library. A stable-fluids solver
// carries heat (it rises: buoyancy) and the pointer's steam; the display
// pass adds the analytic PC (cached at half resolution) and maps the sum
// through the Inferno ramp as posterised isotherms (shaders.ts).
//
//   const T = await createThermal(canvas, { maxDpr: 1.25, sim: 96, dye: 320, iters: 14 });
//   T.resize(); T.state.boot = 1; T.step(1 / 60); T.render(); … T.dispose();
//
// createThermal() resolves null when WebGL2 is missing or a program fails to
// link: the caller keeps the CSS poster. Programs link without blocking the
// main thread where the driver offers KHR_parallel_shader_compile. Without
// EXT_color_buffer_float there is no fluid, only the PC.
import { infernoLUT } from '../heat';
import { FS, VS, type ProgramName } from './shaders';

export interface ThermalOptions {
  /** Device pixel ratio cap (1.25 on desktop, 1 on touch). */
  maxDpr: number;
  /** Sim grid on the short side (velocity, pressure). */
  sim: number;
  /** Temperature grid on the short side. */
  dye: number;
  /** Jacobi pressure iterations. */
  iters: number;
  power?: WebGLPowerPreference;
}

export interface ThermalState {
  /** 0..1: the PC powering up (PSU, board, CPU, GPU). */
  boot: number;
  /** 0..1: CPU and GPU load. */
  load: number;
  /** The case in device pixels: left edge, bottom, height. */
  case: [number, number, number];
  /** Per exhaust: uv x, uv y, spread, strength. */
  sources: [number, number, number, number][];
  /** Per exhaust: velocity push. */
  dirs: [number, number][];
}

export interface Thermal {
  state: ThermalState;
  /** Whether the fluid runs (EXT_color_buffer_float). */
  fluid: boolean;
  /** Canvas size in device pixels, and the ratio used. */
  size(): { w: number; h: number; dpr: number };
  resize(): void;
  splat(x: number, y: number, dx: number, dy: number, heat: number): void;
  step(dt: number): void;
  render(time: number): void;
  dispose(): void;
}

interface Target {
  tex: WebGLTexture;
  fb: WebGLFramebuffer;
  w: number;
  h: number;
}
interface Pair {
  read: Target;
  write: Target;
  swap(): void;
  w: number;
  h: number;
}
type Uniforms = Record<string, WebGLUniformLocation | null>;
interface Program {
  p: WebGLProgram;
  u: Uniforms;
}

const COMPLETION_STATUS_KHR = 0x91b1;

export async function createThermal(canvas: HTMLCanvasElement, opts: ThermalOptions): Promise<Thermal | null> {
  const ctx = canvas.getContext('webgl2', {
    antialias: false,
    alpha: false,
    depth: false,
    stencil: false,
    premultipliedAlpha: false,
    preserveDrawingBuffer: false,
    powerPreference: opts.power ?? 'default',
  });
  if (!ctx) return null;
  const gl: WebGL2RenderingContext = ctx;
  const fluid = !!gl.getExtension('EXT_color_buffer_float');
  const parallel = !!gl.getExtension('KHR_parallel_shader_compile');

  // Compile and link everything, then wait for the driver without blocking.
  const vs = gl.createShader(gl.VERTEX_SHADER)!;
  gl.shaderSource(vs, VS);
  gl.compileShader(vs);
  const pending: [ProgramName, WebGLProgram, WebGLShader][] = [];
  for (const [name, src] of Object.entries(FS) as [ProgramName, string][]) {
    const fs = gl.createShader(gl.FRAGMENT_SHADER)!;
    gl.shaderSource(fs, src);
    gl.compileShader(fs);
    const p = gl.createProgram()!;
    gl.attachShader(p, vs);
    gl.attachShader(p, fs);
    gl.linkProgram(p);
    pending.push([name, p, fs]);
  }
  if (parallel) {
    await new Promise<void>((done) => {
      const poll = () => {
        if (gl.isContextLost() || pending.every(([, p]) => gl.getProgramParameter(p, COMPLETION_STATUS_KHR))) done();
        else setTimeout(poll, 16);
      };
      poll();
    });
  }
  if (gl.isContextLost()) return null;
  const P = {} as Record<ProgramName, Program>;
  for (const [name, p, fs] of pending) {
    if (!gl.getProgramParameter(p, gl.LINK_STATUS)) {
      console.warn(`thermal: ${name}: ${gl.getShaderInfoLog(fs) || gl.getProgramInfoLog(p)}`);
      return null;
    }
    const u: Uniforms = {};
    const n = gl.getProgramParameter(p, gl.ACTIVE_UNIFORMS) as number;
    for (let i = 0; i < n; i++) {
      const info = gl.getActiveUniform(p, i);
      if (info) u[info.name.replace('[0]', '')] = gl.getUniformLocation(p, info.name);
    }
    P[name] = { p, u };
    gl.deleteShader(fs);
  }
  gl.deleteShader(vs);

  // One triangle covers the screen.
  const vao = gl.createVertexArray();
  gl.bindVertexArray(vao);
  const buf = gl.createBuffer();
  gl.bindBuffer(gl.ARRAY_BUFFER, buf);
  gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1, -1, 3, -1, -1, 3]), gl.STATIC_DRAW);
  gl.enableVertexAttribArray(0);
  gl.vertexAttribPointer(0, 2, gl.FLOAT, false, 0, 0);

  // The Inferno LUT, sampled at band centres (NEAREST).
  const lut = gl.createTexture()!;
  gl.bindTexture(gl.TEXTURE_2D, lut);
  gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, 256, 1, 0, gl.RGBA, gl.UNSIGNED_BYTE, infernoLUT(256));
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.NEAREST);
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.NEAREST);
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);

  const targets: Target[] = [];
  function target(w: number, h: number, internal: number, format: number, type: number): Target {
    const tex = gl.createTexture()!;
    gl.bindTexture(gl.TEXTURE_2D, tex);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
    gl.texImage2D(gl.TEXTURE_2D, 0, internal, w, h, 0, format, type, null);
    const fb = gl.createFramebuffer()!;
    gl.bindFramebuffer(gl.FRAMEBUFFER, fb);
    gl.framebufferTexture2D(gl.FRAMEBUFFER, gl.COLOR_ATTACHMENT0, gl.TEXTURE_2D, tex, 0);
    gl.viewport(0, 0, w, h);
    gl.clearColor(0, 0, 0, 1);
    gl.clear(gl.COLOR_BUFFER_BIT);
    const t = { tex, fb, w, h };
    targets.push(t);
    return t;
  }
  const half = (w: number, h: number, internal: number, format: number) => target(w, h, internal, format, gl.HALF_FLOAT);
  function pair(w: number, h: number, internal: number, format: number): Pair {
    let a = half(w, h, internal, format);
    let b = half(w, h, internal, format);
    return {
      get read() {
        return a;
      },
      get write() {
        return b;
      },
      swap() {
        [a, b] = [b, a];
      },
      w,
      h,
    };
  }
  function freeTargets() {
    for (const t of targets.splice(0)) {
      gl.deleteTexture(t.tex);
      gl.deleteFramebuffer(t.fb);
    }
  }

  let W = 0;
  let Hh = 0;
  let dpr = 1;
  let vel: Pair | null = null;
  let temp: Pair | null = null;
  let pres: Pair | null = null;
  let div: Target | null = null;
  let curlT: Target | null = null;
  let pcT: Target | null = null;
  let pcKey = '';
  const state: ThermalState = { boot: 0, load: 0, case: [0, 0, 1], sources: [], dirs: [] };

  function resize() {
    dpr = Math.min(window.devicePixelRatio || 1, opts.maxDpr);
    const r = canvas.getBoundingClientRect();
    const w = Math.max(2, Math.round(r.width * dpr));
    const h = Math.max(2, Math.round(r.height * dpr));
    if (w === W && h === Hh && pcT) return;
    W = w;
    Hh = h;
    canvas.width = W;
    canvas.height = Hh;
    freeTargets();
    // The PC layer: half resolution; 8 bits are enough once it is posterised.
    pcT = target(Math.ceil(W / 2), Math.ceil(Hh / 2), gl.RGBA8, gl.RGBA, gl.UNSIGNED_BYTE);
    pcKey = '';
    if (!fluid) return;
    const aspect = W / Hh;
    const grid = (n: number): [number, number] =>
      aspect >= 1 ? [Math.round(n * aspect), n] : [n, Math.round(n / aspect)];
    const [sw, sh] = grid(opts.sim);
    const [dw, dh] = grid(opts.dye);
    vel = pair(sw, sh, gl.RG16F, gl.RG);
    temp = pair(dw, dh, gl.R16F, gl.RED);
    pres = pair(sw, sh, gl.R16F, gl.RED);
    div = half(sw, sh, gl.R16F, gl.RED);
    curlT = half(sw, sh, gl.R16F, gl.RED);
  }

  const bind = (tex: WebGLTexture, unit: number) => {
    gl.activeTexture(gl.TEXTURE0 + unit);
    gl.bindTexture(gl.TEXTURE_2D, tex);
    return unit;
  };
  function draw(t: Target | null) {
    if (t) {
      gl.bindFramebuffer(gl.FRAMEBUFFER, t.fb);
      gl.viewport(0, 0, t.w, t.h);
    } else {
      gl.bindFramebuffer(gl.FRAMEBUFFER, null);
      gl.viewport(0, 0, W, Hh);
    }
    gl.drawArrays(gl.TRIANGLES, 0, 3);
  }
  const program = (k: ProgramName) => {
    gl.useProgram(P[k].p);
    return P[k].u;
  };
  const texel = (t: { w: number; h: number }): [number, number] => [1 / t.w, 1 / t.h];

  function splat(x: number, y: number, dx: number, dy: number, heat: number) {
    if (!vel || !temp) return;
    const u = program('splat');
    gl.uniform1i(u.uTarget, bind(vel.read.tex, 0));
    gl.uniform1f(u.aspect, W / Hh);
    gl.uniform2f(u.point, x, y);
    gl.uniform3f(u.color, dx, dy, 0);
    gl.uniform1f(u.radius, 0.0022);
    draw(vel.write);
    vel.swap();
    gl.uniform1i(u.uTarget, bind(temp.read.tex, 0));
    gl.uniform3f(u.color, heat, 0, 0);
    gl.uniform1f(u.radius, 0.0016);
    draw(temp.write);
    temp.swap();
  }

  const srcData = new Float32Array(16);
  const dirData = new Float32Array(8);
  function sourceUniforms(u: Uniforms) {
    srcData.fill(0);
    dirData.fill(0);
    state.sources.forEach((v, i) => srcData.set(v, i * 4));
    state.dirs.forEach((v, i) => dirData.set(v, i * 2));
    if (u.uSrc) gl.uniform4fv(u.uSrc, srcData);
    if (u.uDir) gl.uniform2fv(u.uDir, dirData);
  }

  function step(dt: number) {
    if (!vel || !temp || !pres || !div || !curlT) return;
    const aspect = W / Hh;
    let u = program('curl');
    gl.uniform2fv(u.texel, texel(vel));
    gl.uniform1i(u.uVelocity, bind(vel.read.tex, 0));
    draw(curlT);
    u = program('vorticity');
    gl.uniform2fv(u.texel, texel(vel));
    gl.uniform1i(u.uVelocity, bind(vel.read.tex, 0));
    gl.uniform1i(u.uCurl, bind(curlT.tex, 1));
    gl.uniform1f(u.curl, 16);
    gl.uniform1f(u.dt, dt);
    draw(vel.write);
    vel.swap();
    u = program('buoy');
    gl.uniform2fv(u.texel, texel(vel));
    gl.uniform1i(u.uVelocity, bind(vel.read.tex, 0));
    gl.uniform1i(u.uTemp, bind(temp.read.tex, 1));
    gl.uniform1f(u.dt, dt);
    gl.uniform1f(u.buoy, 26);
    gl.uniform1f(u.aspect, aspect);
    sourceUniforms(u);
    draw(vel.write);
    vel.swap();
    u = program('divergence');
    gl.uniform2fv(u.texel, texel(vel));
    gl.uniform1i(u.uVelocity, bind(vel.read.tex, 0));
    draw(div);
    u = program('clear');
    gl.uniform1i(u.uTexture, bind(pres.read.tex, 0));
    gl.uniform1f(u.value, 0.8);
    draw(pres.write);
    pres.swap();
    u = program('pressure');
    gl.uniform2fv(u.texel, texel(vel));
    gl.uniform1i(u.uDivergence, bind(div.tex, 1));
    for (let i = 0; i < opts.iters; i++) {
      gl.uniform1i(u.uPressure, bind(pres.read.tex, 0));
      draw(pres.write);
      pres.swap();
    }
    u = program('gradient');
    gl.uniform2fv(u.texel, texel(vel));
    gl.uniform1i(u.uPressure, bind(pres.read.tex, 0));
    gl.uniform1i(u.uVelocity, bind(vel.read.tex, 1));
    draw(vel.write);
    vel.swap();
    u = program('advect');
    gl.uniform2fv(u.texel, texel(vel));
    gl.uniform1i(u.uVelocity, bind(vel.read.tex, 0));
    gl.uniform1i(u.uSource, bind(vel.read.tex, 0));
    gl.uniform1f(u.dt, dt);
    gl.uniform1f(u.dissipation, 0.25);
    draw(vel.write);
    vel.swap();
    u = program('source');
    gl.uniform2fv(u.texel, texel(temp));
    gl.uniform1i(u.uTemp, bind(temp.read.tex, 0));
    gl.uniform1f(u.dt, dt);
    gl.uniform1f(u.aspect, aspect);
    sourceUniforms(u);
    draw(temp.write);
    temp.swap();
    u = program('advect');
    gl.uniform2fv(u.texel, texel(vel));
    gl.uniform1i(u.uVelocity, bind(vel.read.tex, 0));
    gl.uniform1i(u.uSource, bind(temp.read.tex, 1));
    gl.uniform1f(u.dt, dt);
    gl.uniform1f(u.dissipation, 1.5);
    draw(temp.write);
    temp.swap();
  }

  function render(time: number) {
    if (!pcT) return;
    // Redraw the PC only when it changed (the boot, a scroll, a resize).
    const key = `${state.boot.toFixed(3)} ${state.load.toFixed(3)} ${state.case.join(',')}`;
    if (key !== pcKey) {
      const p = program('pcpass');
      gl.uniform3fv(p.uCase, state.case);
      gl.uniform1f(p.uBoot, state.boot);
      gl.uniform1f(p.uLoad, state.load);
      gl.uniform1f(p.uScale, W / pcT.w);
      gl.uniform2fv(p.texel, texel(pcT));
      draw(pcT);
      pcKey = key;
    }
    const u = program('display');
    gl.uniform2fv(u.texel, [1 / W, 1 / Hh]);
    gl.uniform1i(u.uTemp, bind(temp ? temp.read.tex : lut, 0));
    gl.uniform1i(u.uLut, bind(lut, 1));
    gl.uniform1i(u.uPc, bind(pcT.tex, 2));
    gl.uniform2f(u.uRes, W, Hh);
    gl.uniform1f(u.uTime, time);
    gl.uniform1f(u.uFluid, temp ? 0.8 : 0);
    gl.uniform1f(u.uBands, 14);
    gl.uniform1f(u.uGain, 1.5);
    draw(null);
  }

  function dispose() {
    freeTargets();
    for (const k of Object.keys(P) as ProgramName[]) gl.deleteProgram(P[k].p);
    gl.deleteTexture(lut);
    gl.deleteBuffer(buf);
    gl.deleteVertexArray(vao);
    gl.getExtension('WEBGL_lose_context')?.loseContext();
  }

  resize();
  return {
    state,
    fluid,
    size: () => ({ w: W, h: Hh, dpr }),
    resize,
    splat,
    step,
    render,
    dispose,
  };
}
