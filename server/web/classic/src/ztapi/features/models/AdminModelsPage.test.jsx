/*
Copyright (C) 2025 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.
*/

import React from 'react';
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react';
import AdminModelsPage from './AdminModelsPage.jsx';
import { adminRequest, AdminSessionError } from '../../auth/admin-session.js';

vi.mock('../../auth/admin-session.js', async (importOriginal) => {
  const actual = await importOriginal();
  return { ...actual, adminRequest: vi.fn() };
});

const modelFixture = {
  id: 17,
  version: 4,
  source_model: 'gpt-4.1-mini',
  public_name: '',
  family: 'openai',
  protocol: 'openai_compatible',
  provider_family: 'openai',
  input_cost_per_million: 0,
  output_cost_per_million: 0,
  input_price_per_million: 0,
  output_price_per_million: 0,
  cache_read_ratio: 1,
  cache_creation_ratio: 1,
  cache_creation_5m_ratio: 1,
  cache_creation_1h_ratio: 1,
  image_ratio: 1,
  audio_ratio: 1,
  audio_completion_ratio: 1,
  enabled_groups: [],
  published: false,
  route_ready: false,
  enabled_route_count: 0,
  publication_blockers: ['identity_missing', 'price_source_missing'],
};

const verifiedFixture = {
  ...modelFixture,
  version: 9,
  public_name: 'zt-gpt-fast',
  input_cost_per_million: 0.4,
  output_cost_per_million: 1.6,
  input_price_per_million: 0.52,
  output_price_per_million: 2.08,
  enabled_groups: ['default'],
  route_ready: true,
  enabled_route_count: 1,
  publication_blockers: [],
};

const mediaFixture = {
  ...verifiedFixture,
  id: 31,
  source_model: 'gp-image-2',
  public_name: 'zt-image-pro',
  modality: 'image',
};

const auditFixture = {
  items: [
    {
      id: 8,
      action: 'model.identity_mapped',
      model_config_id: 17,
      public_name: 'zt-gpt-fast',
      operator_id: 2,
      created_at: 1787770000,
    },
  ],
};

function listResponse(items = [modelFixture]) {
  return { items, total: items.length, page: 1, page_size: 100 };
}

function channelResponse() {
  return {
    items: [
      {
        id: 3,
        name: '云信号池',
        models: 'gpt-4.1-mini,claude-sonnet',
      },
    ],
  };
}

function installReadMocks(items = [modelFixture], detail = items[0]) {
  adminRequest.mockImplementation(({ method, url }) => {
    if (url === '/api/models/ztapi/?page=1&page_size=100&keyword=') {
      return Promise.resolve(listResponse(items));
    }
    if (url === '/api/models/ztapi/audit-events?page=1&page_size=8') {
      return Promise.resolve(auditFixture);
    }
    if (method === 'GET' && url === `/api/models/ztapi/${detail.id}`) {
      return Promise.resolve(detail);
    }
    if (url === '/api/channel/ztapi/?p=1&page_size=100') {
      return Promise.resolve(channelResponse());
    }
    return Promise.reject(new Error(`unexpected request ${method} ${url}`));
  });
}

describe('AdminModelsPage evidence workflow', () => {
  beforeEach(() => vi.clearAllMocks());
  afterEach(cleanup);

  it('shows workflow states, provider/protocol filters, blockers, and audit history', async () => {
    const claude = {
      ...verifiedFixture,
      id: 18,
      source_model: 'claude-sonnet',
      public_name: 'zt-claude-pro',
      family: 'claude',
      protocol: 'anthropic',
      provider_family: 'anthropic',
    };
    installReadMocks([modelFixture, claude]);
    render(<AdminModelsPage canWrite />);

    const table = await screen.findByRole('table', {
      name: '模型上线证据列表',
    });
    expect(within(table).getByText('已发现')).toBeVisible();
    expect(within(table).getByText('已验证')).toBeVisible();
    expect(
      within(table).getByText(/尚未确认公开名称、协议和供应商/),
    ).toBeVisible();
    expect(screen.getByText('完成身份映射')).toBeVisible();

    fireEvent.change(screen.getByLabelText('供应商筛选'), {
      target: { value: 'anthropic' },
    });
    expect(screen.getByText('zt-claude-pro')).toBeVisible();
    expect(screen.queryByText('gpt-4.1-mini')).toBeNull();

    fireEvent.change(screen.getByLabelText('流程状态筛选'), {
      target: { value: 'published' },
    });
    expect(screen.getByText('没有匹配当前筛选条件的模型。')).toBeVisible();
  });

  it('keeps the whole evidence workflow read-only without model.write', async () => {
    installReadMocks();
    render(<AdminModelsPage canWrite={false} />);

    expect(await screen.findByText('只读权限')).toBeVisible();
    expect(screen.queryByRole('button', { name: /配置/ })).toBeNull();
  });

  it('opens media evidence from a quotation-backed candidate row', async () => {
    installReadMocks([mediaFixture], mediaFixture);
    const fallback = adminRequest.getMockImplementation();
    adminRequest.mockImplementation((request) => {
      if (
        request.method === 'GET' &&
        request.url === '/api/models/ztapi/31/media-contract'
      ) {
        return Promise.resolve({
          model_id: 31,
          public_name: 'zt-image-pro',
          modality: 'image',
          quotation_sheet: '国外模型',
          quotation_cell: 'C42',
          price_policy: 'enterprise_40_margin',
          pending_reconciliation_count: 0,
        });
      }
      if (
        request.method === 'GET' &&
        request.url === '/api/models/ztapi/31/health'
      ) {
        return Promise.resolve({
          model_id: 31,
          enabled: true,
          observed: false,
          state: null,
          window: {},
          coverage: [],
          events: [],
          incidents: [],
          outbox: [],
        });
      }
      return fallback(request);
    });
    render(<AdminModelsPage canWrite />);

    const button = await screen.findByRole('button', {
      name: '媒体证据 zt-image-pro',
    });
    fireEvent.click(button);
    const dialog = await screen.findByRole('dialog', { name: '媒体产品证据' });
    expect(await within(dialog).findByText('国外模型 / C42')).toBeVisible();
  });

  it('maps identity with explicit evidence and optimistic versioning', async () => {
    let current = modelFixture;
    installReadMocks([modelFixture], current);
    const fallback = adminRequest.getMockImplementation();
    adminRequest.mockImplementation((request) => {
      if (
        request.method === 'PUT' &&
        request.url === '/api/models/ztapi/17/identity'
      ) {
        current = {
          ...current,
          version: 5,
          public_name: 'zt-gpt-fast',
          publication_blockers: ['price_source_missing'],
        };
        return Promise.resolve({ model: current });
      }
      if (request.method === 'GET' && request.url === '/api/models/ztapi/17') {
        return Promise.resolve(current);
      }
      return fallback(request);
    });
    render(<AdminModelsPage canWrite />);
    await screen.findByText('gpt-4.1-mini');
    fireEvent.click(screen.getByRole('button', { name: '配置 gpt-4.1-mini' }));

    const dialog = await screen.findByRole('dialog', {
      name: '模型上线工作流',
    });
    fireEvent.change(within(dialog).getByLabelText('公开模型名'), {
      target: { value: 'zt-gpt-fast' },
    });
    fireEvent.change(within(dialog).getByLabelText('证据来源'), {
      target: { value: '云信报价单-2026-08' },
    });
    fireEvent.change(within(dialog).getByLabelText('变更原因'), {
      target: { value: '首次建立公开映射' },
    });
    fireEvent.click(
      within(dialog).getByLabelText('我已核对上游模型、协议和供应商'),
    );
    fireEvent.click(
      within(dialog).getByRole('button', { name: '保存身份映射' }),
    );

    await waitFor(
      () => {
        const request = adminRequest.mock.calls.find(
          ([value]) => value.url === '/api/models/ztapi/17/identity',
        )?.[0];
        expect(request).toBeDefined();
        expect(JSON.parse(request.body)).toEqual({
          version: 4,
          source_model: 'gpt-4.1-mini',
          public_name: 'zt-gpt-fast',
          protocol: 'openai_compatible',
          provider_family: 'openai',
          source_reference: '云信报价单-2026-08',
          reason: '首次建立公开映射',
          confirm: true,
        });
      },
      { timeout: 3000 },
    );
    expect(await screen.findByText('身份映射证据已保存。')).toBeVisible();
  });

  it('previews and imports immutable price evidence at a 40 percent gross margin', async () => {
    let current = {
      ...modelFixture,
      ...verifiedFixture,
      publication_blockers: ['price_source_missing'],
    };
    installReadMocks([current], current);
    const fallback = adminRequest.getMockImplementation();
    adminRequest.mockImplementation((request) => {
      if (request.url.endsWith('/price-preview')) {
        return Promise.resolve({
          input_sale_usd_per_million: '0.6666666667',
          output_sale_usd_per_million: '2.6666666667',
        });
      }
      if (request.url.endsWith('/price-sources')) {
        current = { ...current, version: 10, publication_blockers: [] };
        return Promise.resolve({
          preview: {
            input_sale_usd_per_million: '0.6666666667',
            output_sale_usd_per_million: '2.6666666667',
          },
        });
      }
      if (request.method === 'GET' && request.url === '/api/models/ztapi/17')
        return Promise.resolve(current);
      return fallback(request);
    });
    render(<AdminModelsPage canWrite />);
    await screen.findByRole('button', { name: '配置 zt-gpt-fast' });
    fireEvent.click(screen.getByRole('button', { name: '配置 zt-gpt-fast' }));
    const dialog = await screen.findByRole('dialog', {
      name: '模型上线工作流',
    });

    fireEvent.change(within(dialog).getByLabelText('输入 / 百万 Token'), {
      target: { value: '0.4' },
    });
    fireEvent.change(within(dialog).getByLabelText('输出 / 百万 Token'), {
      target: { value: '1.6' },
    });
    fireEvent.change(within(dialog).getByLabelText('报价文件 SHA-256'), {
      target: { value: 'a'.repeat(64) },
    });
    fireEvent.change(within(dialog).getByLabelText('导入原因'), {
      target: { value: '导入朋友书面确认的报价' },
    });
    fireEvent.click(
      within(dialog).getByRole('button', { name: '预览 40% 毛利售价' }),
    );

    expect(
      await within(dialog).findByText('输入 $0.6666666667 / 百万 Token'),
    ).toBeVisible();
    fireEvent.click(
      within(dialog).getByLabelText('我已核对报价来源和计费维度'),
    );
    fireEvent.click(
      within(dialog).getByRole('button', { name: '导入报价证据' }),
    );

    await waitFor(() => {
      const request = adminRequest.mock.calls.find(([value]) =>
        value.url.endsWith('/price-sources'),
      )?.[0];
      const payload = JSON.parse(request.body);
      expect(payload.billing_dimensions).toEqual([
        'input_tokens',
        'output_tokens',
      ]);
      expect(payload.input_per_million).toBe('0.4');
      expect(payload.output_per_million).toBe('1.6');
      expect(payload.confirm).toBe(true);
    });
  });

  it('verifies against a managed channel and publishes only to explicit groups', async () => {
    let current = {
      ...verifiedFixture,
      publication_blockers: ['verification_streaming_missing'],
    };
    installReadMocks([current], current);
    const fallback = adminRequest.getMockImplementation();
    adminRequest.mockImplementation((request) => {
      if (request.url.endsWith('/verify')) {
        current = { ...current, version: 10, publication_blockers: [] };
        return Promise.resolve({ verification: { status: 'verified' } });
      }
      if (request.method === 'PUT' && request.url === '/api/models/ztapi/17') {
        current = {
          ...current,
          version: 11,
          published: true,
          enabled_groups: ['default', 'vip'],
        };
        return Promise.resolve(current);
      }
      if (request.method === 'GET' && request.url === '/api/models/ztapi/17')
        return Promise.resolve(current);
      return fallback(request);
    });
    render(<AdminModelsPage canWrite />);
    await screen.findByRole('button', { name: '配置 zt-gpt-fast' });
    fireEvent.click(screen.getByRole('button', { name: '配置 zt-gpt-fast' }));
    const dialog = await screen.findByRole('dialog', {
      name: '模型上线工作流',
    });

    await within(dialog).findByRole('option', {
      name: '云信号池（已发现该模型）',
    });
    fireEvent.click(within(dialog).getByLabelText('对外发布'));
    await waitFor(() =>
      expect(
        within(dialog).getByRole('button', { name: '发布到指定用户组' }),
      ).toBeDisabled(),
    );
    fireEvent.click(within(dialog).getByRole('button', { name: '开始验证' }));
    await waitFor(() => {
      const request = adminRequest.mock.calls.find(([value]) =>
        value.url.endsWith('/verify'),
      )?.[0];
      expect(JSON.parse(request.body)).toEqual({ channel_id: 3 });
    });
    expect(
      await within(dialog).findByText('全部证据已通过，可以选择用户组发布。'),
    ).toBeVisible();

    fireEvent.change(within(dialog).getByLabelText('明确用户组'), {
      target: { value: 'default,vip' },
    });
    const publishButton = await waitFor(() =>
      within(dialog).getByRole('button', { name: '发布到指定用户组' }),
    );
    fireEvent.click(publishButton);
    await waitFor(() => {
      const request = adminRequest.mock.calls.find(
        ([value]) =>
          value.method === 'PUT' && value.url === '/api/models/ztapi/17',
      )?.[0];
      const payload = JSON.parse(request.body);
      expect(payload.enabled_groups).toEqual(['default', 'vip']);
      expect(payload.published).toBe(true);
    });
  });

  it('handles optimistic conflicts by reloading authoritative evidence', async () => {
    let detailReads = 0;
    installReadMocks([verifiedFixture], verifiedFixture);
    const fallback = adminRequest.getMockImplementation();
    adminRequest.mockImplementation((request) => {
      if (request.method === 'PUT' && request.url === '/api/models/ztapi/17') {
        return Promise.reject(new AdminSessionError(409));
      }
      if (request.method === 'GET' && request.url === '/api/models/ztapi/17') {
        detailReads += 1;
        return Promise.resolve(
          detailReads === 1
            ? verifiedFixture
            : {
                ...verifiedFixture,
                version: 10,
                public_name: 'zt-authoritative',
              },
        );
      }
      return fallback(request);
    });
    render(<AdminModelsPage canWrite />);
    await screen.findByRole('button', { name: '配置 zt-gpt-fast' });
    fireEvent.click(screen.getByRole('button', { name: '配置 zt-gpt-fast' }));
    const dialog = await screen.findByRole('dialog', {
      name: '模型上线工作流',
    });
    fireEvent.click(within(dialog).getByLabelText('对外发布'));
    fireEvent.click(
      within(dialog).getByRole('button', { name: '发布到指定用户组' }),
    );
    expect(await within(dialog).findByRole('alert')).toHaveTextContent(
      '模型已被其他管理员更新',
    );
    fireEvent.click(
      within(dialog).getByRole('button', { name: '重新加载最新证据' }),
    );
    expect(await within(dialog).findByText('zt-authoritative')).toBeVisible();
  });
});
