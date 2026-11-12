import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { PublicDocsPage } from './PublicDocsPage';

const sourceCommit = '0123456789abcdef0123456789abcdef01234567';
const sourceRepository = 'https://github.com/ffff582/ztapi-source';
const sourceMetadata = {
  success: true,
  data: {
    license: 'AGPL-3.0-or-later',
    source_repository: sourceRepository,
    source_commit: sourceCommit,
    source_url: `${sourceRepository}/tree/${sourceCommit}`,
  },
};

function response(body: unknown = sourceMetadata, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

function renderLicense(fetchMock: ReturnType<typeof vi.fn>) {
  vi.stubGlobal('fetch', fetchMock);
  return render(<MemoryRouter initialEntries={['/docs/license']}>
    <Routes><Route path="/docs/:section" element={<PublicDocsPage />} /></Routes>
  </MemoryRouter>);
}

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('current corresponding-source offer', () => {
  it('offers only the exact verified source commit using an anonymous non-redirecting GET', async () => {
    const fetchMock = vi.fn().mockResolvedValue(response());
    renderLicense(fetchMock);

    const link = await screen.findByRole('link', { name: '获取当前版本对应源码' });
    expect(link).toHaveAttribute('href', `${sourceRepository}/tree/${sourceCommit}`);
    expect(link).toHaveAttribute('rel', 'noopener noreferrer');
    expect(screen.getByText(sourceCommit, { exact: true })).toBeVisible();
    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/.well-known/source');
    expect(init).toMatchObject({ method: 'GET', credentials: 'omit', redirect: 'error', cache: 'no-store' });
    expect(new Headers(init.headers).get('Authorization')).toBeNull();
    expect(new Headers(init.headers).get('New-Api-User')).toBeNull();
    expect(init.body).toBeUndefined();
  });

  it.each([
    { name: 'a release tag instead of an immutable SHA', changes: { source_url: `${sourceRepository}/tree/production-old` } },
    { name: 'a short SHA', changes: { source_commit: '0123456', source_url: `${sourceRepository}/tree/0123456` } },
    { name: 'a non-hex SHA', changes: { source_commit: 'g'.repeat(40), source_url: `${sourceRepository}/tree/${'g'.repeat(40)}` } },
    { name: 'a mismatched URL commit', changes: { source_url: `${sourceRepository}/tree/${'a'.repeat(40)}` } },
    { name: 'another repository', changes: { source_url: `https://github.com/other/project/tree/${sourceCommit}` } },
    { name: 'a lookalike host', changes: { source_url: `https://github.com.example.com/ffff582/ztapi-source/tree/${sourceCommit}` } },
    { name: 'plain HTTP', changes: { source_url: `http://github.com/ffff582/ztapi-source/tree/${sourceCommit}` } },
    { name: 'embedded credentials', changes: { source_url: `https://user:secret@github.com/ffff582/ztapi-source/tree/${sourceCommit}` } },
    { name: 'a query string', changes: { source_url: `${sourceRepository}/tree/${sourceCommit}?redirect=old` } },
    { name: 'a fragment', changes: { source_url: `${sourceRepository}/tree/${sourceCommit}#old` } },
    { name: 'a different license', changes: { license: 'MIT' } },
    { name: 'a missing license', changes: { license: undefined } },
    { name: 'a contradictory repository field', changes: { source_repository: 'https://github.com/other/project' } },
  ])('rejects $name without offering an old or unverified source link', async ({ changes }) => {
    renderLicense(vi.fn().mockResolvedValue(response({ ...sourceMetadata, data: { ...sourceMetadata.data, ...changes } })));
    expect(await screen.findByRole('alert')).toHaveTextContent('当前版本对应源码暂不可用');
    expect(screen.queryByRole('link', { name: '获取当前版本对应源码' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: '重新加载对应源码' })).toBeVisible();
    expect([...document.querySelectorAll('a')].some((link) => link.href.includes('/ffff582/ztapi-source'))).toBe(false);
  });

  it.each([
    { name: 'an unsuccessful envelope', body: { ...sourceMetadata, success: false }, status: 200 },
    { name: 'a missing data envelope', body: { success: true }, status: 200 },
    { name: 'an HTTP failure with an otherwise valid body', body: sourceMetadata, status: 503 },
    { name: 'malformed JSON', body: null, status: 200 },
  ])('fails explicitly for $name', async ({ body, status }) => {
    const result = body === null ? new Response('{invalid', { status }) : response(body, status);
    renderLicense(vi.fn().mockResolvedValue(result));
    expect(await screen.findByRole('alert')).toHaveTextContent('当前版本对应源码暂不可用');
    expect(screen.queryByRole('link', { name: '获取当前版本对应源码' })).not.toBeInTheDocument();
  });

  it('keeps attribution available while loading and recovers from a network failure through retry', async () => {
    let rejectRequest!: (error: Error) => void;
    const fetchMock = vi.fn()
      .mockImplementationOnce(() => new Promise<Response>((_resolve, reject) => { rejectRequest = reject; }))
      .mockResolvedValueOnce(response());
    renderLicense(fetchMock);
    expect(screen.getByRole('status')).toHaveTextContent('正在加载对应源码');
    expect(screen.getByText('Frontend design and development by New API contributors.', { exact: true })).toBeVisible();
    expect(screen.queryByRole('link', { name: '获取当前版本对应源码' })).not.toBeInTheDocument();
    await act(async () => { rejectRequest(new Error('offline')); });
    expect(await screen.findByRole('alert')).toBeVisible();
    fireEvent.click(screen.getByRole('button', { name: '重新加载对应源码' }));
    expect(await screen.findByRole('link', { name: '获取当前版本对应源码' })).toHaveAttribute('href', sourceMetadata.data.source_url);
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it('bounds a hanging request and ignores a late response after timing out', async () => {
    vi.useFakeTimers();
    let resolveRequest!: (value: Response) => void;
    const fetchMock = vi.fn(() => new Promise<Response>((resolve) => { resolveRequest = resolve; }));
    renderLicense(fetchMock);
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000); });
    expect(screen.getByRole('alert')).toHaveTextContent('当前版本对应源码暂不可用');
    const [, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(init.signal?.aborted).toBe(true);
    await act(async () => { resolveRequest(response()); });
    expect(screen.queryByRole('link', { name: '获取当前版本对应源码' })).not.toBeInTheDocument();
  });

  it('aborts the read when the page unmounts', async () => {
    const fetchMock = vi.fn(() => new Promise<Response>(() => {}));
    const view = renderLicense(fetchMock);
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    const [, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    view.unmount();
    expect(init.signal?.aborted).toBe(true);
  });
});
