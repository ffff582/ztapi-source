export function consoleReturnTo(search: string): string {
  const value = new URLSearchParams(search).get('redirect');
  if (!value || !value.startsWith('/') || value.startsWith('//') || value.includes('\\') || Array.from(value).some((character) => character.charCodeAt(0) < 32)) return '/console';
  try {
    const url = new URL(value, window.location.origin);
    if (url.origin !== window.location.origin || !(url.pathname === '/console' || url.pathname.startsWith('/console/'))) return '/console';
    return `${url.pathname}${url.search}${url.hash}`;
  } catch {
    return '/console';
  }
}
