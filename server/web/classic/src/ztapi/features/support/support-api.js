import {
  adminRequest,
  clearAdminSession,
  getAdminSession,
} from '../../auth/admin-session';

export class SupportAdminError extends Error {
  constructor(status = 0) {
    super('客服工单请求失败');
    this.status = status;
  }
}
async function request(path, body) {
  const session = getAdminSession();
  if (!session) throw new SupportAdminError(401);
  const headers = new Headers({
    Accept: 'application/json',
    Authorization: `Bearer ${session.access_token}`,
    'New-API-User': String(session.user.id),
  });
  if (body !== undefined) headers.set('Content-Type', 'application/json');
  let response;
  try {
    response = await fetch(`/api/admin/support/tickets${path}`, {
      method: body === undefined ? 'GET' : 'POST',
      credentials: 'include',
      cache: 'no-store',
      headers,
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    });
  } catch {
    throw new SupportAdminError();
  }
  if (getAdminSession() !== session) throw new SupportAdminError(401);
  if (response.status === 401) clearAdminSession();
  if (!response.ok) throw new SupportAdminError(response.status);
  let envelope;
  try {
    envelope = await response.json();
  } catch {
    throw new SupportAdminError(response.status);
  }
  if (getAdminSession() !== session) throw new SupportAdminError(401);
  if (envelope?.success !== true || envelope.data == null)
    throw new SupportAdminError(response.status);
  return envelope.data;
}
export const supportAdminApi = {
  async attachment(id) {
    const userId = getAdminSession()?.user.id;
    if (!userId) throw new SupportAdminError(401);
    const value = await adminRequest({
      url: `/api/admin/support/attachments/${encodeURIComponent(id)}`,
    });
    if (getAdminSession()?.user.id !== userId) throw new SupportAdminError(401);
    return value;
  },
  list: (status = 'open', afterId = 0) =>
    request(
      `?${new URLSearchParams({ status, after_id: String(afterId), limit: '20' })}`,
    ),
  detail: (id) => request(`/${encodeURIComponent(id)}`),
  reply: (id, input) => request(`/${encodeURIComponent(id)}/replies`, input),
  status: (id, input) => request(`/${encodeURIComponent(id)}/status`, input),
};
export function supportAdminErrorMessage(error) {
  if (error?.status === 401) return '管理员登录已失效，请重新登录。';
  if (error?.status === 403) return '无权管理客服工单。';
  if (error?.status === 404) return '工单不存在或无权访问。';
  if (error?.status === 429) return '请求过于频繁，请稍后操作。';
  return '工单请求失败，结果尚未确认，请刷新核实或显式重试原操作。';
}
