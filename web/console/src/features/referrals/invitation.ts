const codePattern = /^[A-Za-z0-9_-]{1,128}$/;

export function captureInvitation(search: string): string | undefined {
  const values = new URLSearchParams(search).getAll('invite');
  if (values.length > 0) {
    const code = values.length === 1 && codePattern.test(values[0]) ? values[0] : undefined;
    try {
      if (code) sessionStorage.setItem('invite_code', code);
      else sessionStorage.removeItem('invite_code');
    } catch { /* Registration also works without browser storage. */ }
    return code;
  }
  try {
    const code = sessionStorage.getItem('invite_code');
    return code && codePattern.test(code) ? code : undefined;
  } catch {
    return undefined;
  }
}

export function clearInvitation() {
  try { sessionStorage.removeItem('invite_code'); } catch { /* Storage may be blocked. */ }
}
