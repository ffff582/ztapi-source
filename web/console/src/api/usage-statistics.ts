import { apiClient, getAuthSession } from './client';
import { DataContractError, parseUserLogPage, type UserLogItem } from './contracts';

export type UsageRange = 'today' | 'yesterday' | '7d' | '30d';

export function usageRangeTimestamps(range: UsageRange, now = new Date()) {
  const today = new Date(now);
  today.setHours(0, 0, 0, 0);
  const start = new Date(today);
  start.setDate(start.getDate() - (range === 'yesterday' ? 1 : range === '7d' ? 6 : range === '30d' ? 29 : 0));
  return {
    start: Math.floor(start.getTime() / 1000),
    // Both timestamp filters in GetUserLogs are inclusive.
    end: range === 'yesterday' ? today.getTime() / 1000 - 1 : Math.floor(now.getTime() / 1000),
  };
}

export interface UsageStatistics {
  requestCount: number;
  settledTokens: number;
  settledCharge: number;
  recent: UserLogItem[];
}

export async function loadUsageStatistics(
  timestamps: { start: number; end: number },
  signal: AbortSignal,
): Promise<UsageStatistics> {
  const userID = getAuthSession()?.user.id;
  const seen = new Set<string>();
  let firstPage = '';
  let pageNumber = 1;
  let pageSize = 100;
  let expectedTotal: number | undefined;
  let consumed = 0;
  let compensation = 0;
  const statistics: UsageStatistics = { requestCount: 0, settledTokens: 0, settledCharge: 0, recent: [] };

  async function readPage(number: number) {
    signal.throwIfAborted();
    if (getAuthSession()?.user.id !== userID) throw new DataContractError();
    const query = new URLSearchParams({ p: String(number), page_size: String(pageSize),
      start_timestamp: String(timestamps.start), end_timestamp: String(timestamps.end) });
    const page = parseUserLogPage(await apiClient.get<unknown>(`/log/self?${query}`, { signal }));
    signal.throwIfAborted();
    if (getAuthSession()?.user.id !== userID) throw new DataContractError();
    return page;
  }

  do {
    const page = await readPage(pageNumber);
    if (page.page !== pageNumber || page.page_size <= 0 || page.page_size > pageSize ||
      (expectedTotal !== undefined && (page.total !== expectedTotal || page.page_size !== pageSize))) {
      throw new DataContractError();
    }
    // GetUserLogs uses logSearchCountLimit=10000. Never trust a total at that boundary.
    if (page.total >= 10000) throw new DataContractError();
    if (pageNumber === 1) firstPage = JSON.stringify(page);
    expectedTotal = page.total;
    pageSize = page.page_size;
    if (page.items.length !== Math.min(pageSize, page.total - consumed)) throw new DataContractError();
    for (const log of page.items) {
      const fingerprint = JSON.stringify(log);
      if (seen.has(fingerprint) || log.timestamp < timestamps.start || log.timestamp > timestamps.end ||
        ![log.timestamp, log.prompt_tokens, log.completion_tokens, log.total_tokens].every(Number.isSafeInteger) ||
        log.total_tokens !== log.prompt_tokens + log.completion_tokens) {
        throw new DataContractError();
      }
      seen.add(fingerprint);
      if (log.status === 'info') continue;
      statistics.requestCount += 1;
      if (statistics.recent.length < 5) statistics.recent.push(log);
      if (log.status === 'success') {
        statistics.settledTokens += log.total_tokens;
        const amount = log.billed_amount - compensation;
        const sum = statistics.settledCharge + amount;
        compensation = (sum - statistics.settledCharge) - amount;
        statistics.settledCharge = sum;
        if (!Number.isSafeInteger(statistics.settledTokens) || !Number.isFinite(sum)) throw new DataContractError();
      }
    }
    consumed += page.items.length;
    pageNumber += 1;
  } while (consumed < expectedTotal);

  // Offset pagination has no snapshot token. Detect changed counts and shifted leading rows.
  if (JSON.stringify(await readPage(1)) !== firstPage) throw new DataContractError();
  return statistics;
}
