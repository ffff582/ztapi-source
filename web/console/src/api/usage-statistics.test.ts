import { afterEach, describe, expect, it, vi } from 'vitest';
import { clearAuthSession, setAuthSession } from './client';
import { loadUsageStatistics, usageRangeTimestamps } from './usage-statistics';

const timestamps = { start: 1000, end: 2000 };

function item(index: number, overrides: Record<string, unknown> = {}) {
  return { timestamp: 1500, request_id: `req-${index}`, model: 'zt-public-model', status: 'success',
    latency: 1, prompt_tokens: 10, completion_tokens: 5, total_tokens: 15, billed_amount: 0.1, ...overrides };
}

function mockPages(makePage: (page: number) => unknown) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = new URL(String(input), 'http://ztapi.test');
    expect(url.pathname).toBe('/api/log/self');
    expect(url.searchParams.get('start_timestamp')).toBe('1000');
    expect(url.searchParams.get('end_timestamp')).toBe('2000');
    return new Response(JSON.stringify({ success: true, data: makePage(Number(url.searchParams.get('p'))) }),
      { headers: { 'Content-Type': 'application/json' } });
  });
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

afterEach(() => { clearAuthSession(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

describe('whole-period statistics', () => {
  it.each([
    ['today', new Date(2026, 2, 1), new Date(2026, 2, 1, 12, 34, 56)],
    ['yesterday', new Date(2026, 1, 28), new Date(2026, 1, 28, 23, 59, 59)],
    ['7d', new Date(2026, 1, 23), new Date(2026, 2, 1, 12, 34, 56)],
    ['30d', new Date(2026, 0, 31), new Date(2026, 2, 1, 12, 34, 56)],
  ] as const)('uses inclusive local-calendar boundaries for %s', (range, start, end) => {
    expect(usageRangeTimestamps(range, new Date(2026, 2, 1, 12, 34, 56))).toEqual({
      start: start.getTime() / 1000, end: end.getTime() / 1000,
    });
  });

  it('reads every page, preserves zero-charge usage and counts request records, not information logs', async () => {
    const logs = [item(1), item(2, { status: 'info', total_tokens: 900, prompt_tokens: 900, completion_tokens: 0, billed_amount: 999 }),
      item(3, { status: 'error', billed_amount: 500 }), item(4, { billed_amount: 0 })];
    mockPages((page) => ({ page, page_size: 2, total: 4, items: logs.slice((page - 1) * 2, page * 2) }));
    const result = await loadUsageStatistics(timestamps, new AbortController().signal);
    expect(result).toMatchObject({ requestCount: 3, settledTokens: 30, settledCharge: 0.1 });
    expect(result.recent.map((log) => log.request_id)).toEqual(['req-1', 'req-3', 'req-4']);
  });

  it('returns verified zero totals for an empty period', async () => {
    mockPages((page) => ({ page, page_size: 100, total: 0, items: [] }));
    expect(await loadUsageStatistics(timestamps, new AbortController().signal)).toEqual({
      requestCount: 0, settledTokens: 0, settledCharge: 0, recent: [],
    });
  });

  it('refuses the server search-count ceiling rather than displaying a potentially capped total', async () => {
    const fetchMock = mockPages((page) => ({ page, page_size: 100, total: 10000,
      items: Array.from({ length: 100 }, (_, index) => item((page - 1) * 100 + index)) }));
    await expect(loadUsageStatistics(timestamps, new AbortController().signal)).rejects.toThrow();
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('refuses duplicate page records rather than silently double-counting', async () => {
    mockPages((page) => ({ page, page_size: 2, total: 4, items: [item(1), item(2)] }));
    await expect(loadUsageStatistics(timestamps, new AbortController().signal)).rejects.toThrow();
  });

  it('revalidates the first page to detect inserts or deletion during offset pagination', async () => {
    let firstPageReads = 0;
    mockPages((page) => {
      if (page === 1) firstPageReads += 1;
      return { page, page_size: 2, total: 3,
        items: page === 2 ? [item(3)] : firstPageReads === 1 ? [item(1), item(2)] : [item(99), item(1)] };
    });
    await expect(loadUsageStatistics(timestamps, new AbortController().signal)).rejects.toThrow();
  });

  it.each([
    ['out-of-range timestamp', { timestamp: 2001 }],
    ['inconsistent token dimensions', { total_tokens: 100 }],
    ['unsafe integer token usage', { prompt_tokens: Number.MAX_SAFE_INTEGER + 1, total_tokens: Number.MAX_SAFE_INTEGER + 1, completion_tokens: 0 }],
  ])('refuses %s instead of publishing unreliable totals', async (_name, overrides) => {
    mockPages((page) => ({ page, page_size: 100, total: 1, items: [item(1, overrides)] }));
    await expect(loadUsageStatistics(timestamps, new AbortController().signal)).rejects.toThrow();
  });

  it('rejects unsafe aggregate token arithmetic', async () => {
    mockPages((page) => ({ page, page_size: 100, total: 2,
      items: [item(1, { prompt_tokens: Number.MAX_SAFE_INTEGER, completion_tokens: 0, total_tokens: Number.MAX_SAFE_INTEGER }), item(2)] }));
    await expect(loadUsageStatistics(timestamps, new AbortController().signal)).rejects.toThrow();
  });

  it('rejects overflowing charge arithmetic', async () => {
    mockPages((page) => ({ page, page_size: 100, total: 2,
      items: [item(1, { billed_amount: 1e308 }), item(2, { billed_amount: 1e308 })] }));
    await expect(loadUsageStatistics(timestamps, new AbortController().signal)).rejects.toThrow();
  });

  it.each([
    ['short intermediate page', { page: 1, page_size: 2, total: 3, items: [item(1)] }],
    ['wrong page number', { page: 2, page_size: 2, total: 2, items: [item(1), item(2)] }],
    ['zero page size', { page: 1, page_size: 0, total: 0, items: [] }],
  ])('rejects %s', async (_name, page) => {
    mockPages(() => page);
    await expect(loadUsageStatistics(timestamps, new AbortController().signal)).rejects.toThrow();
  });

  it('rejects a changing total without returning a partial aggregate', async () => {
    mockPages((page) => ({ page, page_size: 2, total: page === 1 ? 3 : 4,
      items: page === 1 ? [item(1), item(2)] : [item(3), item(4)] }));
    await expect(loadUsageStatistics(timestamps, new AbortController().signal)).rejects.toThrow();
  });

  it('does not continue pagination after cancellation', async () => {
    const controller = new AbortController();
    const fetchMock = mockPages((page) => {
      controller.abort();
      return { page, page_size: 2, total: 3, items: [item(1), item(2)] };
    });
    await expect(loadUsageStatistics(timestamps, controller.signal)).rejects.toThrow();
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('does not mix pages from different signed-in accounts', async () => {
    setAuthSession({ access_token: 'synthetic-a', expires_in: 900,
      user: { id: 1, username: 'a', role: 1, group: 'default' } });
    const fetchMock = mockPages((page) => {
      setAuthSession({ access_token: 'synthetic-b', expires_in: 900,
        user: { id: 2, username: 'b', role: 1, group: 'default' } });
      return { page, page_size: 2, total: 3, items: page === 1 ? [item(1), item(2)] : [item(3)] };
    });
    await expect(loadUsageStatistics(timestamps, new AbortController().signal)).rejects.toThrow();
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});
