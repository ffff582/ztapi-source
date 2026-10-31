import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { clearAuthSession, setAuthSession } from './client';
import { fetchWorkbenchVideo, readVideoReference, saveVideoReference } from './workbench-video';

describe('workbench video GET and storage contracts', () => {
  beforeEach(() => setAuthSession({ access_token: 'fixture', expires_in: 900, user: { id: 9, username: 'fixture', role: 1, group: 'default' } }));
  afterEach(() => { clearAuthSession(); localStorage.clear(); vi.unstubAllGlobals(); vi.restoreAllMocks(); });

  it.each([
    ['NOT_START', 'queued'], ['SUBMITTED', 'queued'], ['QUEUED', 'queued'], ['IN_PROGRESS', 'processing'],
    ['SUCCESS', 'succeeded'], ['FAILURE', 'failed'], ['future-state', 'unknown'],
  ])('normalizes the backend TaskDto status %s without guessing', async (raw, status) => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ code: 'success', data: { task_id: 'task /1', user_id: 9, status: raw } })));
    vi.stubGlobal('fetch', fetchMock);
    expect(await fetchWorkbenchVideo('task /1')).toEqual({ task_id: 'task /1', status, request_id: '' });
    expect(fetchMock).toHaveBeenCalledExactlyOnceWith('/pg/video/generations/task%20%2F1', expect.objectContaining({ method: 'GET' }));
  });

  it.each([
    { task_id: 'wrong-task', user_id: 9 },
    { task_id: 'expected', user_id: 10 },
  ])('rejects a mismatched task identity or owner', async (data) => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ code: 'success', data }))));
    await expect(fetchWorkbenchVideo('expected')).rejects.toMatchObject({ kind: 'invalid_response' });
  });

  it('drops raw failure details, progress text and unsafe URLs', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ code: 'success', data: {
      task_id: 'task-1', status: 'FAILURE', result_url: 'javascript:alert(1)',
      fail_reason: 'sensitive', progress: 'sensitive', data: { prompt: 'sensitive' },
    } }))));
    expect(await fetchWorkbenchVideo('task-1')).toEqual({ task_id: 'task-1', status: 'failed', request_id: '' });
  });

  it('persists only the minimal reference, not arbitrary response fields', () => {
    const response = { task_id: 'task-1', model: 'zt-video', url: 'sensitive-url', prompt: 'sensitive', status: 'queued' };
    expect(saveVideoReference(9, response)).toBe(true);
    expect(localStorage.getItem('ztapi:workbench:video-task:v1:9')).toBe('{"task_id":"task-1","model":"zt-video"}');
    expect(readVideoReference(10)).toEqual({ reference: null, writable: true });
  });

  it.each(['{invalid', '{"task_id":"a","model":"b","prompt":"private"}'])('preserves corrupt or oversized legacy references instead of overwriting them', (raw) => {
    localStorage.setItem('ztapi:workbench:video-task:v1:9', raw);
    expect(readVideoReference(9)).toEqual({ reference: null, writable: false });
    expect(localStorage.getItem('ztapi:workbench:video-task:v1:9')).toBe(raw);
  });
});
