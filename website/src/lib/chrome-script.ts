// The one inline script the site runs, before first paint and before React
// hydrates (src/app/layout.tsx puts it at the top of <body>). It does two
// small things that must not wait for the bundles:
//
// 1. Pause motion: the footer's switch (src/lib/motion/prefs.ts) saves
//    'paused' in localStorage; this puts html[data-motion="paused"] back on
//    before anything animates.
//
// 2. The nav's scrim: a plain, rAF-throttled scroll listener writes
//    html[data-nav] = "scrolled" from the first pixel of scroll, plus
//    "onlight" while a white-hot block (.surface-hot) sits under the nav
//    (src/styles/chrome.css draws both). It needs no GSAP, runs under reduced
//    motion, and survives client-side navigation, because
//    getElementsByClassName is live. window.__vosNav() re-checks on demand
//    (site/nav-sync.tsx calls it after a route change).
//
// <html> carries suppressHydrationWarning for these two attributes.
export const MOTION_KEY = 'vaporos-motion';

const script = `(function(){
var d=document,h=d.documentElement,w=window;
try{if(localStorage.getItem('${MOTION_KEY}')==='paused')h.setAttribute('data-motion','paused')}catch(e){}
var hot=d.getElementsByClassName('surface-hot'),nav=null,raf=0,last=null;
function run(){raf=0;
nav=nav&&nav.isConnected?nav:d.querySelector('.site-nav');
var s=(w.scrollY||h.scrollTop)>0?'scrolled':'',band=nav?nav.getBoundingClientRect().bottom:72;
for(var i=0;i<hot.length;i++){var r=hot[i].getBoundingClientRect();if(r.height>0&&r.top<band&&r.bottom>0){s+=(s?' ':'')+'onlight';break}}
if(s!==last){last=s;if(s)h.setAttribute('data-nav',s);else h.removeAttribute('data-nav')}}
function queue(){if(!raf)raf=requestAnimationFrame(run)}
w.__vosNav=queue;
w.addEventListener('scroll',queue,{passive:true});
w.addEventListener('resize',queue);
w.addEventListener('load',queue);
w.addEventListener('hashchange',queue);
w.addEventListener('pageshow',queue);
d.addEventListener('DOMContentLoaded',queue);
queue();
})();`;

/** The inline script's source, for <script dangerouslySetInnerHTML> in the root layout. */
export const chromeScript = script.replace(/\n/g, '');

declare global {
  interface Window {
    /** Re-check the nav's scrim now (set by the inline chrome script). */
    __vosNav?: () => void;
  }
}
