import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { SupportPage } from './SupportPage';
import { supportApi, SupportError } from '../../api/support';

vi.mock('../../auth/session', () => ({ useAuth: () => ({ user: { id: 7 } }) }));
vi.mock('../../api/support', async (original) => {
  const actual = await original<typeof import('../../api/support')>();
  return {
    ...actual,
    supportApi: {
      config: vi.fn(),
      conversations: vi.fn(),
      conversation: vi.fn(),
      requests: vi.fn(),
      diagnosis: vi.fn(),
      createConversation: vi.fn(),
      sendMessage: vi.fn(),
      createTicket: vi.fn(),
      attachment: vi.fn(),
      telegramBinding: vi.fn(),
      updateTelegramBinding: vi.fn(),
    },
  };
});
const config = {
  enabled: true,
  model_label: '平台 AI',
  vision_enabled: false,
  max_message_chars: 2000,
  max_image_bytes: 3145728,
  knowledge_version: 'v1',
};
const conversation = {
  id: 'c1',
  title: '历史问题',
  created_at: '2026-11-02T00:00:00Z',
  updated_at: '2026-11-02T00:00:00Z',
};
const message = {
  id: 1,
  role: 'assistant' as const,
  content: '<img src=x onerror=alert(1)>',
  has_image: false,
  created_at: conversation.created_at,
};
const diagnosis = {
  request_id: 'req1',
  model: 'public-model',
  created_at: conversation.created_at,
  outcome: 'failed',
  code: 'timeout',
  summary: '上游未确认用量',
  http_status: 504,
  stream: true,
  dispatched: true,
  usage_received: false,
  charged_u: '0',
  reserved_u: '0.2',
  needs_human: true,
};
const ticket = {
  id: 12,
  conversation_id: 'c1',
  subject: '请求失败',
  content: '请检查',
  contact: '',
  request_id: 'req1',
  status: 'open',
  version: 1,
  created_at: conversation.created_at,
  updated_at: conversation.updated_at,
};
function mount(url = '/console/support') {
  return render(
    <MemoryRouter initialEntries={[url]}>
      <SupportPage />
    </MemoryRouter>,
  );
}
beforeEach(() => {
  vi.clearAllMocks();
  sessionStorage.clear();
  localStorage.clear();
  vi.mocked(supportApi.config).mockResolvedValue(config);
  vi.mocked(supportApi.conversations).mockResolvedValue({
    items: [conversation],
  });
  vi.mocked(supportApi.conversation).mockResolvedValue({
    conversation,
    messages: [message],
  });
  vi.mocked(supportApi.requests).mockResolvedValue({ items: [diagnosis] });
  vi.mocked(supportApi.diagnosis).mockResolvedValue(diagnosis);
  vi.mocked(supportApi.createConversation).mockResolvedValue(conversation);
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.restoreAllMocks();
});

function scrollMetrics(height = 1200) {
  const metrics = { height };
  vi.spyOn(Element.prototype, 'scrollHeight', 'get').mockImplementation(
    function (this: Element) {
      return this.classList.contains('support-messages') ? metrics.height : 0;
    },
  );
  vi.spyOn(Element.prototype, 'clientHeight', 'get').mockImplementation(
    function (this: Element) {
      return this.classList.contains('support-messages') ? 600 : 0;
    },
  );
  return metrics;
}

describe('platform support', () => {
  it('keeps general setup help available on old servers without a binding or additional login', async () => {
    mount();
    await screen.findByText(message.content);
    expect(screen.getByLabelText('消息内容')).toBeEnabled();
    expect(supportApi.telegramBinding).not.toHaveBeenCalled();
    expect(screen.getByText(/小 Z/)).toBeInTheDocument();
    expect(screen.getByText(/不要发送密码、API Key/)).toBeInTheDocument();
  });

  it('loads opt-in binding independently without blocking general setup on binding failure', async () => {
    vi.mocked(supportApi.config).mockResolvedValue({ ...config, telegram_binding_enabled: true });
    vi.mocked(supportApi.telegramBinding).mockRejectedValue(new SupportError(503));
    mount();
    await screen.findByText(message.content);
    await screen.findByRole('region', { name: 'Telegram 工单通知' });
    expect(screen.getByLabelText('消息内容')).toBeEnabled();
    expect(screen.getByRole('button', { name: '申请人工工单' })).toBeEnabled();
    expect(screen.getByRole('alert')).toHaveTextContent('绑定结果尚未确认');
  });

  it('opens an owned conversation from a deterministic ticket link', async () => {
    const owned = { ...conversation, id: 'owned-link', title: '链接中的工单' };
    vi.mocked(supportApi.conversation).mockResolvedValue({ conversation: owned, messages: [{ ...message, content: '本人进度' }] });
    mount('/console/support?conversation=owned-link');
    await screen.findByText('本人进度');
    expect(supportApi.conversation).toHaveBeenCalledWith('owned-link');
    expect(supportApi.createConversation).not.toHaveBeenCalled();
  });

  it('does not fall back to another conversation or invent success for a foreign link', async () => {
    vi.mocked(supportApi.conversation).mockRejectedValue(new SupportError(404));
    mount('/console/support?conversation=foreign');
    await screen.findByRole('alert');
    expect(screen.getByRole('alert')).toHaveTextContent('记录不存在或无权访问');
    expect(supportApi.conversation).toHaveBeenCalledExactlyOnceWith('foreign');
    expect(screen.queryByText(message.content)).not.toBeInTheDocument();
  });

  it('offers the authorized user community without replacing private support or handoff', async () => {
    mount(); await screen.findByText(message.content);
    const community = screen.getByRole('link', { name: '用户交流群' });
    expect(community).toHaveAttribute('href', 'https://t.me/ztapivip');
    expect(community).toHaveAttribute('target', '_blank');
    expect(community).toHaveAttribute('rel', 'noopener noreferrer');
    expect(community.querySelector('svg')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '申请人工工单' })).toBeEnabled();
  });
  it('shows unknown diagnostic amounts as unconfirmed rather than an empty U amount', async () => {
    vi.mocked(supportApi.diagnosis).mockResolvedValue({
      ...diagnosis,
      charged_u: '',
      reserved_u: '',
    });
    mount();
    await screen.findByText(message.content);
    fireEvent.click(screen.getByRole('button', { name: /req1/ }));
    await screen.findByText('诊断证据');
    expect(
      screen.getByText('已扣金额').parentElement?.querySelector('dd'),
    ).toHaveTextContent(/^未确认$/);
    expect(
      screen.getByText('预留金额').parentElement?.querySelector('dd'),
    ).toHaveTextContent(/^未确认$/);
  });

  it('keeps a confirmed zero diagnostic amount as 0 U', async () => {
    vi.mocked(supportApi.diagnosis).mockResolvedValue({
      ...diagnosis,
      charged_u: '0',
      reserved_u: '0',
    });
    mount();
    await screen.findByText(message.content);
    fireEvent.click(screen.getByRole('button', { name: /req1/ }));
    await screen.findByText('诊断证据');
    expect(
      screen.getByText('已扣金额').parentElement?.querySelector('dd'),
    ).toHaveTextContent(/^0 U$/);
    expect(
      screen.getByText('预留金额').parentElement?.querySelector('dd'),
    ).toHaveTextContent(/^0 U$/);
  });

  it('opens persisted history at the latest message', async () => {
    scrollMetrics();
    mount();
    await screen.findByText(message.content);
    expect(screen.getByRole('log', { name: '聊天记录' }).scrollTop).toBe(600);
  });

  it('follows new messages only when the reader was near the bottom', async () => {
    const metrics = scrollMetrics();
    mount();
    await screen.findByText(message.content);
    const log = screen.getByRole('log', { name: '聊天记录' });
    fireEvent.scroll(log, { target: { scrollTop: 580 } });
    metrics.height = 1800;
    vi.mocked(supportApi.conversation).mockResolvedValue({
      conversation,
      messages: [
        message,
        { ...message, id: 2, role: 'human', content: '新的人工回复' },
      ],
    });
    fireEvent.click(screen.getByRole('button', { name: '刷新会话' }));
    await screen.findByText('新的人工回复');
    expect(log.scrollTop).toBe(1200);
  });

  it('does not pull a reader away from older history when a new message arrives', async () => {
    const metrics = scrollMetrics();
    mount();
    await screen.findByText(message.content);
    const log = screen.getByRole('log', { name: '聊天记录' });
    fireEvent.scroll(log, { target: { scrollTop: 0 } });
    metrics.height = 1800;
    vi.mocked(supportApi.conversation).mockResolvedValue({
      conversation,
      messages: [
        message,
        { ...message, id: 2, role: 'human', content: '阅读历史时收到人工消息' },
      ],
    });
    fireEvent.click(screen.getByRole('button', { name: '刷新会话' }));
    await screen.findByText('阅读历史时收到人工消息');
    expect(log.scrollTop).toBe(0);
  });

  it('returns to the latest messages after the user sends from older history', async () => {
    const metrics = scrollMetrics();
    mount();
    await screen.findByText(message.content);
    const log = screen.getByRole('log', { name: '聊天记录' });
    fireEvent.scroll(log, { target: { scrollTop: 0 } });
    const sent = {
      ...message,
      id: 2,
      role: 'user' as const,
      content: '自己发送的新消息',
    };
    const reply = { ...message, id: 3, content: '新答复' };
    vi.mocked(supportApi.sendMessage).mockResolvedValue({
      message: sent,
      reply,
      call_id: 'call-scroll',
    });
    vi.mocked(supportApi.conversation).mockResolvedValue({
      conversation,
      messages: [message, sent, reply],
    });
    metrics.height = 1800;
    fireEvent.change(screen.getByLabelText('消息内容'), {
      target: { value: sent.content },
    });
    fireEvent.click(screen.getByRole('button', { name: '发送消息' }));
    await screen.findByText('新答复');
    expect(log.scrollTop).toBe(1200);
  });

  it('returns to the latest message when switching between conversations', async () => {
    scrollMetrics();
    const other = { ...conversation, id: 'c2', title: '另一个会话' };
    vi.mocked(supportApi.conversations).mockResolvedValue({
      items: [conversation, other],
    });
    vi.mocked(supportApi.conversation).mockImplementation(async (id) => ({
      conversation: id === other.id ? other : conversation,
      messages: [
        {
          ...message,
          content: id === other.id ? '另一会话消息' : message.content,
        },
      ],
    }));
    mount();
    await screen.findByText(message.content);
    const log = screen.getByRole('log', { name: '聊天记录' });
    fireEvent.scroll(log, { target: { scrollTop: 0 } });
    fireEvent.click(screen.getByRole('button', { name: '另一个会话' }));
    await screen.findByText('另一会话消息');
    expect(log.scrollTop).toBe(600);
    fireEvent.scroll(log, { target: { scrollTop: 0 } });
    fireEvent.click(screen.getByRole('button', { name: '历史问题' }));
    await screen.findByText(message.content);
    expect(log.scrollTop).toBe(600);
  });
  it.each(['provider 失败', '调用中断', '未派发终态'])(
    'restores a persisted pending AI message from its user + system terminal (%s) without a new POST',
    async (reason) => {
      sessionStorage.setItem(
        'ztapi.support.pending.7',
        JSON.stringify({
          conversationId: 'c1',
          key: 'pending-system-terminal',
          after: 1,
        }),
      );
      vi.mocked(supportApi.conversation).mockResolvedValue({
        conversation,
        messages: [
          message,
          { ...message, id: 2, role: 'user', content: '已经发送' },
          { ...message, id: 3, role: 'system', content: reason },
        ],
      });
      mount();
      await screen.findByText(reason);
      expect(screen.getByLabelText('消息内容')).toBeEnabled();
      expect(screen.getByRole('button', { name: '新建会话' })).toBeEnabled();
      expect(screen.getByRole('button', { name: '历史问题' })).toBeEnabled();
      expect(sessionStorage.getItem('ztapi.support.pending.7')).toBeNull();
      expect(supportApi.sendMessage).not.toHaveBeenCalled();
      expect(supportApi.createConversation).not.toHaveBeenCalled();
    },
  );

  it('does not clear a pending AI operation for an unrelated system notice without a matching new user message', async () => {
    sessionStorage.setItem(
      'ztapi.support.pending.7',
      JSON.stringify({
        conversationId: 'c1',
        key: 'pending-unmatched',
        after: 1,
      }),
    );
    vi.mocked(supportApi.conversation).mockResolvedValue({
      conversation,
      messages: [
        message,
        { ...message, id: 3, role: 'system', content: '无关系统通知' },
      ],
    });
    mount();
    await screen.findByText('无关系统通知');
    expect(screen.getByLabelText('消息内容')).toBeDisabled();
    expect(sessionStorage.getItem('ztapi.support.pending.7')).not.toBeNull();
    expect(supportApi.sendMessage).not.toHaveBeenCalled();
  });
  it('does not replay a failed human message into AI after the ticket is resolved', async () => {
    vi.mocked(supportApi.conversation).mockResolvedValue({
      conversation,
      messages: [message],
      ticket,
    });
    vi.mocked(supportApi.sendMessage).mockRejectedValue(new Error('network'));
    mount();
    await screen.findByText(message.content);
    fireEvent.change(screen.getByLabelText('消息内容'), {
      target: { value: '原人工消息' },
    });
    fireEvent.click(screen.getByRole('button', { name: '发送消息' }));
    await screen.findByRole('button', { name: '重试原消息' });
    vi.mocked(supportApi.conversation).mockResolvedValue({
      conversation,
      messages: [message],
      ticket: { ...ticket, status: 'resolved', version: 2 },
    });
    fireEvent.click(screen.getByRole('button', { name: '刷新会话' }));
    await screen.findByText(/工单状态已变化/);
    expect(screen.getByRole('button', { name: '重试原消息' })).toBeDisabled();
    expect(screen.getByRole('button', { name: '申请人工工单' })).toBeEnabled();
    expect(supportApi.sendMessage).toHaveBeenCalledTimes(1);
  });
  it('confirms an ambiguous human message from the persisted system acknowledgement without resending', async () => {
    vi.mocked(supportApi.conversation).mockResolvedValue({
      conversation,
      messages: [message],
      ticket,
    });
    vi.mocked(supportApi.sendMessage).mockRejectedValue(new Error('network'));
    mount();
    await screen.findByText(message.content);
    fireEvent.change(screen.getByLabelText('消息内容'), {
      target: { value: '补充人工证据' },
    });
    fireEvent.click(screen.getByRole('button', { name: '发送消息' }));
    await screen.findByRole('button', { name: '重试原消息' });
    vi.mocked(supportApi.conversation).mockResolvedValue({
      conversation,
      ticket,
      messages: [
        message,
        { ...message, id: 3, role: 'user', content: '补充人工证据' },
        { ...message, id: 4, role: 'system', content: '已转交人工工单' },
      ],
    });
    fireEvent.click(screen.getByRole('button', { name: '刷新会话' }));
    await screen.findByText('已转交人工工单');
    expect(screen.queryByRole('button', { name: '重试原消息' })).toBeNull();
    expect(screen.getByLabelText('消息内容')).toBeEnabled();
    expect(supportApi.sendMessage).toHaveBeenCalledTimes(1);
  });
  it('shows storage capacity as a storage limit, not a daily call limit', async () => {
    vi.mocked(supportApi.config).mockResolvedValue({
      ...config,
      vision_enabled: true,
    });
    vi.mocked(supportApi.sendMessage).mockRejectedValue(
      new SupportError(413, 'image_storage_full'),
    );
    mount();
    const input = await screen.findByLabelText('上传截图');
    fireEvent.change(input, {
      target: {
        files: [
          new File(
            [new Uint8Array([137, 80, 78, 71, 13, 10, 26, 10])],
            'a.png',
            { type: 'image/png' },
          ),
        ],
      },
    });
    await screen.findByRole('button', { name: '移除截图' });
    fireEvent.click(screen.getByRole('button', { name: '发送消息' }));
    expect(await screen.findByRole('alert')).toHaveTextContent(
      '每用户 50MB、全站 512MB',
    );
    expect(screen.queryByText(/每日|2U|20次/)).toBeNull();
  });

  it('keeps an existing human ticket usable when AI support is disabled', async () => {
    vi.mocked(supportApi.config).mockResolvedValue({
      ...config,
      enabled: false,
    });
    vi.mocked(supportApi.conversation).mockResolvedValue({
      conversation,
      messages: [message],
      ticket,
    });
    mount();
    await screen.findByText(message.content);
    fireEvent.change(screen.getByLabelText('消息内容'), {
      target: { value: '补充人工消息' },
    });
    expect(screen.getByRole('button', { name: '发送消息' })).toBeEnabled();
  });
  it.each(['open', 'in_progress'])(
    'sends follow-up messages to the active %s human ticket and accepts system acknowledgement with an empty call ID',
    async (status) => {
      const acknowledgement = {
        ...message,
        id: 4,
        role: 'system' as const,
        content: '已转交人工工单',
      };
      const sent = {
        ...message,
        id: 3,
        role: 'user' as const,
        content: '补充证据',
      };
      vi.mocked(supportApi.conversation).mockResolvedValue({
        conversation,
        messages: [message],
        ticket: { ...ticket, status },
      });
      vi.mocked(supportApi.sendMessage).mockResolvedValue({
        message: sent,
        reply: acknowledgement,
        call_id: '',
      });
      mount();
      await screen.findByText(message.content);
      expect(
        screen.getByText('人工工单处理中，后续消息交由人工，不调用 AI。'),
      ).toBeVisible();
      fireEvent.change(screen.getByLabelText('消息内容'), {
        target: { value: '补充证据' },
      });
      vi.mocked(supportApi.conversation).mockResolvedValue({
        conversation,
        messages: [message, sent, acknowledgement],
        ticket: { ...ticket, status },
      });
      fireEvent.click(screen.getByRole('button', { name: '发送消息' }));
      expect(await screen.findByText('已转交人工工单')).toBeVisible();
      expect(screen.getByText('系统通知')).toBeVisible();
      expect(screen.queryByText(/回复尚未确认/)).toBeNull();
      expect(supportApi.sendMessage).toHaveBeenCalledTimes(1);
    },
  );

  it('does not claim human-only routing after the ticket is resolved', async () => {
    vi.mocked(supportApi.conversation).mockResolvedValue({
      conversation,
      messages: [message],
      ticket: { ...ticket, status: 'resolved' },
    });
    mount();
    await screen.findByText(message.content);
    expect(
      screen.queryByText('人工工单处理中，后续消息交由人工，不调用 AI。'),
    ).toBeNull();
  });

  it('loads a persisted screenshot only after an explicit click, never through a public image URL', async () => {
    vi.mocked(supportApi.conversation).mockResolvedValue({
      conversation,
      messages: [{ ...message, has_image: true, attachment_id: 31 }],
    });
    vi.mocked(supportApi.attachment).mockResolvedValue({
      mime_type: 'image/png',
      data_base64: 'iVBORw0KGgo=',
    });
    mount();
    await screen.findByText(message.content);
    expect(supportApi.attachment).not.toHaveBeenCalled();
    expect(screen.queryByRole('img', { name: '会话截图' })).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: '查看截图' }));
    expect(
      await screen.findByRole('img', { name: '会话截图' }),
    ).toHaveAttribute('src', 'data:image/png;base64,iVBORw0KGgo=');
    expect(supportApi.attachment).toHaveBeenCalledWith(31);
    fireEvent.click(screen.getByRole('button', { name: '关闭截图' }));
    expect(screen.queryByRole('img', { name: '会话截图' })).toBeNull();
    expect(screen.queryByText('附有截图（不保留原图）')).toBeNull();
  });

  it('rejects unsafe attachment MIME and does not render server error details', async () => {
    vi.mocked(supportApi.conversation).mockResolvedValue({
      conversation,
      messages: [{ ...message, has_image: true, attachment_id: 31 }],
    });
    vi.mocked(supportApi.attachment).mockResolvedValue({
      mime_type: 'image/svg+xml',
      data_base64: 'PHN2Zy8+',
    });
    mount();
    await screen.findByText(message.content);
    fireEvent.click(screen.getByRole('button', { name: '查看截图' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('截图读取失败');
    expect(screen.queryByRole('img', { name: '会话截图' })).toBeNull();
  });

  it.each([413, 507])(
    'keeps text and human handoff usable after screenshot rejection HTTP %i, without automatic resend',
    async (status) => {
      vi.mocked(supportApi.config).mockResolvedValue({
        ...config,
        vision_enabled: true,
      });
      vi.mocked(supportApi.sendMessage).mockRejectedValue(
        new SupportError(status),
      );
      mount();
      const input = await screen.findByLabelText('上传截图');
      expect(screen.getByText(/上传前请遮住 API Key、密码/)).toBeVisible();
      fireEvent.change(input, {
        target: {
          files: [
            new File(
              [new Uint8Array([137, 80, 78, 71, 13, 10, 26, 10])],
              'a.png',
              { type: 'image/png' },
            ),
          ],
        },
      });
      await screen.findByRole('button', { name: '移除截图' });
      fireEvent.change(screen.getByLabelText('消息内容'), {
        target: { value: '仍需人工核实' },
      });
      fireEvent.click(screen.getByRole('button', { name: '发送消息' }));
      fireEvent.click(
        await screen.findByRole('button', { name: '移除截图，改发文本' }),
      );
      expect(screen.getByLabelText('消息内容')).toHaveValue('仍需人工核实');
      expect(screen.getByRole('button', { name: '发送消息' })).toBeEnabled();
      expect(
        screen.getByRole('button', { name: '申请人工工单' }),
      ).toBeEnabled();
      expect(supportApi.sendMessage).toHaveBeenCalledTimes(1);
    },
  );
  it('does not mistake another call reply for delivery of an empty-call-id pending message', async () => {
    vi.mocked(supportApi.sendMessage).mockResolvedValue({
      pending: true,
      call_id: '',
    });
    mount();
    await screen.findByText(message.content);
    fireEvent.change(screen.getByLabelText('消息内容'), {
      target: { value: '保留我的原消息' },
    });
    fireEvent.click(screen.getByRole('button', { name: '发送消息' }));
    await screen.findByText(/回复尚未确认/);
    const first = vi.mocked(supportApi.sendMessage).mock.calls[0];
    vi.mocked(supportApi.conversation).mockResolvedValue({
      conversation,
      messages: [message, { ...message, id: 3, content: '另一请求的回复' }],
    });
    fireEvent.click(screen.getByRole('button', { name: '刷新会话' }));
    await screen.findByText('另一请求的回复');
    expect(screen.getByLabelText('消息内容')).toHaveValue('保留我的原消息');
    expect(supportApi.sendMessage).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByRole('button', { name: '重试原消息' }));
    await waitFor(() =>
      expect(supportApi.sendMessage).toHaveBeenCalledTimes(2),
    );
    expect(vi.mocked(supportApi.sendMessage).mock.calls[1]).toEqual(first);
  });
  it('does not let a stale history read erase a newly acknowledged ticket', async () => {
    vi.mocked(supportApi.createTicket).mockResolvedValue(ticket);
    mount();
    await screen.findByText(message.content);
    let finish!: (value: {
      conversation: typeof conversation;
      messages: (typeof message)[];
    }) => void;
    vi.mocked(supportApi.conversation).mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    );
    fireEvent.click(screen.getByRole('button', { name: '刷新会话' }));
    fireEvent.click(screen.getByRole('button', { name: '申请人工工单' }));
    fireEvent.change(screen.getByLabelText('工单主题'), {
      target: { value: '人工核实' },
    });
    fireEvent.change(screen.getByLabelText('问题详情'), {
      target: { value: '请核实' },
    });
    fireEvent.click(screen.getByRole('button', { name: '提交工单' }));
    await screen.findByText(/工单 #12/);
    await act(async () => {
      finish({ conversation, messages: [message] });
    });
    expect(screen.getByText(/工单 #12/)).toBeVisible();
  });
  it('restores server history on refresh, escapes HTML, and distinguishes human replies', async () => {
    vi.mocked(supportApi.conversation).mockResolvedValue({
      conversation,
      messages: [
        message,
        { ...message, id: 2, role: 'human', content: '已记录，正在核实' },
      ],
      ticket,
    });
    const view = mount();
    expect(await screen.findByText(message.content)).toBeVisible();
    expect(document.querySelector('img[src="x"]')).toBeNull();
    expect(screen.getByText('人工回复')).toBeVisible();
    expect(screen.getByText(/工单 #12/)).toBeVisible();
    expect(screen.queryByText(/人工在线/)).toBeNull();
    view.unmount();
    mount();
    expect(await screen.findByText('已记录，正在核实')).toBeVisible();
    expect(supportApi.createConversation).not.toHaveBeenCalled();
  });

  it('does not create or rename conversations while typing or selecting a new draft', async () => {
    mount();
    await screen.findByText(message.content);
    fireEvent.click(screen.getByRole('button', { name: '新建会话' }));
    fireEvent.change(screen.getByLabelText('消息内容'), {
      target: { value: '只是草稿' },
    });
    expect(supportApi.createConversation).not.toHaveBeenCalled();
    expect(supportApi.sendMessage).not.toHaveBeenCalled();
    expect(screen.getByRole('button', { name: '历史问题' })).toBeVisible();
  });

  it('keeps one immutable key and payload for explicit retry after an ambiguous failure', async () => {
    vi.mocked(supportApi.sendMessage).mockRejectedValue(
      new Error('private upstream error'),
    );
    mount();
    await screen.findByText(message.content);
    fireEvent.change(screen.getByLabelText('消息内容'), {
      target: { value: '请检查失败原因' },
    });
    fireEvent.click(screen.getByRole('button', { name: '发送消息' }));
    await screen.findByRole('button', { name: '重试原消息' });
    expect(supportApi.sendMessage).toHaveBeenCalledTimes(1);
    const first = vi.mocked(supportApi.sendMessage).mock.calls[0];
    fireEvent.click(screen.getByRole('button', { name: '重试原消息' }));
    await waitFor(() =>
      expect(supportApi.sendMessage).toHaveBeenCalledTimes(2),
    );
    expect(vi.mocked(supportApi.sendMessage).mock.calls[1]).toEqual(first);
    expect(first[1].idempotency_key).toBeTruthy();
    expect(screen.queryByText('private upstream error')).toBeNull();
  });

  it('polls a 202 result without another POST and recovers pending state after a remount', async () => {
    vi.mocked(supportApi.sendMessage).mockResolvedValue({
      pending: true,
      call_id: 'call1',
    });
    const view = mount();
    await screen.findByText(message.content);
    fireEvent.change(screen.getByLabelText('消息内容'), {
      target: { value: '你好' },
    });
    fireEvent.click(screen.getByRole('button', { name: '发送消息' }));
    await screen.findByText(/回复尚未确认/);
    view.unmount();
    mount();
    await screen.findByText(/回复尚未确认/);
    fireEvent.click(screen.getByRole('button', { name: '刷新会话' }));
    await waitFor(() =>
      expect(supportApi.conversation).toHaveBeenCalledTimes(3),
    );
    expect(supportApi.sendMessage).toHaveBeenCalledTimes(1);
  });

  it('gates screenshots and validates size, count, MIME and file signatures', async () => {
    const view = mount();
    await screen.findByText(message.content);
    expect(screen.queryByLabelText('上传截图')).toBeNull();
    view.unmount();
    vi.mocked(supportApi.config).mockResolvedValue({
      ...config,
      vision_enabled: true,
    });
    mount();
    const input = await screen.findByLabelText('上传截图');
    fireEvent.change(input, {
      target: { files: [new File(['x'], 'a.gif', { type: 'image/gif' })] },
    });
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'PNG、JPEG 或 WebP',
    );
    fireEvent.change(input, {
      target: {
        files: [
          new File([new Uint8Array(3145729)], 'a.png', { type: 'image/png' }),
        ],
      },
    });
    expect(await screen.findByRole('alert')).toHaveTextContent('3MB');
    fireEvent.change(input, {
      target: {
        files: [
          new File(['x'], 'a.png', { type: 'image/png' }),
          new File(['x'], 'b.png', { type: 'image/png' }),
        ],
      },
    });
    expect(await screen.findByRole('alert')).toHaveTextContent('最多 1 张');
    fireEvent.change(input, {
      target: {
        files: [new File(['not-png'], 'a.png', { type: 'image/png' })],
      },
    });
    expect(await screen.findByRole('alert')).toHaveTextContent('图片内容');
    expect(supportApi.sendMessage).not.toHaveBeenCalled();
  });

  it('sends a screenshot without prefilled text and polls even when the pending call ID is empty', async () => {
    vi.mocked(supportApi.config).mockResolvedValue({
      ...config,
      vision_enabled: true,
    });
    vi.mocked(supportApi.sendMessage).mockResolvedValue({
      pending: true,
      call_id: '',
    });
    mount();
    const input = await screen.findByLabelText('上传截图');
    fireEvent.change(input, {
      target: {
        files: [
          new File(
            [new Uint8Array([137, 80, 78, 71, 13, 10, 26, 10])],
            'a.png',
            { type: 'image/png' },
          ),
        ],
      },
    });
    await screen.findByRole('button', { name: '移除截图' });
    expect(screen.getByLabelText('消息内容')).toHaveValue('');
    fireEvent.click(screen.getByRole('button', { name: '发送消息' }));
    await screen.findByText(/回复尚未确认/);
    expect(supportApi.sendMessage).toHaveBeenCalledWith(
      'c1',
      expect.objectContaining({
        content: '',
        image: { mime_type: 'image/png', data_base64: 'iVBORw0KGgo=' },
      }),
    );
    expect(supportApi.sendMessage).toHaveBeenCalledTimes(1);
  });

  it('shows explicit request evidence and creates an acknowledged human ticket', async () => {
    vi.mocked(supportApi.createTicket).mockResolvedValue(ticket);
    mount();
    await screen.findByText(message.content);
    fireEvent.click(screen.getByRole('button', { name: /req1/ }));
    expect(await screen.findByText('上游未确认用量')).toBeVisible();
    expect(screen.getByText('用量凭证')).toBeVisible();
    expect(screen.getByText('预留金额')).toBeVisible();
    fireEvent.click(screen.getByRole('button', { name: '申请人工工单' }));
    fireEvent.change(screen.getByLabelText('工单主题'), {
      target: { value: '请求失败' },
    });
    fireEvent.change(screen.getByLabelText('问题详情'), {
      target: { value: '请检查' },
    });
    fireEvent.click(screen.getByRole('button', { name: '提交工单' }));
    expect(await screen.findByText(/工单 #12/)).toBeVisible();
    expect(supportApi.createTicket).toHaveBeenCalledWith(
      expect.objectContaining({
        conversation_id: 'c1',
        request_id: 'req1',
        subject: '请求失败',
        content: '请检查',
        idempotency_key: expect.any(String),
      }),
    );
  });

  it('fails closed when support configuration is disabled or unavailable', async () => {
    vi.mocked(supportApi.config).mockResolvedValue({
      ...config,
      enabled: false,
    });
    mount();
    await screen.findByText('平台客服暂未开放');
    expect(screen.getByRole('button', { name: '发送消息' })).toBeDisabled();
  });

  it('does not send into a conversation whose history could not be read', async () => {
    vi.mocked(supportApi.conversation).mockRejectedValue(
      new Error('not found'),
    );
    mount();
    await screen.findByRole('alert');
    fireEvent.change(screen.getByLabelText('消息内容'), {
      target: { value: '检查' },
    });
    expect(screen.getByRole('button', { name: '发送消息' })).toBeDisabled();
    expect(supportApi.sendMessage).not.toHaveBeenCalled();
  });

  it('allows human handoff while an AI reply is unconfirmed without replaying the AI request', async () => {
    vi.mocked(supportApi.sendMessage).mockResolvedValue({
      pending: true,
      call_id: '',
    });
    vi.mocked(supportApi.createTicket).mockResolvedValue(ticket);
    mount();
    await screen.findByText(message.content);
    fireEvent.change(screen.getByLabelText('消息内容'), {
      target: { value: '检查' },
    });
    fireEvent.click(screen.getByRole('button', { name: '发送消息' }));
    await screen.findByText(/回复尚未确认/);
    fireEvent.click(screen.getByRole('button', { name: '申请人工工单' }));
    fireEvent.change(screen.getByLabelText('工单主题'), {
      target: { value: '人工核实' },
    });
    fireEvent.change(screen.getByLabelText('问题详情'), {
      target: { value: 'AI 回复未确认，请核实' },
    });
    fireEvent.click(screen.getByRole('button', { name: '提交工单' }));
    await screen.findByText(/工单 #12/);
    expect(supportApi.sendMessage).toHaveBeenCalledTimes(1);
    expect(screen.getByLabelText('消息内容')).toBeEnabled();
  });

  it('polls pending history automatically and stops posting after the first send', async () => {
    vi.mocked(supportApi.sendMessage).mockResolvedValue({
      pending: true,
      call_id: '',
    });
    mount();
    await screen.findByText(message.content);
    vi.useFakeTimers();
    fireEvent.change(screen.getByLabelText('消息内容'), {
      target: { value: '你好' },
    });
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: '发送消息' }));
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(15_000);
    });
    expect(supportApi.conversation).toHaveBeenCalledTimes(4);
    expect(supportApi.sendMessage).toHaveBeenCalledTimes(1);
  });

  it('keeps the creation key stable when creating a new conversation fails', async () => {
    vi.mocked(supportApi.createConversation).mockRejectedValue(
      new Error('disconnected'),
    );
    mount();
    await screen.findByText(message.content);
    fireEvent.click(screen.getByRole('button', { name: '新建会话' }));
    fireEvent.change(screen.getByLabelText('消息内容'), {
      target: { value: '新问题' },
    });
    fireEvent.click(screen.getByRole('button', { name: '发送消息' }));
    fireEvent.click(await screen.findByRole('button', { name: '重试原消息' }));
    await waitFor(() =>
      expect(supportApi.createConversation).toHaveBeenCalledTimes(2),
    );
    expect(vi.mocked(supportApi.createConversation).mock.calls[0]).toEqual(
      vi.mocked(supportApi.createConversation).mock.calls[1],
    );
    expect(supportApi.sendMessage).not.toHaveBeenCalled();
  });

  it('keeps an immutable ticket payload after a failed ticket submission', async () => {
    vi.mocked(supportApi.createTicket).mockRejectedValue(new Error('network'));
    mount();
    await screen.findByText(message.content);
    fireEvent.click(screen.getByRole('button', { name: '申请人工工单' }));
    fireEvent.change(screen.getByLabelText('工单主题'), {
      target: { value: '人工核实' },
    });
    fireEvent.change(screen.getByLabelText('问题详情'), {
      target: { value: '请核实' },
    });
    fireEvent.click(screen.getByRole('button', { name: '提交工单' }));
    fireEvent.click(await screen.findByRole('button', { name: '重试原工单' }));
    await waitFor(() =>
      expect(supportApi.createTicket).toHaveBeenCalledTimes(2),
    );
    expect(vi.mocked(supportApi.createTicket).mock.calls[0]).toEqual(
      vi.mocked(supportApi.createTicket).mock.calls[1],
    );
    expect(screen.getByLabelText('工单主题')).toBeDisabled();
  });
});
