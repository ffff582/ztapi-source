/*
Copyright (C) 2025 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/

import React from 'react';
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react';
import AdminChannelsPage from './AdminChannelsPage.jsx';
import { adminRequest } from '../../auth/admin-session.js';

vi.mock('../../auth/admin-session.js', () => ({ adminRequest: vi.fn() }));

const channelFixture = {
  id: 7,
  name: '生产 OpenAI',
  type: 1,
  key: '********',
  status: 1,
  base_url: 'https://api.example.invalid',
  models: 'gpt-4o,gpt-4o-mini',
  group: 'default',
  priority: 100,
  weight: 7,
  model_mapping: '{"zt-gpt-4o":"gpt-4o"}',
  response_time: 238,
  channel_info: {
    is_multi_key: true,
    multi_key_size: 3,
    multi_key_mode: 'random',
  },
};

function listResponse(items = [channelFixture]) {
  return { items, total: items.length, page: 1, page_size: 20 };
}

function renderPage(props = {}) {
  adminRequest.mockResolvedValueOnce(listResponse());
  return render(<AdminChannelsPage canWrite {...props} />);
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

describe('AdminChannelsPage', () => {
  beforeEach(() => vi.clearAllMocks());
  afterEach(cleanup);

  it('lists and searches only the intended upstream channel projection', async () => {
    renderPage();
    expect(await screen.findByText('生产 OpenAI')).toBeVisible();
    expect(screen.getByText('OpenAI')).toBeVisible();
    expect(screen.getByText('3 个密钥（已脱敏）')).toBeVisible();
    expect(screen.queryByText(/New API|部署模型|Ollama|Midjourney/)).toBeNull();

    adminRequest.mockResolvedValueOnce(
      listResponse([
        { ...channelFixture, id: 8, name: 'Claude 主线路', type: 14 },
      ]),
    );
    fireEvent.change(screen.getByRole('searchbox', { name: '搜索上游渠道' }), {
      target: { value: 'Claude' },
    });
    fireEvent.submit(
      screen.getByRole('searchbox', { name: '搜索上游渠道' }).closest('form'),
    );

    expect(await screen.findByText('Claude 主线路')).toBeVisible();
    expect(adminRequest).toHaveBeenLastCalledWith(
      expect.objectContaining({
        method: 'GET',
        url: '/api/channel/ztapi/search?keyword=Claude&p=1&page_size=20',
      }),
    );
  });

  it('creates only OpenAI, Claude, or Gemini upstreams', async () => {
    renderPage();
    await screen.findByText('生产 OpenAI');
    fireEvent.click(screen.getByRole('button', { name: '新建上游渠道' }));

    const dialog = screen.getByRole('dialog', { name: '新建上游渠道' });
    expect(
      within(within(dialog).getByLabelText('上游类型'))
        .getAllByRole('option')
        .map((option) => option.textContent),
    ).toEqual(['OpenAI', 'Claude', 'Gemini']);
    fireEvent.change(within(dialog).getByLabelText('渠道名称'), {
      target: { value: 'Gemini 主线路' },
    });
    fireEvent.change(within(dialog).getByLabelText('上游类型'), {
      target: { value: '24' },
    });
    fireEvent.change(within(dialog).getByLabelText('API 地址'), {
      target: { value: 'https://gemini.example.invalid' },
    });
    fireEvent.change(within(dialog).getByLabelText('API 密钥'), {
      target: { value: 'test-only-credential' },
    });
    fireEvent.change(within(dialog).getByLabelText('模型'), {
      target: { value: 'gemini-test' },
    });
    adminRequest
      .mockResolvedValueOnce(undefined)
      .mockResolvedValueOnce(listResponse());
    fireEvent.click(within(dialog).getByRole('button', { name: '创建渠道' }));

    await waitFor(() =>
      expect(adminRequest).toHaveBeenCalledWith(
        expect.objectContaining({
          method: 'POST',
          url: '/api/channel/ztapi/',
          body: expect.stringContaining('test-only-credential'),
        }),
      ),
    );
  });

  it('edits metadata without receiving or resubmitting the stored credential', async () => {
    renderPage();
    await screen.findByText('生产 OpenAI');
    adminRequest.mockResolvedValueOnce(channelFixture);
    fireEvent.click(screen.getByRole('button', { name: '编辑 生产 OpenAI' }));

    const dialog = await screen.findByRole('dialog', { name: '编辑上游渠道' });
    expect(within(dialog).getByLabelText('API 密钥')).toHaveValue('');
    expect(within(dialog).getByText('留空将保留现有密钥。')).toBeVisible();
    expect(within(dialog).getByLabelText('优先级')).toHaveValue(100);
    expect(within(dialog).getByLabelText('权重')).toHaveValue(7);
    expect(within(dialog).getByLabelText('模型映射')).toHaveValue(
      '{"zt-gpt-4o":"gpt-4o"}',
    );
    fireEvent.change(within(dialog).getByLabelText('渠道名称'), {
      target: { value: '生产 OpenAI 2' },
    });
    fireEvent.change(within(dialog).getByLabelText('优先级'), {
      target: { value: '90' },
    });
    fireEvent.change(within(dialog).getByLabelText('权重'), {
      target: { value: '3' },
    });
    fireEvent.change(within(dialog).getByLabelText('模型映射'), {
      target: { value: '{"zt-gpt-4o":"gpt-4o-2026"}' },
    });
    adminRequest
      .mockResolvedValueOnce(undefined)
      .mockResolvedValueOnce(listResponse());
    fireEvent.click(within(dialog).getByRole('button', { name: '保存渠道' }));

    await waitFor(() =>
      expect(adminRequest).toHaveBeenCalledWith(
        expect.objectContaining({
          method: 'PUT',
          url: '/api/channel/ztapi/7',
          body: expect.stringContaining('"key":""'),
        }),
      ),
    );
    const updateRequest = adminRequest.mock.calls.find(
      ([request]) => request.method === 'PUT',
    )[0];
    const updateBody = JSON.parse(updateRequest.body);
    expect(updateBody.weight).toBe(3);
    expect(updateBody.priority).toBe(90);
    expect(updateBody.model_mapping).toBe('{"zt-gpt-4o":"gpt-4o-2026"}');
    expect(updateBody).not.toHaveProperty('auto_ban');
  });

  it('creates a disabled upstream placeholder without an API key', async () => {
    renderPage();
    await screen.findByText('生产 OpenAI');
    fireEvent.click(screen.getByRole('button', { name: '新建上游渠道' }));

    const dialog = screen.getByRole('dialog', { name: '新建上游渠道' });
    fireEvent.change(within(dialog).getByLabelText('渠道名称'), {
      target: { value: 'APIKEY FUN 候选渠道' },
    });
    fireEvent.change(within(dialog).getByLabelText('API 地址'), {
      target: { value: 'https://api.apikey.fun' },
    });
    fireEvent.change(within(dialog).getByLabelText('模型'), {
      target: { value: 'gpt-pilot' },
    });
    fireEvent.change(within(dialog).getByLabelText('状态'), {
      target: { value: '2' },
    });
    fireEvent.change(within(dialog).getByLabelText('优先级'), {
      target: { value: '100' },
    });
    adminRequest
      .mockResolvedValueOnce(undefined)
      .mockResolvedValueOnce(listResponse());
    fireEvent.click(within(dialog).getByRole('button', { name: '创建渠道' }));

    await waitFor(() =>
      expect(adminRequest).toHaveBeenCalledWith(
        expect.objectContaining({
          method: 'POST',
          url: '/api/channel/ztapi/',
        }),
      ),
    );
    const createRequest = adminRequest.mock.calls.find(
      ([request]) => request.method === 'POST',
    )[0];
    expect(JSON.parse(createRequest.body)).toEqual(
      expect.objectContaining({
        key: '',
        status: 2,
        priority: 100,
        weight: 1,
      }),
    );
  });

  it('enables, disables, tests connectivity, and imports a private discovery snapshot', async () => {
    renderPage();
    await screen.findByText('生产 OpenAI');

    adminRequest
      .mockResolvedValueOnce(undefined)
      .mockResolvedValueOnce(listResponse());
    fireEvent.click(screen.getByRole('button', { name: '禁用 生产 OpenAI' }));
    await waitFor(() =>
      expect(adminRequest).toHaveBeenCalledWith(
        expect.objectContaining({
          method: 'PATCH',
          url: '/api/channel/ztapi/7/status',
          body: expect.stringContaining('"status":2'),
        }),
      ),
    );

    adminRequest
      .mockResolvedValueOnce({ time: 0.238 })
      .mockResolvedValueOnce(listResponse());
    fireEvent.click(screen.getByRole('button', { name: '测试 生产 OpenAI' }));
    expect(await screen.findByText('连接正常，耗时 0.238 秒。')).toBeVisible();

    adminRequest
      .mockResolvedValueOnce({
        snapshot_id: 91,
        channel_id: 7,
        model_list_hash: 'a'.repeat(64),
        imported_count: 2,
        model_ids: ['gpt-4.1', 'gpt-4o'],
        fetched_at: 1787770000,
      })
      .mockResolvedValueOnce(listResponse());
    fireEvent.click(
      screen.getByRole('button', { name: '获取并导入模型 生产 OpenAI' }),
    );
    expect(
      await screen.findByText(
        '已导入 2 个模型并生成快照 #91，全部保持未发布。',
      ),
    ).toBeVisible();
    expect(screen.getByText('快照 #91 · 已私有导入 2 个')).toBeVisible();
    expect(adminRequest).toHaveBeenCalledWith({
      method: 'GET',
      url: '/api/channel/ztapi/fetch_models/7?import=true',
    });
  });

  it('compares two immutable discovery snapshots without batch publishing', async () => {
    const second = {
      ...channelFixture,
      id: 8,
      name: '备用 OpenAI',
      models: 'gpt-4o,gpt-5',
    };
    adminRequest.mockResolvedValueOnce(listResponse([channelFixture, second]));
    render(<AdminChannelsPage canWrite />);
    await screen.findByText('备用 OpenAI');

    adminRequest
      .mockResolvedValueOnce({
        snapshot_id: 91,
        channel_id: 7,
        imported_count: 2,
        model_ids: ['gpt-4.1', 'gpt-4o'],
        fetched_at: 100,
      })
      .mockResolvedValueOnce(listResponse([channelFixture, second]));
    fireEvent.click(
      screen.getByRole('button', { name: '获取并导入模型 生产 OpenAI' }),
    );
    await screen.findByText('快照 #91 · 已私有导入 2 个');

    adminRequest
      .mockResolvedValueOnce({
        snapshot_id: 92,
        channel_id: 8,
        imported_count: 2,
        model_ids: ['gpt-4o', 'gpt-5'],
        fetched_at: 200,
      })
      .mockResolvedValueOnce(listResponse([channelFixture, second]));
    fireEvent.click(
      screen.getByRole('button', { name: '获取并导入模型 备用 OpenAI' }),
    );

    const comparison = await screen.findByLabelText('上游模型快照对比');
    expect(within(comparison).getByText('共同模型 1 个')).toBeVisible();
    expect(within(comparison).getByText('备用 OpenAI 独有 1 个')).toBeVisible();
    expect(within(comparison).getByText('生产 OpenAI 独有 1 个')).toBeVisible();
    expect(
      within(comparison).getByText('对比只用于目录治理，不会自动发布模型。'),
    ).toBeVisible();
  });

  it.each([
    ['dns', 'DNS 解析失败'],
    ['connect_timeout', '连接上游超时'],
    ['authentication', '上游身份验证失败'],
    ['rate_limit', '上游触发限流'],
    ['upstream_response', '上游响应异常'],
  ])(
    'renders %s failures without raw response bodies',
    async (category, label) => {
      renderPage();
      await screen.findByText('生产 OpenAI');
      adminRequest.mockResolvedValueOnce({
        error_category: category,
        message: 'raw upstream SECRET_BODY',
      });
      fireEvent.click(screen.getByRole('button', { name: '测试 生产 OpenAI' }));

      expect(await screen.findByText(label)).toBeVisible();
      expect(screen.queryByText(/SECRET_BODY/)).toBeNull();
    },
  );

  it('removes every usable write control for read-only channel operators', async () => {
    adminRequest.mockResolvedValueOnce(listResponse());
    render(<AdminChannelsPage canWrite={false} />);

    expect(await screen.findByText('生产 OpenAI')).toBeVisible();
    expect(screen.queryByRole('button', { name: '新建上游渠道' })).toBeNull();
    expect(
      screen.queryByRole('button', { name: /编辑|禁用|启用|测试|获取.*模型/ }),
    ).toBeNull();
    expect(screen.getByText('只读权限')).toBeVisible();
  });

  it('does not let an older search overwrite the latest result', async () => {
    renderPage();
    await screen.findByText('生产 OpenAI');
    const older = deferred();
    const latest = deferred();
    adminRequest
      .mockImplementationOnce(() => older.promise)
      .mockImplementationOnce(() => latest.promise);

    const search = screen.getByRole('searchbox', { name: '搜索上游渠道' });
    fireEvent.change(search, { target: { value: '旧查询' } });
    fireEvent.submit(search.closest('form'));
    fireEvent.change(search, { target: { value: '新查询' } });
    fireEvent.submit(search.closest('form'));

    await act(async () => {
      latest.resolve(
        listResponse([{ ...channelFixture, id: 9, name: '最新结果' }]),
      );
    });
    expect(await screen.findByText('最新结果')).toBeVisible();

    await act(async () => {
      older.resolve(
        listResponse([{ ...channelFixture, id: 10, name: '过期结果' }]),
      );
    });
    expect(screen.getByText('最新结果')).toBeVisible();
    expect(screen.queryByText('过期结果')).toBeNull();
  });

  it('does not let an older detail request replace the latest edit dialog', async () => {
    const first = { ...channelFixture, id: 7, name: '渠道 A' };
    const second = { ...channelFixture, id: 8, name: '渠道 B' };
    adminRequest.mockResolvedValueOnce(listResponse([first, second]));
    render(<AdminChannelsPage canWrite />);
    await screen.findByText('渠道 B');

    const older = deferred();
    const latest = deferred();
    adminRequest
      .mockImplementationOnce(() => older.promise)
      .mockImplementationOnce(() => latest.promise);
    fireEvent.click(screen.getByRole('button', { name: '编辑 渠道 A' }));
    fireEvent.click(screen.getByRole('button', { name: '编辑 渠道 B' }));

    await act(async () => latest.resolve(second));
    const dialog = await screen.findByRole('dialog', { name: '编辑上游渠道' });
    expect(within(dialog).getByLabelText('渠道名称')).toHaveValue('渠道 B');

    await act(async () => older.resolve(first));
    expect(within(dialog).getByLabelText('渠道名称')).toHaveValue('渠道 B');
  });

  it('manages dialog focus, traps Tab, closes on Escape, and restores focus', async () => {
    renderPage();
    await screen.findByText('生产 OpenAI');
    const invoker = screen.getByRole('button', { name: '新建上游渠道' });
    invoker.focus();
    fireEvent.click(invoker);

    const dialog = screen.getByRole('dialog', { name: '新建上游渠道' });
    const nameInput = within(dialog).getByLabelText('渠道名称');
    expect(nameInput).toHaveFocus();

    const closeButton = within(dialog).getByRole('button', {
      name: '关闭渠道表单',
    });
    const submitButton = within(dialog).getByRole('button', {
      name: '创建渠道',
    });
    closeButton.focus();
    fireEvent.keyDown(dialog, { key: 'Tab', shiftKey: true });
    expect(submitButton).toHaveFocus();
    fireEvent.keyDown(dialog, { key: 'Tab' });
    expect(closeButton).toHaveFocus();

    fireEvent.keyDown(dialog, { key: 'Escape' });
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(invoker).toHaveFocus();
  });
});
