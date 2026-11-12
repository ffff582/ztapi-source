import { describe, expect, it, vi } from 'vitest';
import { type UserLogItem } from './contracts';
import { serializeDiagnosticLogs } from './usage-statistics-diagnostics';

const log: UserLogItem = { request_id: 'req-public', model: 'zt-public-model', status: 'success', timestamp: 1500,
  latency: 2, prompt_tokens: 12, completion_tokens: 3, total_tokens: 15, billed_amount: 0.000002 };

describe('diagnostic field allowlist', () => {
  it('reconstructs a flat allowlisted record even if the input gains secrets, nested metadata or a toJSON hook', () => {
    const toJSON = vi.fn(() => ({ credentials: 'synthetic-never-export' }));
    const input = { ...log, authorization: 'synthetic-never-export', api_key: 'synthetic-never-export',
      cookie: 'synthetic-never-export', prompt: 'customer-input-never-export',
      upstream_request_id: 'private-upstream-never-export', raw_response: { text: 'raw-never-export' },
      billing: { provider_token: 'nested-never-export' }, toJSON };
    const exported = serializeDiagnosticLogs([input]);
    expect(JSON.parse(exported)).toEqual([log]);
    expect(toJSON).not.toHaveBeenCalled();
    expect(exported).not.toContain('never-export');
    expect(Object.keys(JSON.parse(exported)[0]).sort()).toEqual([
      'billed_amount', 'completion_tokens', 'latency', 'model', 'prompt_tokens', 'request_id',
      'status', 'timestamp', 'total_tokens',
    ]);
  });

  it('preserves a public error code without adding a raw error message', () => {
    const input = { ...log, status: 'error' as const, billed_amount: 0,
      error_code: 'model_unavailable', error: { message: 'private-upstream-never-export' } };
    expect(JSON.parse(serializeDiagnosticLogs([input]))).toEqual([{ ...log, status: 'error', billed_amount: 0, error_code: 'model_unavailable' }]);
  });

  it.each([
    ['non-code error details', { error_code: 'Bearer synthetic-never-export' }],
    ['unsafe usage integer', { prompt_tokens: Number.MAX_SAFE_INTEGER + 1, completion_tokens: 0, total_tokens: Number.MAX_SAFE_INTEGER + 1 }],
    ['inconsistent token dimensions', { total_tokens: 99 }],
    ['non-finite bill', { billed_amount: Infinity }],
    ['negative bill', { billed_amount: -1 }],
  ])('refuses %s rather than exporting invalid diagnostics', (_name, override) => {
    expect(() => serializeDiagnosticLogs([{ ...log, ...override }])).toThrow();
  });
});
