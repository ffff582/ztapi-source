/*
Copyright (C) 2025 QuantumNous
SPDX-License-Identifier: AGPL-3.0-or-later
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
import MediaContractDialog from './MediaContractDialog.jsx';
import { adminRequest } from '../../auth/admin-session.js';

vi.mock('../../auth/admin-session.js', async (importOriginal) => {
  const actual = await importOriginal();
  return { ...actual, adminRequest: vi.fn() };
});

const modelFixture = {
  id: 31,
  source_model: 'gp-image-2',
  public_name: 'zt-image-pro',
  modality: 'image',
  published: true,
};

const contractFixture = {
  model_id: 31,
  public_name: 'zt-image-pro',
  modality: 'image',
  published: true,
  quotation_sheet: '国外模型',
  quotation_cell: 'C42',
  quotation_label: 'gp-image-2',
  quotation_resource: '企业资源',
  quotation_discount_percent: '40',
  quotation_currency: 'USD',
  quotation_effective_at: 1787000000,
  source_document_checksum: 'a'.repeat(64),
  price_policy: 'enterprise_40_margin',
  price_source_version: 3,
  frozen_pricing_version: 'ztapi-snapshot-91',
  protocol_evidence_sha256: 'b'.repeat(64),
  pending_reconciliation_count: 2,
};

const healthFixture = {
  model_id: 31,
  enabled: true,
  observed: true,
  state: {
    model_id: 31,
    generation: 4,
    open: true,
    completion_sequence: 12,
    consecutive_failures: 2,
    incident_id: 7,
    updated_at: 1787000200,
  },
  window: {
    valid_samples: 20,
    failures: 2,
    window_start: 1786913800,
    window_end: 1787000200,
  },
  coverage: [],
  events: [
    {
      id: 44,
      completion_sequence: 12,
      generation: 4,
      config_version: 7,
      result: 'failure',
      reason: 'empty_output',
      source: 'real',
      stream: true,
      counted: true,
      finish_reasons: ['length'],
      completed_at: 1787000200,
    },
  ],
  incidents: [],
  outbox: [],
};

function installMocks() {
  adminRequest.mockImplementation(({ method, url }) => {
    if (method === 'GET' && url === '/api/models/ztapi/31/media-contract') {
      return Promise.resolve(contractFixture);
    }
    if (method === 'GET' && url === '/api/models/ztapi/31/health') {
      return Promise.resolve(healthFixture);
    }
    return Promise.reject(new Error(`unexpected request ${method} ${url}`));
  });
}

describe('MediaContractDialog', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    installMocks();
  });
  afterEach(cleanup);

  it('shows quotation, frozen pricing, protocol, reconciliation, and latest health evidence', async () => {
    render(
      <MediaContractDialog
        model={modelFixture}
        canWrite
        onClose={vi.fn()}
        onOpenWorkflow={vi.fn()}
        onOpenHealth={vi.fn()}
      />,
    );

    const dialog = await screen.findByRole('dialog', { name: '媒体产品证据' });
    expect(await within(dialog).findByText('国外模型 / C42')).toBeVisible();
    expect(within(dialog).getByText('企业 40% 毛利')).toBeVisible();
    expect(within(dialog).getByText('ztapi-snapshot-91')).toBeVisible();
    expect(within(dialog).getByText('b'.repeat(64))).toBeVisible();
    expect(within(dialog).getByText('2 笔待对账')).toBeVisible();
    expect(within(dialog).getByText('已熔断')).toBeVisible();
    expect(within(dialog).getByText('有效失败')).toBeVisible();
    expect(within(dialog).getByText('length')).toBeVisible();
  });

  it('routes manual publication and recovery through the existing controls', async () => {
    const onOpenWorkflow = vi.fn();
    const onOpenHealth = vi.fn();
    render(
      <MediaContractDialog
        model={modelFixture}
        canWrite
        onClose={vi.fn()}
        onOpenWorkflow={onOpenWorkflow}
        onOpenHealth={onOpenHealth}
      />,
    );
    const dialog = await screen.findByRole('dialog', { name: '媒体产品证据' });
    await within(dialog).findByText('国外模型 / C42');
    fireEvent.click(
      within(dialog).getByRole('button', { name: '打开发布工作流' }),
    );
    fireEvent.click(
      within(dialog).getByRole('button', { name: '打开健康恢复' }),
    );
    expect(onOpenWorkflow).toHaveBeenCalledWith(modelFixture);
    expect(onOpenHealth).toHaveBeenCalledWith(modelFixture);
  });

  it('keeps evidence visible but hides mutation controls without model.write', async () => {
    render(
      <MediaContractDialog
        model={modelFixture}
        canWrite={false}
        onClose={vi.fn()}
        onOpenWorkflow={vi.fn()}
        onOpenHealth={vi.fn()}
      />,
    );
    const dialog = await screen.findByRole('dialog', { name: '媒体产品证据' });
    expect(await within(dialog).findByText('国外模型 / C42')).toBeVisible();
    expect(within(dialog).getByText('只读权限')).toBeVisible();
    expect(
      within(dialog).queryByRole('button', { name: '打开发布工作流' }),
    ).toBeNull();
    expect(
      within(dialog).queryByRole('button', { name: '打开健康恢复' }),
    ).toBeNull();
  });

  it('restores focus when closed', async () => {
    const invoker = document.createElement('button');
    document.body.append(invoker);
    invoker.focus();
    const onClose = vi.fn();
    const { unmount } = render(
      <MediaContractDialog
        model={modelFixture}
        canWrite
        onClose={onClose}
        onOpenWorkflow={vi.fn()}
        onOpenHealth={vi.fn()}
        returnFocusTo={invoker}
      />,
    );
    await screen.findByRole('dialog', { name: '媒体产品证据' });
    fireEvent.keyDown(screen.getByRole('dialog', { name: '媒体产品证据' }), {
      key: 'Escape',
    });
    expect(onClose).toHaveBeenCalledTimes(1);
    unmount();
    await waitFor(() => expect(invoker).toHaveFocus());
    invoker.remove();
  });
});
