import { adminRequest } from '../../auth/admin-session.js';
import {
  normalizeModelHealth,
  loadModelHealth,
  recoverModelHealth,
  canRecoverModelHealth,
  healthErrorMessage,
  loadHealthWorkerStatus,
} from './model-health.js';

vi.mock('../../auth/admin-session.js', () => ({ adminRequest: vi.fn() }));

const snapshot = (patch = {}) => ({
  model_id: 17,
  enabled: true,
  observed: true,
  state: {
    ModelID: 17,
    Generation: 4,
    Open: true,
    ConsecutiveFailures: 2,
    CompletionSequence: 12,
  },
  window: {
    ValidSamples: 12,
    Failures: 2,
    WindowStart: 1000,
    WindowEnd: 87400,
  },
  events: [],
  incidents: [],
  outbox: [],
  coverage: [],
  ...patch,
});

describe('model health API adapter', () => {
  beforeEach(() => vi.clearAllMocks());

  it('keeps a tripped model tripped when instrumentation is disabled', () => {
    const health = normalizeModelHealth(snapshot({ enabled: false }));
    expect(health.status).toBe('tripped');
    expect(health.enabled).toBe(false);
    expect(health.state.generation).toBe(4);
    expect(health.window.validSamples).toBe(12);
  });

  it('distinguishes disabled, unobserved, and zero-valid-sample states from available', () => {
    expect(
      normalizeModelHealth(snapshot({ state: null, observed: false })).status,
    ).toBe('unknown');
    expect(
      normalizeModelHealth(
        snapshot({ state: null, enabled: false, observed: false }),
      ).status,
    ).toBe('disabled');
    expect(
      normalizeModelHealth(
        snapshot({
          state: { Generation: 4, Open: false },
          window: { ValidSamples: 0, Failures: 0 },
        }),
      ).window.failureRate,
    ).toBeNull();
    expect(
      normalizeModelHealth(
        snapshot({
          state: { Generation: 4, Open: false },
          window: { ValidSamples: 0, Failures: 0 },
        }),
      ).status,
    ).toBe('unknown');
    expect(
      normalizeModelHealth(
        snapshot({
          state: { Generation: 4, Open: false },
          window: { ValidSamples: 150, Failures: 3 },
        }),
      ).window.failureRate,
    ).toBe(2);
    expect(
      normalizeModelHealth(snapshot({ state: { Generation: 4, Open: false } }))
        .status,
    ).toBe('available');
  });

  it('normalizes both casing contracts and whitelists evidence without raw bodies or leases', () => {
    const health = normalizeModelHealth(
      snapshot({
        state: {
          model_id: 17,
          generation: 8,
          open: true,
          consecutive_failures: 2,
        },
        events: [
          {
            ID: 1,
            CompletionSequence: 3,
            Result: 'failure',
            Reason: 'upstream_5xx',
            HTTPStatus: 503,
            UpstreamRequestID: 'upstream-17',
            Outcome: JSON.stringify({
              FinishReasons: ['stop'],
              TerminalStatus: 'broken',
              response_body: 'PRIVATE_BODY',
              api_key: 'SECRET',
            }),
          },
        ],
        outbox: [
          {
            id: 1,
            kind: 'alert',
            status: 'pending',
            attempts: 2,
            lease_token: 'LEASE_SECRET',
            last_error: 'delivery_timeout',
          },
        ],
        incidents: [
          {
            ID: 4,
            Rule: 'consecutive_2',
            Generation: 8,
            RecoveryEvidence: 'review:17',
          },
        ],
        coverage: [
          {
            Stream: true,
            Source: 'real',
            ValidSamples: 3,
            UnknownSamples: 1,
            LastValidAt: 100,
          },
        ],
      }),
    );
    expect(health.state.generation).toBe(8);
    expect(health.events[0]).toMatchObject({
      sequence: 3,
      httpStatus: 503,
      upstreamRequestID: 'upstream-17',
      finishReasons: ['stop'],
      terminalStatus: 'broken',
    });
    expect(health.outbox[0]).toMatchObject({ status: 'pending', attempts: 2 });
    expect(health.coverage[0]).toMatchObject({
      stream: true,
      validSamples: 3,
      unknownSamples: 1,
    });
    expect(JSON.stringify(health)).not.toMatch(
      /PRIVATE_BODY|SECRET|lease_token|response_body/,
    );
  });

  it('uses the server latest-100 default without polling or scanning event history', async () => {
    adminRequest.mockResolvedValueOnce(
      snapshot({
        state: { Generation: 4, Open: true, CompletionSequence: 250 },
        events: [{ CompletionSequence: 250 }],
      }),
    );
    const health = await loadModelHealth(17);
    expect(adminRequest.mock.calls.map(([request]) => request.url)).toEqual([
      '/api/models/ztapi/17/health',
    ]);
    expect(health.events[0].sequence).toBe(250);
  });

  it('whitelists optional worker status and preserves unavailable budget as unknown', async () => {
    adminRequest.mockResolvedValue({
      enabled: false,
      worker_status: [
        {
          component: 'probe',
          code: 'probe_identity_missing',
          reference: 'SECRET_REFERENCE',
          updated_at: 123,
        },
      ],
      probe_budget: null,
    });
    const status = await loadHealthWorkerStatus();
    expect(status).toMatchObject({
      enabled: false,
      budget: null,
      workers: [
        { component: 'probe', code: 'probe_identity_missing', updatedAt: 123 },
      ],
    });
    expect(JSON.stringify(status)).not.toContain('SECRET_REFERENCE');
    expect(adminRequest).toHaveBeenCalledWith({
      method: 'GET',
      url: '/api/models/ztapi/health/status',
    });
  });

  it('rejects mismatched models and unsafe generation values', async () => {
    adminRequest.mockResolvedValue(snapshot({ model_id: 18 }));
    await expect(loadModelHealth(17)).rejects.toThrow();
    expect(canRecoverModelHealth(normalizeModelHealth(snapshot()), false)).toBe(
      false,
    );
    expect(
      canRecoverModelHealth(
        normalizeModelHealth(
          snapshot({
            state: { Open: true, Generation: Number.MAX_SAFE_INTEGER + 1 },
          }),
        ),
        true,
      ),
    ).toBe(false);
  });

  it.each([true, [4], '4', {}, 0].map((value) => [value]))(
    'fails closed for malformed or zero generation %j',
    (generation) => {
      const health = normalizeModelHealth(
        snapshot({
          state: { ModelID: 17, Open: true, Generation: generation },
        }),
      );
      expect(canRecoverModelHealth(health, true)).toBe(false);
    },
  );

  it('posts only expected generation and trimmed evidence, never publication or price changes', async () => {
    adminRequest.mockResolvedValue({
      model_id: 17,
      recovered: true,
      published: false,
    });
    const health = normalizeModelHealth(snapshot());
    await recoverModelHealth(17, health, {
      canWrite: true,
      evidence: '  review:123  ',
      confirmed: true,
    });
    expect(adminRequest).toHaveBeenCalledWith({
      method: 'POST',
      url: '/api/models/ztapi/17/health/recover',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ generation: 4, evidence: 'review:123' }),
    });
  });

  it.each([
    { canWrite: false, evidence: 'review:123', confirmed: true },
    { canWrite: true, evidence: '   ', confirmed: true },
    { canWrite: true, evidence: 'review:123', confirmed: false },
    { canWrite: true, evidence: '复'.repeat(1400), confirmed: true },
  ])('rejects invalid recovery locally: %j', async (options) => {
    await expect(
      recoverModelHealth(17, normalizeModelHealth(snapshot()), options),
    ).rejects.toThrow();
    expect(adminRequest).not.toHaveBeenCalled();
  });

  it('does not retry conflicts or display server error bodies', async () => {
    const error = Object.assign(new Error('PRIVATE_RAW_ERROR'), {
      status: 409,
    });
    adminRequest.mockRejectedValue(error);
    await expect(
      recoverModelHealth(17, normalizeModelHealth(snapshot()), {
        canWrite: true,
        evidence: 'review:123',
        confirmed: true,
      }),
    ).rejects.toBe(error);
    expect(adminRequest).toHaveBeenCalledTimes(1);
    expect(healthErrorMessage(error, 'recover')).toMatch(/代次.*刷新/);
    expect(healthErrorMessage({ status: 403 }, 'recover')).toMatch(/权限/);
    expect(healthErrorMessage({ status: 401 }, 'recover')).toMatch(/会话/);
    expect(
      healthErrorMessage({ status: 503, message: 'SECRET' }, 'recover'),
    ).toMatch(/未确认/);
    expect(
      healthErrorMessage({ status: 503, message: 'SECRET' }, 'recover'),
    ).not.toContain('SECRET');
  });
});
