import {
  apiClient,
  clearAuthSession,
  getAuthSession,
  getAuthSessionLifecycle,
} from './client';

export type Conversation = {
  id: string;
  title: string;
  created_at: string;
  updated_at: string;
};
export type Message = {
  id: number;
  role: 'user' | 'assistant' | 'human' | 'system';
  content: string;
  created_at: string;
  has_image: boolean;
  attachment_id?: number;
};
export type Diagnosis = {
  request_id: string;
  model: string;
  created_at: string;
  outcome: string;
  code: string;
  summary: string;
  http_status: number;
  stream: boolean;
  dispatched: boolean;
  usage_received: boolean;
  charged_u: string;
  reserved_u: string;
  needs_human: boolean;
};
export type Ticket = {
  id: number;
  conversation_id: string;
  subject: string;
  content: string;
  contact: string;
  request_id: string;
  status: string;
  version: number;
  created_at: string;
  updated_at: string;
};
export type SupportConfig = {
  enabled: boolean;
  model_label: string;
  vision_enabled: boolean;
  max_message_chars: number;
  max_image_bytes: number;
  knowledge_version: string;
};
export type ConversationDetail = {
  conversation: Conversation;
  messages: Message[];
  ticket?: Ticket;
};
export type SupportImage = { mime_type: string; data_base64: string };
export type SendMessage = {
  content: string;
  request_id?: string;
  image?: SupportImage;
  idempotency_key: string;
};
export type TicketInput = {
  conversation_id: string;
  request_id?: string;
  subject: string;
  content: string;
  contact?: string;
  idempotency_key: string;
};
export type SendResult =
  | { message: Message; reply: Message; call_id: string }
  | { pending: true; call_id: string };
export class SupportError extends Error {
  constructor(
    public readonly status = 0,
    public readonly code: 'unknown' | 'image_storage_full' = 'unknown',
  ) {
    super('客服请求失败');
  }
}
async function request<T>(path: string, body?: unknown): Promise<T> {
  const session = getAuthSession();
  const lifecycle = getAuthSessionLifecycle();
  if (!session) throw new SupportError(401);
  const headers = new Headers({
    Accept: 'application/json',
    Authorization: `Bearer ${session.access_token}`,
    'New-Api-User': String(session.user.id),
  });
  if (body !== undefined) headers.set('Content-Type', 'application/json');
  let response: Response;
  try {
    // Support mutations must not be replayed by generic auth-refresh logic.
    response = await fetch(`/api/support${path}`, {
      method: body === undefined ? 'GET' : 'POST',
      credentials: 'include',
      cache: 'no-store',
      headers,
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    });
  } catch {
    throw new SupportError();
  }
  if (lifecycle !== getAuthSessionLifecycle()) throw new SupportError(401);
  if (
    response.status === 401 &&
    getAuthSession()?.access_token === session.access_token
  )
    clearAuthSession();
  if (!response.ok) {
    let code: 'unknown' | 'image_storage_full' = 'unknown';
    if (response.status === 413) {
      try {
        const failure = await response.json();
        if (failure?.message === 'support_image_storage_full')
          code = 'image_storage_full';
      } catch {
        /* Unrecognized error bodies are never shown. */
      }
    }
    throw new SupportError(response.status, code);
  }
  let envelope: { success?: boolean; data?: T };
  try {
    envelope = await response.json();
  } catch {
    throw new SupportError(response.status);
  }
  if (lifecycle !== getAuthSessionLifecycle()) throw new SupportError(401);
  if (envelope?.success !== true || envelope.data == null)
    throw new SupportError(response.status);
  return envelope.data;
}
export function supportErrorMessage(error: unknown) {
  if (error instanceof SupportError) {
    if (error.status === 401) return '登录已失效，请重新登录。';
    if (error.status === 403) return '当前账号无权访问客服服务。';
    if (error.status === 404) return '记录不存在或无权访问。';
    if (error.status === 429) return '请求过于频繁，请稍后操作。';
    if (error.status === 413) return '截图超过允许大小。';
  }
  return '客服请求失败，结果尚未确认。请刷新查看或显式重试原请求。';
}
export const supportApi = {
  async attachment(id: number): Promise<SupportImage> {
    const lifecycle = getAuthSessionLifecycle();
    const value = await apiClient.get<SupportImage>(
      `/support/attachments/${encodeURIComponent(id)}`,
    );
    if (lifecycle !== getAuthSessionLifecycle()) throw new SupportError(401);
    return value;
  },
  config: () => request<SupportConfig>('/config'),
  conversations: () => request<{ items: Conversation[] }>('/conversations'),
  conversation: (id: string) =>
    request<ConversationDetail>(`/conversations/${encodeURIComponent(id)}`),
  createConversation: (key: string) =>
    request<Conversation>('/conversations', { idempotency_key: key }),
  sendMessage: (id: string, input: SendMessage) =>
    request<SendResult>(
      `/conversations/${encodeURIComponent(id)}/messages`,
      input,
    ),
  requests: () => request<{ items: Diagnosis[] }>('/requests'),
  diagnosis: (id: string) =>
    request<Diagnosis>(`/diagnostics/${encodeURIComponent(id)}`),
  createTicket: (input: TicketInput) => request<Ticket>('/tickets', input),
};
