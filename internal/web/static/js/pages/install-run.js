// pages/install-run.js: the install once it runs (spec-cc-screens
// §15.6-§15.8), loaded by pages/install.js when an install starts or a
// reload finds one. Progress comes from install.progress events and a poll
// of /install/status every 2 s (polling is free on the ISO: nothing there
// counts web activity). The viewfinder heats with each step and is
// white-hot when done; after Restart now it cools to an ember, and the page
// follows the PC to the installed system's sign-in (?installed=1).

import { api, errorText, ping, url, useSetupCode } from '../core/api.js';
import { announce } from '../core/announce.js';
import { byId, h, setVar, sleep } from '../core/dom.js';
import { pause } from '../core/live.js';
import { elapsed, percent, phaseLabel } from '../fmt.js';
import { heat } from './entry-kit.js';
import { folders, forget, w } from './install-state.js';

const LONG_MS = 4 * 60e3;

// runner takes the wizard's steps from pages/install.js: ctx is {vf, go,
// askForCode, reachable, back}.
export function runner(ctx) {
  const { vf } = ctx;
  const bar = byId('progress-bar');
  const big = byId('vf-big');
  let polling = false;
  let step = '';

  const needle = (text) => {
    byId('vf-needle').textContent = text;
  };
  const guard = (e) => {
    e.preventDefault();
    e.returnValue = '';
  };

  // plates: the drive being written, and one more fact (the version, or
  // the time since the restart).
  function plates(second) {
    const d = w.disk;
    byId('vf-plates').hidden = !d;
    if (d) byId('vf-disk').textContent = `${d.path.replace(/^\/dev\//, '')} · ${d.model || 'drive'}`;
    const t = byId('vf-time');
    t.hidden = !second;
    t.textContent = second || '';
  }

  function start(status) {
    w.finished = false;
    step = '';
    byId('progress-failed').hidden = true;
    byId('progress-retry').hidden = true;
    bar.dataset.indeterminate = '';
    bar.removeAttribute('aria-valuenow');
    bar.removeAttribute('aria-valuetext');
    setVar(bar, '--progress', 0);
    byId('progress-step').textContent = phaseLabel('probe', 'install');
    byId('progress-message').textContent = '';
    big.textContent = '0%';
    ctx.go('progress');
    plates('');
    big.hidden = false;
    needle('installing');
    heat(vf, 'probe');
    addEventListener('beforeunload', guard);
    if (status) apply(status);
    poll();
  }

  async function poll() {
    if (polling) return;
    polling = true;
    while (!w.finished) {
      try {
        apply(await api('GET', '/install/status'));
      } catch (err) {
        if (ctx.askForCode(err)) break;
        /* otherwise a blip: the next poll tries again */
      }
      if (!w.finished) await sleep(2000);
    }
    polling = false;
  }

  // apply shows one report: an install.progress event or a status. The
  // step's words come from T4; the server's own message goes under them as
  // it is (it may say "signed", which CI looks for).
  function apply(s) {
    if (!s || w.finished || w.step !== 'progress' || s.state === 'idle') return;
    if (s.step && s.step !== step) {
      step = s.step;
      byId('progress-step').textContent = phaseLabel(step, 'install');
      if (s.state === 'running') {
        heat(vf, step);
        announce(phaseLabel(step, 'install'));
      }
    }
    if (s.percent != null) {
      const pct = percent(s.percent);
      delete bar.dataset.indeterminate;
      bar.setAttribute('aria-valuenow', String(pct));
      bar.setAttribute('aria-valuetext', `${phaseLabel(step, 'install')}, ${pct} percent`);
      setVar(bar, '--progress', pct / 100);
      big.textContent = `${pct}%`;
    }
    if (s.message) byId('progress-message').textContent = s.message;
    if (s.state === 'done') finish();
    else if (s.state === 'failed' && 'error' in s) fail(s.error || s.message);
    else if (s.state === 'failed') failed(s);
  }

  // failed: the event says the install stopped but carries no error (only
  // the status does); ask for it, so the server's own words show.
  async function failed(s) {
    let st = s;
    try {
      st = await api('GET', '/install/status');
    } catch {
      /* show what the event said */
    }
    if (!w.finished) fail(st.error || s.message);
  }

  function finish() {
    w.finished = true;
    removeEventListener('beforeunload', guard);
    setVar(bar, '--progress', 1);
    const paths = folders();
    byId('done-libraries').hidden = paths.length === 0;
    byId('done-library-paths').replaceChildren(...paths.flatMap((f, i) => [
      i ? (i === paths.length - 1 ? ' and ' : ', ') : '',
      h('span', { class: 'mono', text: f }),
    ]));
    ctx.go('done');
    big.hidden = true;
    plates(w.probe?.version ? `VaporOS ${w.probe.version}` : '');
    needle('installed');
    heat(vf, 'done');
  }

  function fail(msg) {
    w.finished = true;
    removeEventListener('beforeunload', guard);
    byId('progress-error').textContent = msg || 'No details were reported.';
    byId('progress-failed').hidden = false;
    byId('progress-retry').hidden = false;
    heat(vf, 'failed');
    needle('stopped');
    announce(`The install didn't finish. ${msg || ''}`.trim(), { assertive: true });
    byId('retry-btn').focus();
  }

  byId('retry-btn').addEventListener('click', () => {
    // After a reload the password is gone: walk the steps again to type it.
    if (!w.disk || (w.mode === 'erase' && !w.password)) ctx.go('disk');
    else ctx.back();
  });

  byId('reboot-btn').addEventListener('click', async (e) => {
    const btn = e.currentTarget;
    if (btn.getAttribute('aria-busy') === 'true') return;
    btn.setAttribute('aria-busy', 'true');
    byId('reboot-error').textContent = '';
    try {
      await api('POST', '/install/reboot', {});
    } catch (err) {
      btn.removeAttribute('aria-busy');
      if (!ctx.askForCode(err)) byId('reboot-error').textContent = await errorText(err);
      return;
    }
    restart();
  });

  // restart follows the PC through its restart. The installed system
  // usually gets the same address (DHCP by MAC), so this origin answering
  // with mode "os" is the common case. Otherwise http://<name>.local is
  // tried once the installer has gone away (before that the name may be
  // another PC's). A repair keeps the name the probe read from the drive.
  async function restart() {
    pause();
    ctx.go('restart');
    needle('restarting');
    const pane = byId('restart-status').closest('.wiz-pane');
    const name = w.hostname || w.disk?.hostname || '';
    const target = name ? `http://${name}.local/` : '';
    if (target) {
      const a = byId('restart-link');
      a.href = target;
      a.textContent = `http://${name}.local`;
    }
    byId('restart-or').hidden = !target;
    const ip = (w.probe?.ips || []).find((x) => x && x !== location.hostname);
    if (ip) {
      const base = ip.includes(':') ? `http://[${ip}]/` : `http://${ip}/`;
      const a = byId('restart-ip');
      a.href = base;
      a.textContent = base.replace(/\/$/, '');
    }
    byId('restart-alt').hidden = !ip;
    const status = (text) => {
      byId('restart-status').textContent = text;
    };
    const since = Date.now();
    const tick = () => plates(elapsed((Date.now() - since) / 1000));
    tick();
    const timer = setInterval(tick, 1000);
    const land = async (to) => {
      clearInterval(timer);
      status('VaporOS is up. Opening it…');
      pane.dataset.phase = 'back';
      needle('starting');
      heat(vf, 'back');
      forget();
      useSetupCode('');
      await sleep(1200);
      location.replace(to);
    };
    let down = false;
    let long = false;
    for (;;) {
      await sleep(2500);
      const here = await ping();
      if (here && here.mode === 'os') {
        await land(url('/login?installed=1'));
        return;
      }
      if (!here && !down) {
        down = true;
        heat(vf, 'down');
        status('VaporOS is starting from the drive. This takes about a minute.');
      }
      if (target && (down || Date.now() - since > 30e3) && (await ctx.reachable(target))) {
        await land(`${target}login?installed=1`);
        return;
      }
      if (!long && Date.now() - since > LONG_MS) {
        long = true;
        status("This is taking longer than usual. Check that the USB stick is out and the PC restarted, then open the address shown on the PC's screen or in your router's list of devices.");
      }
    }
  }

  return { start, apply, finish };
}
