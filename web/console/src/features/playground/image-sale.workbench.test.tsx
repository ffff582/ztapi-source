import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, expect, it, vi } from 'vitest';
import { clearAuthSession, setAuthSession } from '../../api/client';
import { partialImageItem } from '../models/image-sale.fixture';
import { PlaygroundPage } from './PlaygroundPage';

afterEach(() => { cleanup(); clearAuthSession(); vi.unstubAllGlobals(); });

it('limits v2 controls, rejects long prompts and pixels, switches edit back to 1K without paid calls', async () => {
  setAuthSession({ access_token: 'offline', expires_in: 900, user: { id: 7, username: 'test', role: 1, group: 'default' } });
  const calls: string[] = [];
  const item = partialImageItem();
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input); calls.push(url);
    const data = url === '/api/user/models' ? { success: true, data: [item.model_name], catalog: [item] }
      : url.startsWith('/api/token/') ? { success: true, data: { page: 1, page_size: 100, total: 1, items: [{ id: 1, user_id: 7, status: 1, name: 'test', created_time: 1, accessed_time: 0, expired_time: -1, remain_quota: 100, unlimited_quota: true, model_limits_enabled: false, model_limits: '', allow_ips: '', key_prefix: 'sk-zt-offline...' }] } }
        : null;
    if (!data) throw new Error(`Unexpected request ${url}`);
    return new Response(JSON.stringify(data), { headers: { 'Content-Type': 'application/json' } });
  }));
  const close = vi.fn();
  const decode = vi.fn().mockResolvedValue({ width: 2048, height: 2048, close });
  vi.stubGlobal('createImageBitmap', decode);
  render(<MemoryRouter><PlaygroundPage initialMode="image" workbench /></MemoryRouter>);
  await screen.findByLabelText('图片数量');
  expect(within(screen.getByLabelText('图片数量')).getAllByRole('option')).toHaveLength(1);
  expect(within(screen.getByLabelText('图片分辨率')).queryByText('4K')).toBeNull();
  expect(screen.getByText(/0.30 U/)).toBeVisible();
  fireEvent.change(screen.getByLabelText('图片提示词'), { target: { value: '中'.repeat(2001) } });
  await waitFor(() => expect(screen.getByRole('button', { name: '生成图片' })).toBeEnabled());
  fireEvent.click(screen.getByRole('button', { name: '生成图片' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('超过 2000');
  fireEvent.change(screen.getByLabelText('图片分辨率'), { target: { value: '2K' } });
  const file = new File(['offline'], 'ref.png', { type: 'image/png' });
  decode.mockResolvedValueOnce({ width: 2049, height: 2048, close });
  await act(async () => { fireEvent.change(screen.getByLabelText('上传参考图'), { target: { files: [file] } }); });
  expect(screen.getByText('参考图最多 4194304 像素。')).toBeVisible();
  expect(screen.queryByAltText('参考图 1')).toBeNull();
  await act(async () => { fireEvent.change(screen.getByLabelText('上传参考图'), { target: { files: [file] } }); });
  await screen.findByAltText('参考图 1');
  await waitFor(() => expect(screen.getByLabelText('图片分辨率')).toHaveValue('1K'));
  expect(within(screen.getByLabelText('图片分辨率')).getAllByRole('option')).toHaveLength(1);
  fireEvent.change(screen.getByLabelText('上传参考图'), { target: { files: [file] } });
  expect(await screen.findByText('最多上传 1 张参考图。')).toBeVisible();
  expect(calls.every((url) => url.startsWith('/api/token/') || url === '/api/user/models')).toBe(true);
  expect(close).toHaveBeenCalled();
});
