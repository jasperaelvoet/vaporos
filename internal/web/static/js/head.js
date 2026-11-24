// head.js: classic, render-blocking, at most 1 kB (ARCH §7.3). Types the
// cross-document view transition (a lower data-order is going back).
(function () {
  'use strict';
  var d = document.documentElement;
  // None in a frame (the demo): Chromium 159 crashes on it.
  var framed = parent !== window;
  function skip(t) { t.ready.catch(Boolean); t.skipTransition(); }
  addEventListener('pageswap', function (e) {
    var t = e.viewTransition;
    if (t) if (framed) skip(t); else try { sessionStorage.setItem('vos-vt-from', d.dataset.order); } catch (err) { /* private mode */ }
  });
  addEventListener('pagereveal', function (e) {
    var t = e.viewTransition;
    if (!t) return;
    if (framed) return skip(t);
    try {
      var from = sessionStorage.getItem('vos-vt-from');
      sessionStorage.removeItem('vos-vt-from');
      if (from !== null) t.types.add(Number(d.dataset.order) < Number(from) ? 'back' : 'forward');
    } catch (err) { /* no storage, no type: the plain cross-fade runs */ }
  });
})();
