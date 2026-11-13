import { afterEach, describe, expect, it, vi } from 'vitest';
import { apiClient } from './client';

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('console API requests', () => {
  // The server once answered an unknown route with a week-long cache. Browsers
  // kept serving that 404 after the route was added, so the page never asked
  // again; account data must always come from the server.
  it('never reads an account response from the browser cache', async () => {
    const requests: RequestInit[] = [];
    const fetchMock = vi.fn(async (...args: [RequestInfo | URL, RequestInit?]) => {
      requests.push(args[1] ?? {});
      return new Response(JSON.stringify({ success: true, data: { items: [] } }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      });
    });
    vi.stubGlobal('fetch', fetchMock);

    await apiClient.get('/user/self/pending-settlements?after_id=0&limit=21');

    expect(requests).toHaveLength(1);
    expect(requests[0].cache).toBe('no-store');
  });
});
