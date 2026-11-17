import { describe, expect, it } from 'vitest';
import { URL } from 'node:url';
import { parseHarnessOptions, requestDisposition } from './spending-browser-policy.mjs';

describe('spending acceptance origin and outbound boundary', () => {
  it('defaults to a local dev harness without an external origin', () => {
    expect(parseHarnessOptions([], {})).toMatchObject({ mode: 'local-dev', origin: null });
  });
  it('normalizes an explicit published origin and retains the evidence argument', () => {
    expect(parseHarnessOptions(['output', '--base-url', 'https://published.example/'], {})).toMatchObject({ mode: 'published-static', origin: 'https://published.example', evidence: 'output' });
    expect(parseHarnessOptions(['--origin', 'http://127.0.0.1:4190'], {})).toMatchObject({ origin: 'http://127.0.0.1:4190' });
    expect(parseHarnessOptions([], { SPENDING_BROWSER_BASE_URL: 'https://published.example' })).toMatchObject({ origin: 'https://published.example' });
  });
  it.each(['https://user:password@published.example', 'https://published.example/api', 'https://published.example/?token=secret', 'https://published.example/#secret', 'http://published.example', 'file:///tmp/index.html'])('rejects unsafe origin %s before starting a browser', (origin) => {
    expect(() => parseHarnessOptions(['--base-url', origin], {})).toThrow(/origin/i);
  });
  it('does not ambiguously combine a remote origin and a local static directory', () => {
    expect(() => parseHarnessOptions(['--base-url', 'https://published.example', '--static-dir', 'dist'], {})).toThrow(/combine/i);
  });
  const origin = 'https://published.example';
  const disposition = (pathname, type = 'fetch', method = 'GET', mode = 'published-static') => requestDisposition({ url: new URL(pathname, origin).href, type, method }, { origin, mode });
  it.each(['/api/auth/refresh', '/api/spending/config', '/pg/chat/completions', '/v1/messages', '/v1beta/models/example:generateContent'])('intercepts %s rather than forwarding it', (pathname) => {
    expect(disposition(pathname)).toBe('api');
  });
  it.each([
    ['/console/spending', 'document'], ['/console', 'document'], ['/login', 'document'],
    ['/assets/index-AbCd123.js', 'script'], ['/assets/index-AbCd123.css', 'stylesheet'], ['/brand/ztapi-mark.png', 'image'],
  ])('allows only the expected static resource %s (%s)', (pathname, type) => {
    expect(disposition(pathname, type)).toBe('static');
  });
  it.each([
    ['/assets/model.js', 'fetch', 'GET'], ['/assets/model.js', 'script', 'POST'], ['/unknown', 'fetch', 'GET'],
    ['/api%2Fspending', 'document', 'GET'], ['/src/api/client.ts', 'script', 'GET'], ['/assets/index.js?token=secret', 'script', 'GET'],
    ['https://tracker.example/image.png', 'image', 'GET'], ['https://tracker.example/v1/messages', 'fetch', 'POST'],
  ])('blocks non-static outbound traffic %s (%s %s)', (pathname, type, method) => {
    expect(disposition(pathname, type, method)).toBe('blocked');
  });
  it('permits dev source modules only in explicitly local dev mode', () => {
    expect(disposition('/src/main.tsx', 'script', 'GET', 'local-dev')).toBe('static');
    expect(disposition('/@vite/client', 'script', 'GET', 'local-dev')).toBe('static');
  });
});
