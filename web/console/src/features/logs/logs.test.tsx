import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
} from '@testing-library/react';
import { RouterProvider } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { clearAuthSession } from '../../api/client';
import { AppProviders } from '../../app/providers';
import { createZTAPIRouter } from '../../app/router';

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function authResponse() {
  return jsonResponse({
    success: true,
    data: {
      access_token: 'memory-only-session',
      expires_in: 900,
      user: {
        id: 41,
        username: 'alice',
        role: 1,
        group: 'default',
      },
    },
  });
}

function logItem(requestID: string, timestamp: number) {
  return {
    timestamp,
    request_id: requestID,
    model: 'gpt-4o',
    status: 'success',
    latency: 2,
    prompt_tokens: 10,
    completion_tokens: 5,
    total_tokens: 15,
    billed_amount: 0.25,
  };
}

function logPageResponse(page: number) {
  return jsonResponse({
    success: true,
    data: {
      page,
      page_size: 50,
      total: 51,
      items:
        page === 1
          ? [logItem('ztapi-newest', 1_900_000_100)]
          : [logItem('ztapi-oldest', 1_900_000_000)],
    },
  });
}

afterEach(() => {
  cleanup();
  clearAuthSession();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('ZTAPI user log pagination', () => {
  it('navigates across the first and last log pages', async () => {
    let resolveAuth!: (response: Response) => void;
    const pendingAuth = new Promise<Response>((resolve) => {
      resolveAuth = resolve;
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input.toString();
      if (url.endsWith('/api/auth/refresh')) {
        return pendingAuth;
      }
      if (url.includes('/api/log/self?')) {
        const page = new URL(url, 'http://ztapi.test').searchParams.get('p');
        return logPageResponse(page === '2' ? 2 : 1);
      }
      return jsonResponse({ success: false, message: 'unexpected' }, 500);
    });
    vi.stubGlobal('fetch', fetchMock);
    render(
      <AppProviders>
        <RouterProvider router={createZTAPIRouter(['/console/logs'])} />
      </AppProviders>,
    );

    await act(async () => {
      resolveAuth(authResponse());
      await pendingAuth;
    });
    expect(screen.getByText('ztapi-newest')).toBeVisible();
    expect(screen.getByText('第 1 / 2 页')).toBeVisible();
    expect(screen.getByRole('button', { name: '上一页' })).toBeDisabled();
    expect(screen.getByRole('button', { name: '下一页' })).toBeEnabled();

    fireEvent.click(screen.getByRole('button', { name: '下一页' }));
    expect(await screen.findByText('ztapi-oldest')).toBeVisible();
    expect(screen.getByText('第 2 / 2 页')).toBeVisible();
    expect(screen.getByRole('button', { name: '上一页' })).toBeEnabled();
    expect(screen.getByRole('button', { name: '下一页' })).toBeDisabled();

    fireEvent.click(screen.getByRole('button', { name: '上一页' }));
    expect(await screen.findByText('ztapi-newest')).toBeVisible();
  });
});
