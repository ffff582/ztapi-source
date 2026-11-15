import { URL } from 'node:url';
import { parseArgs } from 'node:util';

export function parseHarnessOptions(args, environment) {
  const { values, positionals } = parseArgs({ args, allowPositionals: true, options: {
    'base-url': { type: 'string' }, origin: { type: 'string' }, 'static-dir': { type: 'string' },
    evidence: { type: 'string' }, help: { type: 'boolean' },
  } });
  if (positionals.length > 1 || values['base-url'] && values.origin) throw new Error('Use one origin and one evidence directory.');
  const supplied = values['base-url'] ?? values.origin ?? environment.SPENDING_BROWSER_BASE_URL;
  const staticDir = values['static-dir'];
  if (supplied && staticDir) throw new Error('Cannot combine a published origin and a local static directory.');
  let origin = null;
  if (supplied) {
    let url;
    try { url = new URL(supplied); } catch { throw new Error('Invalid acceptance origin.'); }
    const loopback = ['127.0.0.1', 'localhost', '[::1]'].includes(url.hostname);
    if (!['http:', 'https:'].includes(url.protocol) || !loopback && url.protocol !== 'https:' || url.username || url.password || url.pathname !== '/' || url.search || url.hash) throw new Error('Acceptance origin must be a credential-free HTTPS origin, or loopback HTTP.');
    origin = url.origin;
  }
  const width = environment.SPENDING_BROWSER_WIDTH;
  if (width && !['320', '390', '1440'].includes(width)) throw new Error('Acceptance width must be 320, 390 or 1440.');
  return { mode: origin ? 'published-static' : staticDir ? 'local-static' : 'local-dev', origin, staticDir, evidence: values.evidence ?? positionals[0], widths: width ? [Number(width)] : [320, 390, 1440], help: values.help === true };
}

export function requestDisposition({ url: raw, method, type }, { origin, mode }) {
  let url;
  try { url = new URL(raw); } catch { return 'blocked'; }
  if (url.origin !== origin) return 'blocked';
  if (/^\/(api|pg|v1|v1beta)(\/|$)/i.test(url.pathname)) return 'api';
  if (!['GET', 'HEAD'].includes(method)) return 'blocked';
  if (type === 'document') return !url.search && ['/', '/console', '/console/spending', '/console/workbench/text', '/login'].includes(url.pathname) ? 'static' : 'blocked';
  if (!['script', 'stylesheet', 'image', 'font', 'other'].includes(type)) return 'blocked';
  if (mode === 'local-dev' && (/^\/(src|node_modules|@vite|@fs|@id)\//.test(url.pathname) || url.pathname === '/@react-refresh')) return 'static';
  if (url.search) return 'blocked';
  if (/^\/assets\/[A-Za-z0-9_/-]+\.(js|css|woff2?|ttf|png|jpe?g|webp|avif|svg|ico)$/.test(url.pathname)) return type === 'other' ? 'blocked' : 'static';
  return /^\/brand\/[A-Za-z0-9_-]+\.(png|svg|ico)$/.test(url.pathname) && ['image', 'other'].includes(type) ? 'static' : 'blocked';
}
