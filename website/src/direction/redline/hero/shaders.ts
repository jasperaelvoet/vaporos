// GLSL for the hero's heat: a stable-fluids solver after Stam, its structure
// following Pavel Dobryakov's MIT WebGL fluid simulation, rewritten for heat
// (temperature instead of dye, buoyancy, exhaust sources), plus an analytic
// thermal image of a mid-tower PC and the display pass that maps both
// through the Inferno ramp as posterised isotherms.

export const VS = `#version 300 es
precision highp float;
layout(location=0) in vec2 aPos;
uniform vec2 texel;
out vec2 vUv, vL, vR, vT, vB;
void main(){ vUv=aPos*.5+.5; vL=vUv-vec2(texel.x,0.); vR=vUv+vec2(texel.x,0.); vT=vUv+vec2(0.,texel.y); vB=vUv-vec2(0.,texel.y); gl_Position=vec4(aPos,0.,1.); }`;

const H = `#version 300 es
precision highp float;
in vec2 vUv, vL, vR, vT, vB;
out vec4 o;
`;

export const FS = {
  splat:
    H +
    `uniform sampler2D uTarget; uniform float aspect, radius; uniform vec3 color; uniform vec2 point;
void main(){ vec2 p=vUv-point; p.x*=aspect; o=vec4(texture(uTarget,vUv).xyz+exp(-dot(p,p)/radius)*color,1.); }`,
  advect:
    H +
    `uniform sampler2D uVelocity, uSource; uniform vec2 texel; uniform float dt, dissipation;
void main(){ vec2 c=vUv-dt*texture(uVelocity,vUv).xy*texel; o=texture(uSource,c)/(1.+dissipation*dt); }`,
  divergence:
    H +
    `uniform sampler2D uVelocity;
void main(){ float L=texture(uVelocity,vL).x,R=texture(uVelocity,vR).x,T=texture(uVelocity,vT).y,B=texture(uVelocity,vB).y; vec2 C=texture(uVelocity,vUv).xy;
 if(vL.x<0.)L=-C.x; if(vR.x>1.)R=-C.x; if(vT.y>1.)T=-C.y; if(vB.y<0.)B=-C.y; o=vec4(.5*(R-L+T-B),0.,0.,1.); }`,
  curl:
    H +
    `uniform sampler2D uVelocity;
void main(){ float L=texture(uVelocity,vL).y,R=texture(uVelocity,vR).y,T=texture(uVelocity,vT).x,B=texture(uVelocity,vB).x; o=vec4(.5*(R-L-T+B),0.,0.,1.); }`,
  vorticity:
    H +
    `uniform sampler2D uVelocity, uCurl; uniform float curl, dt;
void main(){ float L=texture(uCurl,vL).x,R=texture(uCurl,vR).x,T=texture(uCurl,vT).x,B=texture(uCurl,vB).x,C=texture(uCurl,vUv).x;
 vec2 f=.5*vec2(abs(T)-abs(B),abs(R)-abs(L)); f/=length(f)+1e-4; f*=curl*C; f.y*=-1.;
 o=vec4(clamp(texture(uVelocity,vUv).xy+f*dt,-1000.,1000.),0.,1.); }`,
  pressure:
    H +
    `uniform sampler2D uPressure, uDivergence;
void main(){ float L=texture(uPressure,vL).x,R=texture(uPressure,vR).x,T=texture(uPressure,vT).x,B=texture(uPressure,vB).x; o=vec4((L+R+B+T-texture(uDivergence,vUv).x)*.25,0.,0.,1.); }`,
  gradient:
    H +
    `uniform sampler2D uPressure, uVelocity;
void main(){ float L=texture(uPressure,vL).x,R=texture(uPressure,vR).x,T=texture(uPressure,vT).x,B=texture(uPressure,vB).x; o=vec4(texture(uVelocity,vUv).xy-vec2(R-L,T-B),0.,1.); }`,
  clear: H + `uniform sampler2D uTexture; uniform float value; void main(){ o=value*texture(uTexture,vUv); }`,
  // Buoyancy (heat rises) and the exhausts' push.
  buoy:
    H +
    `uniform sampler2D uVelocity, uTemp; uniform float dt, buoy, aspect; uniform vec4 uSrc[4]; uniform vec2 uDir[4];
void main(){ vec2 v=texture(uVelocity,vUv).xy; v.y+=dt*buoy*texture(uTemp,vUv).x;
 for(int i=0;i<4;i++){ vec2 p=vUv-uSrc[i].xy; p.x*=aspect; v+=dt*uDir[i]*exp(-dot(p,p)/uSrc[i].z); } o=vec4(v,0.,1.); }`,
  // The exhausts feed the temperature field.
  source:
    H +
    `uniform sampler2D uTemp; uniform float dt, aspect; uniform vec4 uSrc[4];
void main(){ float T=texture(uTemp,vUv).x; for(int i=0;i<4;i++){ vec2 p=vUv-uSrc[i].xy; p.x*=aspect; T+=dt*uSrc[i].w*exp(-dot(p,p)/uSrc[i].z); } o=vec4(min(T,3.),0.,0.,1.); }`,
  // The PC's own heat, cached in a half-resolution layer: redrawn only when
  // boot, load or the layout change, never every frame. uBoot powers the
  // parts up in order (PSU, board, CPU, GPU); uLoad heats CPU and GPU.
  pcpass:
    H +
    `uniform vec3 uCase; uniform float uBoot, uLoad, uScale;
float sdBox(vec2 p, vec2 b, float r){ vec2 q=abs(p)-b+r; return length(max(q,0.))+min(max(q.x,q.y),0.)-r; }
float fill(float d, float e){ return 1.-smoothstep(-e,e,d); }
float glow(float d, float k){ return exp(-max(d,0.)/k); }
float hash(vec2 p){ return fract(sin(dot(p,vec2(12.9898,78.233)))*43758.5453); }
float vn(vec2 p){ vec2 i=floor(p), f=fract(p); f=f*f*(3.-2.*f); return mix(mix(hash(i),hash(i+vec2(1,0)),f.x),mix(hash(i+vec2(0,1)),hash(i+vec2(1,1)),f.x),f.y); }
float pc(vec2 q){
 float psu=clamp(uBoot*4.,0.,1.), mb=clamp(uBoot*3.-.35,0.,1.), cpu=clamp(uBoot*2.4-.7,0.,1.), gpu=clamp(uBoot*2.-1.,0.,1.);
 float L=uLoad, h=0.;
 float grain=.9+.16*vn(q*30.)+.08*vn(q*8.);
 float dc=sdBox(q-vec2(.25,.5),vec2(.25,.5),.018);
 h+=.17*psu*fill(abs(dc)-.0055,.007);
 h+=.045*mb*fill(dc,.02);
 h+=.05*psu*fill(abs(sdBox(q-vec2(.25,.5),vec2(.235,.485),.012))-.002,.004);
 float dmb=sdBox(q-vec2(.305,.63),vec2(.162,.318),.01);
 h+=mb*(.035+.075*exp(-length(q-vec2(.3,.72))/.12))*fill(dmb,.02);
 float dcp=sdBox(q-vec2(.315,.785),vec2(.058,.082),.012);
 h+=cpu*(.34+.3*L)*(1.15-.55*smoothstep(.72,.87,q.y))*(fill(dcp,.016)+.4*glow(dcp,.045));
 for(int i=0;i<4;i++){ h+=(.2+.08*L)*mb*fill(sdBox(q-vec2(.405+float(i)*.0135,.79),vec2(.004,.108),.002),.006); }
 h+=(.18+.1*L)*cpu*fill(sdBox(q-vec2(.315,.905),vec2(.075,.011),.004),.012);
 float dg=sdBox(q-vec2(.255,.515),vec2(.205,.052),.014);
 float along=1.-.42*abs(q.x-.27)/.22;
 float g=gpu*(.42+.66*L)*along;
 float fans=0.; for(int i=0;i<3;i++){ vec2 c=vec2(.132+float(i)*.121,.515); float r=length(q-c); fans=max(fans,fill(r-.036,.012)*(.55+.45*smoothstep(.0,.034,r))); }
 h+=g*(fill(dg,.016)*(1.-.62*fans)+.45*glow(dg,.06));
 h+=g*.8*fill(sdBox(q-vec2(.47,.515),vec2(.008,.046),.003),.012);
 h+=.2*psu*fill(sdBox(q-vec2(.17,.095),vec2(.12,.064),.01),.02);
 h+=.11*mb*fill(sdBox(q-vec2(.395,.09),vec2(.058,.04),.006),.015);
 for(int i=0;i<3;i++){ float r=length(q-vec2(.045,.3+float(i)*.2)); h+=.05*mb*fill(abs(r-.038)-.004,.006); }
 float re=length(q-vec2(.468,.83)); h+=(.14+.24*L)*cpu*fill(abs(re-.034)-.007,.01);
 return h*grain;
}
void main(){
 vec2 q=(gl_FragCoord.xy*uScale-uCase.xy)/uCase.z; q.x=.5-q.x;
 o=vec4(pc(q)*.5,0.,0.,1.);
}`,
  // Heat through 1 - e^(-gain t), posterised into bands via the Inferno LUT,
  // with fwidth() contour lines, grain and a vignette.
  display:
    H +
    `uniform sampler2D uTemp, uLut, uPc; uniform vec2 uRes; uniform float uTime, uFluid, uBands, uGain;
float hash(vec2 p){ return fract(sin(dot(p,vec2(12.9898,78.233)))*43758.5453); }
void main(){
 vec2 px=gl_FragCoord.xy;
 float t=2.*texture(uPc,vUv).x+texture(uTemp,vUv).x*uFluid;
 t=1.-exp(-uGain*t);
 float n=t*uBands, b=floor(n);
 vec3 col=texture(uLut,vec2((b+.5)/uBands,.5)).rgb;
 float f=fract(n), w=fwidth(n)+1e-4;
 float line=(1.-smoothstep(0.,1.4*w,f))*step(1.,b)*step(w,.45);
 col*=1.-.42*line;
 col+=(hash(px+floor(uTime*24.))-.5)*.035;
 vec2 v=px/uRes-.5; col*=1.-.42*dot(v,v);
 o=vec4(col,1.);
}`,
} as const;

export type ProgramName = keyof typeof FS;
