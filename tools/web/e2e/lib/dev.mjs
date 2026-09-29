// Specs drive the dev server through its loopback-only controls
// (internal/web/devserver_test.go): /__dev/preset, event, down, latency,
// script. server is the flow's t.server.
export async function dev(server, path, body) {
  const r = await fetch(`http://127.0.0.1:${server.port}/__dev/${path}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body ?? {}),
  });
  if (!r.ok) throw new Error(`/__dev/${path}: ${r.status} ${await r.text()}`);
  return r.json().catch(() => ({}));
}

// closed waits until a <dialog> is closed (a closed one is never visible,
// so a locator cannot wait for it).
export const closed = (page, id, timeout = 5000) => page.waitForFunction((i) => !document.getElementById(i).open, id, { timeout });
