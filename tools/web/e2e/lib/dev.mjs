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

// armed returns a waiter a flow sets up before the action it waits for. If
// the action throws first, the flow fails there, and the waiter's later
// rejection is expected: it is handled here, so it never surfaces as an
// unhandled rejection. Await the returned promise as usual.
export function armed(promise) {
  promise.catch(() => {});
  return promise;
}

// request waits for the page's next API request with this method and path
// (under /api/v1), armed.
export const request = (page, method, path) =>
  armed(page.waitForRequest((r) => r.method() === method && new URL(r.url()).pathname === `/api/v1${path}`));

// write waits for the next API write and returns its JSON body, armed.
export const write = (page, method, path) => armed(request(page, method, path).then((r) => r.postDataJSON()));
