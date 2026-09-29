// First-run setup on an installed system without an admin password (a CLI
// install): prove presence with the setup code, then choose the password.
import {
  boot, api, byId, onSubmit, showError, session, initSetupCode, useSetupCode, normalizeCode,
  passwordError, invalid, ApiError,
} from '../lib.js';

async function main() {
  const me = await boot('setup', { auth: false });
  if (!me.needs_setup) {
    location.replace(me.authenticated ? '/' : '/login');
    return;
  }
  initSetupCode();
  onSubmit(byId('setup-form'), async () => {
    const p1 = byId('setup-password');
    const p2 = byId('setup-password2');
    const err = passwordError(p1.value, p2.value);
    if (err) return invalid(p1.value.length < 8 ? p1 : p2, err);
    const field = byId('setup-code');
    if (field) useSetupCode(normalizeCode(field.value));
    showError('setup-error', '');
    try {
      const r = await api('POST', '/auth/setup', { password: p1.value }, { quiet401: true });
      session.csrf = r.csrf || '';
      useSetupCode(''); // done with it; don't keep it in this tab
      location.replace('/');
    } catch (e) {
      if (!(e instanceof ApiError)) throw e;
      if (e.status === 409) {
        // Someone finished setup meanwhile: the password exists now.
        location.replace('/login');
        return;
      }
      showError('setup-error', e.status === 403
        ? "That setup code isn't right. Use the code on the screen connected to VaporOS."
        : e.message);
    }
    return undefined;
  });
}

main();
