// head.js: classic, render-blocking, at most 1 kB (ARCH §7.3). Types the
// cross-document view transition (a lower data-order is going back).
(function () {
  'use strict';
  var d = document.documentElement;
  addEventListener('pageswap', function (e) {
    if (e.viewTransition) try { sessionStorage.setItem('vos-vt-from', d.dataset.order); } catch (err) { /* private mode */ }
  });
  addEventListener('pagereveal', function (e) {
    if (!e.viewTransition) return;
    try {
      var from = sessionStorage.getItem('vos-vt-from');
      sessionStorage.removeItem('vos-vt-from');
      if (from !== null) e.viewTransition.types.add(Number(d.dataset.order) < Number(from) ? 'back' : 'forward');
    } catch (err) { /* no storage, no type: the plain cross-fade runs */ }
  });
})();
