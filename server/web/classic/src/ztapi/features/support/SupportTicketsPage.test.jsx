import React from 'react';
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import SupportTicketsPage from './SupportTicketsPage';
import { supportAdminApi } from './support-api';

vi.mock('./support-api', async (original) => ({
  ...(await original()),
  supportAdminApi: {
    list: vi.fn(),
    detail: vi.fn(),
    reply: vi.fn(),
    status: vi.fn(),
    attachment: vi.fn(),
  },
}));
const ticket = {
  id: 12,
  conversation_id: 'c1',
  subject: '失败请求',
  content: '请核实',
  contact: '邮箱联系',
  request_id: 'req1',
  status: 'open',
  version: 1,
  created_at: '2026-11-02T00:00:00Z',
  updated_at: '2026-11-02T00:00:00Z',
};
const detail = {
  ticket,
  conversation: { id: 'c1', title: '问题' },
  messages: [
    {
      id: 1,
      role: 'assistant',
      content: '<script>alert(1)</script>',
      created_at: ticket.created_at,
      has_image: true,
    },
  ],
  diagnosis: {
    request_id: 'req1',
    model: 'public-model',
    created_at: ticket.created_at,
    outcome: 'failed',
    code: 'timeout',
    summary: '凭证未确认',
    http_status: 504,
    stream: true,
    dispatched: true,
    usage_received: false,
    charged_u: '0',
    reserved_u: '0.2',
    needs_human: true,
  },
};
function mount() {
  return render(
    <MemoryRouter
      future={{ v7_startTransition: true, v7_relativeSplatPath: true }}
    >
      <SupportTicketsPage />
    </MemoryRouter>,
  );
}
beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(supportAdminApi.list).mockResolvedValue({
    items: [ticket],
    next_after_id: 20,
  });
  vi.mocked(supportAdminApi.detail).mockResolvedValue(detail);
});
afterEach(cleanup);

describe('support ticket inbox', () => {
  it('shows missing diagnostic evidence explicitly rather than implying no charge', async () => {
    vi.mocked(supportAdminApi.detail).mockResolvedValue({
      ...detail,
      user_id: 81,
      diagnosis: undefined,
      diagnosis_unavailable: true,
    });
    mount();
    fireEvent.click(
      await screen.findByRole('button', { name: /#12 失败请求/ }),
    );
    expect(
      await screen.findByText('诊断证据暂不可用，不能据此认定无扣款。'),
    ).toBeVisible();
    expect(screen.getByText('81')).toBeVisible();
  });
  it('retrieves the private customer screenshot only after staff explicitly opens it', async () => {
    vi.mocked(supportAdminApi.detail).mockResolvedValue({
      ...detail,
      messages: [{ ...detail.messages[0], attachment_id: 31 }],
    });
    vi.mocked(supportAdminApi.attachment).mockResolvedValue({
      mime_type: 'image/png',
      data_base64: 'iVBORw0KGgo=',
    });
    mount();
    fireEvent.click(
      await screen.findByRole('button', { name: /#12 失败请求/ }),
    );
    await screen.findByRole('button', { name: '查看截图' });
    expect(supportAdminApi.attachment).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: '查看截图' }));
    expect(
      await screen.findByRole('img', { name: '工单截图' }),
    ).toHaveAttribute('src', 'data:image/png;base64,iVBORw0KGgo=');
    expect(supportAdminApi.attachment).toHaveBeenCalledWith(31);
    expect(screen.queryByText('附有截图（不保留原图）')).toBeNull();
  });
  it('loads filtered cursor pages and safely renders complete ticket evidence and history', async () => {
    mount();
    fireEvent.click(
      await screen.findByRole('button', { name: /#12 失败请求/ }),
    );
    expect(await screen.findByText('<script>alert(1)</script>')).toBeVisible();
    expect(document.querySelector('script')).toBeNull();
    expect(screen.getByText('凭证未确认')).toBeVisible();
    expect(screen.getByText('邮箱联系')).toBeVisible();
    fireEvent.change(screen.getByLabelText('工单状态筛选'), {
      target: { value: 'all' },
    });
    await waitFor(() =>
      expect(supportAdminApi.list).toHaveBeenLastCalledWith('all', 0),
    );
    fireEvent.click(screen.getByRole('button', { name: '下一页' }));
    await waitFor(() =>
      expect(supportAdminApi.list).toHaveBeenLastCalledWith('all', 20),
    );
  });

  it('replies as human without changing ticket status implicitly', async () => {
    vi.mocked(supportAdminApi.reply).mockResolvedValue({
      ticket,
      message: {
        id: 2,
        role: 'human',
        content: '已经核实',
        created_at: ticket.created_at,
      },
    });
    mount();
    fireEvent.click(
      await screen.findByRole('button', { name: /#12 失败请求/ }),
    );
    fireEvent.change(await screen.findByLabelText('人工回复内容'), {
      target: { value: '已经核实' },
    });
    fireEvent.click(screen.getByRole('button', { name: '发送人工回复' }));
    expect(await screen.findByText('已经核实')).toBeVisible();
    expect(screen.getByText('人工回复')).toBeVisible();
    expect(supportAdminApi.reply).toHaveBeenCalledWith(
      12,
      expect.objectContaining({
        content: '已经核实',
        idempotency_key: expect.any(String),
      }),
    );
    expect(supportAdminApi.status).not.toHaveBeenCalled();
  });

  it('keeps the exact reply key on explicit retry and never automatically retries', async () => {
    vi.mocked(supportAdminApi.reply).mockRejectedValue(new Error('secret'));
    mount();
    fireEvent.click(
      await screen.findByRole('button', { name: /#12 失败请求/ }),
    );
    fireEvent.change(await screen.findByLabelText('人工回复内容'), {
      target: { value: '已经核实' },
    });
    fireEvent.click(screen.getByRole('button', { name: '发送人工回复' }));
    await screen.findByRole('button', { name: '重试原回复' });
    expect(supportAdminApi.reply).toHaveBeenCalledTimes(1);
    const first = vi.mocked(supportAdminApi.reply).mock.calls[0];
    fireEvent.click(screen.getByRole('button', { name: '重试原回复' }));
    await waitFor(() => expect(supportAdminApi.reply).toHaveBeenCalledTimes(2));
    expect(vi.mocked(supportAdminApi.reply).mock.calls[1]).toEqual(first);
    expect(screen.queryByText('secret')).toBeNull();
  });

  it('requires explicit status application and reloads version on 409 without resubmission', async () => {
    vi.mocked(supportAdminApi.status).mockRejectedValue({ status: 409 });
    mount();
    fireEvent.click(
      await screen.findByRole('button', { name: /#12 失败请求/ }),
    );
    fireEvent.change(await screen.findByLabelText('目标工单状态'), {
      target: { value: 'resolved' },
    });
    expect(supportAdminApi.status).not.toHaveBeenCalled();
    vi.mocked(supportAdminApi.detail).mockResolvedValue({
      ...detail,
      ticket: { ...ticket, status: 'in_progress', version: 2 },
    });
    fireEvent.click(screen.getByRole('button', { name: '应用状态' }));
    expect(await screen.findByRole('alert')).toHaveTextContent(
      '状态已被其他员工修改',
    );
    expect(supportAdminApi.status).toHaveBeenCalledTimes(1);
    expect(supportAdminApi.status).toHaveBeenCalledWith(
      12,
      expect.objectContaining({ status: 'resolved', version: 1 }),
    );
    await waitFor(() =>
      expect(screen.getByLabelText('目标工单状态')).toHaveValue('in_progress'),
    );
    fireEvent.change(screen.getByLabelText('目标工单状态'), {
      target: { value: 'resolved' },
    });
    vi.mocked(supportAdminApi.status).mockResolvedValue({
      ...ticket,
      status: 'resolved',
      version: 3,
    });
    fireEvent.click(screen.getByRole('button', { name: '应用状态' }));
    await waitFor(() =>
      expect(supportAdminApi.status).toHaveBeenCalledTimes(2),
    );
    const calls = vi.mocked(supportAdminApi.status).mock.calls;
    expect(calls[1][1].version).toBe(2);
    expect(calls[1][1].idempotency_key).not.toBe(calls[0][1].idempotency_key);
  });

  it('can confirm an ambiguous status operation with the original key after refreshing', async () => {
    vi.mocked(supportAdminApi.status).mockRejectedValue(new Error('network'));
    mount();
    fireEvent.click(
      await screen.findByRole('button', { name: /#12 失败请求/ }),
    );
    fireEvent.change(await screen.findByLabelText('目标工单状态'), {
      target: { value: 'resolved' },
    });
    fireEvent.click(screen.getByRole('button', { name: '应用状态' }));
    await screen.findByRole('button', { name: '重试原状态' });
    const first = vi.mocked(supportAdminApi.status).mock.calls[0];
    vi.mocked(supportAdminApi.detail).mockResolvedValue({
      ...detail,
      ticket: { ...ticket, status: 'resolved', version: 2 },
    });
    fireEvent.click(screen.getByRole('button', { name: '刷新工单详情' }));
    await waitFor(() =>
      expect(screen.getByLabelText('目标工单状态')).toHaveValue('resolved'),
    );
    expect(screen.getByRole('button', { name: '重试原状态' })).toBeEnabled();
    fireEvent.click(screen.getByRole('button', { name: '重试原状态' }));
    await waitFor(() =>
      expect(supportAdminApi.status).toHaveBeenCalledTimes(2),
    );
    expect(vi.mocked(supportAdminApi.status).mock.calls[1]).toEqual(first);
  });
});
