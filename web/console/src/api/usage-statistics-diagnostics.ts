import { DataContractError, parseUserLogPage, type UserLogItem } from './contracts';

export function serializeDiagnosticLogs(logs: readonly UserLogItem[]): string {
  // Rebuild every row: never serialize API envelopes, sessions, filters or raw objects.
  const items = logs.map((log) => ({
    request_id: log.request_id,
    model: log.model,
    status: log.status,
    timestamp: log.timestamp,
    latency: log.latency,
    ...(log.error_code === undefined ? {} : { error_code: log.error_code }),
    prompt_tokens: log.prompt_tokens,
    completion_tokens: log.completion_tokens,
    total_tokens: log.total_tokens,
    billed_amount: log.billed_amount,
  }));
  const safePage = parseUserLogPage({ page: 1, page_size: items.length, total: items.length, items });
  if (safePage.items.some((log) =>
    ![log.timestamp, log.latency, log.prompt_tokens, log.completion_tokens, log.total_tokens].every(Number.isSafeInteger) ||
    log.total_tokens !== log.prompt_tokens + log.completion_tokens)) throw new DataContractError();
  return JSON.stringify(safePage.items, null, 2);
}

export function downloadDiagnosticLogs(logs: readonly UserLogItem[], page: number) {
  const blob = new Blob([serializeDiagnosticLogs(logs)], { type: 'application/json' });
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement('a');
  anchor.href = url;
  anchor.download = `ztapi-diagnostics-page-${page}.json`;
  anchor.hidden = true;
  document.body.appendChild(anchor);
  try {
    anchor.click();
  } finally {
    anchor.remove();
    window.setTimeout(() => URL.revokeObjectURL(url), 0);
  }
}
