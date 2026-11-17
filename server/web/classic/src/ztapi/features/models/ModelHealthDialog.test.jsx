import React from 'react';
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react';
import { adminRequest } from '../../auth/admin-session.js';
import ModelHealthDialog from './ModelHealthDialog.jsx';
import AdminModelsPage from './AdminModelsPage.jsx';

vi.mock('../../auth/admin-session.js', () => ({ adminRequest: vi.fn() }));

const model = {
  id: 17,
  source_model: 'upstream-example',
  public_name: 'zt-example',
  publication_blockers: [],
  enabled_groups: [],
};
const health = {
  model_id: 17,
  enabled: true,
  observed: true,
  state: {
    ModelID: 17,
    Generation: 4,
    Open: true,
    ConsecutiveFailures: 2,
    CompletionSequence: 2,
  },
  window: {
    ValidSamples: 150,
    Failures: 3,
    WindowStart: 1788600000,
    WindowEnd: 1788686400,
  },
  coverage: [
    {
      Stream: false,
      Source: 'real',
      ValidSamples: 12,
      UnknownSamples: 1,
      LastValidAt: 1788686400,
    },
  ],
  events: [
    {
      ID: 2,
      CompletionSequence: 2,
      Result: 'failure',
      Reason: 'upstream_5xx',
      HTTPStatus: 503,
      UpstreamRequestID: 'upstream-456',
      RequestID: 'request-123',
      CompletedAt: 1788686400,
      Source: 'real',
      Outcome: JSON.stringify({
        FinishReasons: ['length'],
        TerminalStatus: 'interrupted',
        response_body: 'PRIVATE_BODY',
      }),
    },
  ],
  incidents: [
    {
      ID: 1,
      Generation: 4,
      Rule: 'consecutive_2',
      OpenedAt: 1788686400,
      Failures: 2,
      ValidSamples: 2,
    },
  ],
  outbox: [
    {
      id: 1,
      kind: 'alert',
      incident_id: 1,
      status: 'pending',
      attempts: 2,
      last_error: 'delivery_timeout',
      lease_token: 'LEASE_SECRET',
    },
  ],
};

function renderDialog(props = {}) {
  return render(
    <ModelHealthDialog
      model={model}
      canWrite
      onClose={vi.fn()}
      onRecovered={vi.fn()}
      {...props}
    />,
  );
}

describe('ModelHealthDialog', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    adminRequest.mockResolvedValue(health);
  });
  afterEach(cleanup);

  it('shows status and valid window without interpreting a closed or empty breaker as verified health', async () => {
    renderDialog();
    expect(await screen.findByText('已熔断')).toBeVisible();
    expect(screen.getByText('2.00%')).toBeVisible();
    expect(screen.getByText('150')).toBeVisible();
    expect(screen.getByText('实时采集已启用')).toBeVisible();
    expect(screen.getByText('非流式')).toBeVisible();
    expect(screen.getByText('1')).toBeVisible();
  });

  it('renders safe finish/error/request evidence and independent incident delivery status', async () => {
    renderDialog();
    await screen.findByText('已熔断');
    fireEvent.click(screen.getByRole('tab', { name: '请求事件' }));
    expect(screen.getByText('length')).toBeVisible();
    expect(screen.getByText('upstream-456')).toBeVisible();
    expect(screen.getByText('upstream_5xx')).toBeVisible();
    expect(screen.getByText('interrupted')).toBeVisible();
    expect(screen.queryByText('PRIVATE_BODY')).toBeNull();
    fireEvent.click(screen.getByRole('tab', { name: '事故与通知' }));
    expect(screen.getByText('连续 2 次有效失败')).toBeVisible();
    expect(screen.getByText('等待发送')).toBeVisible();
    expect(screen.getByText('delivery_timeout')).toBeVisible();
    expect(screen.queryByText('LEASE_SECRET')).toBeNull();
  });

  it('allows read-only inspection but never exposes the recovery form without model.write', async () => {
    renderDialog({ canWrite: false });
    await screen.findByText('已熔断');
    expect(screen.queryByRole('textbox', { name: /复验记录/ })).toBeNull();
    expect(screen.queryByRole('button', { name: /解除熔断/ })).toBeNull();
    expect(screen.getByText('只读权限')).toBeVisible();
  });

  it('distinguishes pending catalog unpublish from HTTP alert delivery', async () => {
    adminRequest.mockResolvedValue({
      ...health,
      outbox: [
        { id: 1, kind: 'unpublish', status: 'pending', attempts: 0 },
        { id: 2, kind: 'alert', status: 'done', attempts: 1 },
      ],
    });
    renderDialog();
    await screen.findByText('已熔断');
    fireEvent.click(screen.getByRole('tab', { name: '事故与通知' }));
    expect(screen.getByText('等待下架')).toBeVisible();
    expect(screen.getByText('投递任务已完成')).toBeVisible();
  });

  it('requires evidence and explicit confirmation then recovers without publishing', async () => {
    const onRecovered = vi.fn();
    adminRequest.mockImplementation(({ method }) =>
      Promise.resolve(
        method === 'POST'
          ? { model_id: 17, recovered: true, published: false }
          : health,
      ),
    );
    renderDialog({ onRecovered });
    await screen.findByText('已熔断');
    const submit = screen.getByRole('button', { name: '解除熔断（保持下架）' });
    expect(submit).toBeDisabled();
    fireEvent.change(screen.getByRole('textbox', { name: /复验记录/ }), {
      target: { value: 'review:17' },
    });
    expect(submit).toBeDisabled();
    fireEvent.click(
      screen.getByRole('checkbox', { name: /确认解除熔断但保持下架/ }),
    );
    fireEvent.click(submit);
    await waitFor(() => expect(onRecovered).toHaveBeenCalledTimes(1));
    const writes = adminRequest.mock.calls
      .map(([request]) => request)
      .filter((request) => request.method !== 'GET');
    expect(writes).toHaveLength(1);
    expect(JSON.parse(writes[0].body)).toEqual({
      generation: 4,
      evidence: 'review:17',
    });
    expect(await screen.findByText('熔断已解除，模型保持下架。')).toBeVisible();
  });

  it('blocks conflict resubmission until a refresh and new confirmation use the new generation', async () => {
    let generation = 4;
    adminRequest.mockImplementation(({ method }) => {
      if (method === 'POST') {
        generation = 5;
        return Promise.reject({ status: 409 });
      }
      return Promise.resolve({
        ...health,
        state: { ...health.state, Generation: generation },
      });
    });
    renderDialog();
    await screen.findByText('已熔断');
    fireEvent.change(screen.getByRole('textbox', { name: /复验记录/ }), {
      target: { value: 'review:17' },
    });
    fireEvent.click(
      screen.getByRole('checkbox', { name: /确认解除熔断但保持下架/ }),
    );
    fireEvent.click(
      screen.getByRole('button', { name: '解除熔断（保持下架）' }),
    );
    expect(await screen.findByRole('alert')).toHaveTextContent(/代次.*刷新/);
    expect(
      screen.getByRole('button', { name: '解除熔断（保持下架）' }),
    ).toBeDisabled();
    fireEvent.click(screen.getByRole('button', { name: '刷新健康状态' }));
    await waitFor(() => expect(screen.queryByRole('alert')).toBeNull());
    expect(
      screen.getByRole('checkbox', { name: /确认解除熔断但保持下架/ }),
    ).not.toBeChecked();
    fireEvent.click(
      screen.getByRole('checkbox', { name: /确认解除熔断但保持下架/ }),
    );
    fireEvent.click(
      screen.getByRole('button', { name: '解除熔断（保持下架）' }),
    );
    await waitFor(() =>
      expect(
        adminRequest.mock.calls.filter(
          ([request]) => request.method === 'POST',
        ),
      ).toHaveLength(2),
    );
    const writes = adminRequest.mock.calls.filter(
      ([request]) => request.method === 'POST',
    );
    expect(JSON.parse(writes[1][0].body).generation).toBe(5);
  });

  it.each([403, 503])(
    'blocks duplicate submission and requires refresh after recovery error %i',
    async (status) => {
      let rejectWrite;
      adminRequest.mockImplementation(({ method }) =>
        method === 'POST'
          ? new Promise((_, reject) => {
              rejectWrite = reject;
            })
          : Promise.resolve(health),
      );
      renderDialog();
      await screen.findByText('已熔断');
      fireEvent.change(screen.getByRole('textbox', { name: /复验记录/ }), {
        target: { value: 'review:17' },
      });
      fireEvent.click(
        screen.getByRole('checkbox', { name: /确认解除熔断但保持下架/ }),
      );
      const submit = screen.getByRole('button', {
        name: '解除熔断（保持下架）',
      });
      fireEvent.click(submit);
      fireEvent.click(submit);
      expect(
        adminRequest.mock.calls.filter(
          ([request]) => request.method === 'POST',
        ),
      ).toHaveLength(1);
      rejectWrite({ status, message: 'PRIVATE_RECOVERY_ERROR' });
      expect(await screen.findByRole('alert')).toHaveTextContent(
        status === 403 ? '没有模型写入权限' : '恢复结果未确认',
      );
      expect(screen.queryByText('PRIVATE_RECOVERY_ERROR')).toBeNull();
      expect(
        screen.getByRole('button', { name: '解除熔断（保持下架）' }),
      ).toBeDisabled();
      expect(
        screen.getByRole('checkbox', { name: /确认解除熔断但保持下架/ }),
      ).not.toBeChecked();
    },
  );

  it('shows retryable read failures without exposing backend error text', async () => {
    let reads = 0;
    adminRequest.mockImplementation(({ url }) => {
      if (!url.endsWith('/health')) return Promise.reject({ status: 404 });
      return ++reads === 1
        ? Promise.reject({ status: 503, message: 'SECRET' })
        : Promise.resolve(health);
    });
    renderDialog();
    expect(await screen.findByRole('alert')).toHaveTextContent(
      '健康状态暂时不可用',
    );
    expect(screen.queryByText('SECRET')).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: '刷新健康状态' }));
    expect(await screen.findByText('已熔断')).toBeVisible();
  });

  it('does not wait for optional worker status to complete recovery and unlock the dialog', async () => {
    adminRequest.mockImplementation(({ url, method }) => {
      if (url.endsWith('/health/status')) return new Promise(() => {});
      if (method === 'POST')
        return Promise.resolve({
          model_id: 17,
          recovered: true,
          published: false,
        });
      return Promise.resolve(health);
    });
    renderDialog();
    await screen.findByText('已熔断');
    fireEvent.change(screen.getByRole('textbox', { name: /复验记录/ }), {
      target: { value: 'review:17' },
    });
    fireEvent.click(
      screen.getByRole('checkbox', { name: /确认解除熔断但保持下架/ }),
    );
    fireEvent.click(
      screen.getByRole('button', { name: '解除熔断（保持下架）' }),
    );
    await screen.findByText('熔断已解除，模型保持下架。');
    await waitFor(() =>
      expect(
        screen.getByRole('button', { name: '关闭健康状态' }),
      ).toBeEnabled(),
    );
    expect(screen.getByText('工作进程状态暂不可用')).toBeVisible();
  });

  it('adds a read-only per-model action and restores focus when closed', async () => {
    adminRequest.mockImplementation(({ url }) => {
      if (url.endsWith('/health')) return Promise.resolve(health);
      if (url.includes('audit-events')) return Promise.resolve({ items: [] });
      return Promise.resolve({ items: [model] });
    });
    render(<AdminModelsPage canWrite={false} />);
    const action = await screen.findByRole('button', {
      name: '健康状态 zt-example',
    });
    expect(
      adminRequest.mock.calls.some(([request]) =>
        request.url.endsWith('/health'),
      ),
    ).toBe(false);
    fireEvent.click(action);
    const dialog = await screen.findByRole('dialog', { name: '模型健康状态' });
    await within(dialog).findByText('已熔断');
    fireEvent.click(
      within(dialog).getByRole('button', { name: '关闭健康状态' }),
    );
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(action).toHaveFocus();
  });
});
