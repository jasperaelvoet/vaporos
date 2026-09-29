// core/setup-code.js: the setup code on the installer and first-run pages.
// It comes from the prefilled field (the server copies ?code= from the QR
// code into it), the URL or this tab's storage, and is removed from the
// address bar so it is not shared by accident.

import { normalizeCode } from '../validate.js';
import { storedSetupCode, useSetupCode } from './api.js';

export function initSetupCode(field) {
  const params = new URLSearchParams(location.search);
  const code = normalizeCode((field && field.value) || params.get('code') || storedSetupCode());
  if (params.has('code')) {
    params.delete('code');
    const q = params.toString();
    history.replaceState(null, '', location.pathname + (q ? `?${q}` : '') + location.hash);
  }
  if (field) {
    field.value = code;
    field.addEventListener('blur', () => {
      field.value = normalizeCode(field.value);
    });
  }
  useSetupCode(code);
  return code;
}
