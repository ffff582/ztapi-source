import { act, cleanup, renderHook } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { clearAuthSession, setAuthSession } from '../../api/client';
import { useVideoTask } from './useVideoTask';

describe('resumable video lifecycle boundaries', () => {
  beforeEach(() => {
    setAuthSession({ access_token: 'fixture', expires_in: 900, user: { id: 9, username: 'fixture', role: 1, group: 'default' } });
    vi.useFakeTimers();
    localStorage.setItem('ztapi:workbench:video-task:v1:9', '{"task_id":"task-1","model":"zt-video"}');
  });
  afterEach(() => { cleanup(); clearAuthSession(); localStorage.clear(); vi.useRealTimers(); vi.unstubAllGlobals(); vi.restoreAllMocks(); });

  it('stops a long-running task after a bounded GET-only polling window', async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ code: 'success', data: { task_id: 'task-1', status: 'IN_PROGRESS' } })));
    vi.stubGlobal('fetch', fetchMock);
    const { result } = renderHook(() => useVideoTask(9, true, true, true));
    await act(async () => { await vi.advanceTimersByTimeAsync(120_000); });
    expect(result.current.task?.status).toBe('processing');
    expect(result.current.query).toBe('paused');
    expect(fetchMock).toHaveBeenCalledTimes(50);
    await act(async () => { await vi.advanceTimersByTimeAsync(120_000); });
    expect(fetchMock).toHaveBeenCalledTimes(50);
    await act(async () => { result.current.retry(); });
    await act(async () => { await vi.advanceTimersByTimeAsync(1); });
    expect(fetchMock).toHaveBeenCalledTimes(51);
  });

  it('does not resurrect a dismissed task from a late GET response', async () => {
    let finish!: (response: Response) => void;
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>((resolve) => { finish = resolve; })));
    const { result } = renderHook(() => useVideoTask(9, true, true, true));
    await act(async () => { await vi.advanceTimersByTimeAsync(1); });
    expect(result.current.query).toBe('loading');
    await act(async () => {
      result.current.dismiss();
      finish(new Response(JSON.stringify({ data: { task_id: 'task-1', status: 'SUCCESS', result_url: 'https://example.test/late.mp4' } })));
      await Promise.resolve();
    });
    expect(result.current.task).toBeNull();
    expect(localStorage.getItem('ztapi:workbench:video-task:v1:9')).toBeNull();
  });

  it('preserves unreadable references when a new paid result arrives', () => {
    localStorage.setItem('ztapi:workbench:video-task:v1:9', '{corrupt');
    const { result } = renderHook(() => useVideoTask(9, true, true, false));
    expect(result.current.warning).toContain('原记录未覆盖');
    act(() => result.current.accept({ task_id: 'new-task', status: 'queued', request_id: '' }, 'zt-video'));
    expect(result.current.task?.task_id).toBe('new-task');
    expect(result.current.warning).toContain('未能保存');
    expect(localStorage.getItem('ztapi:workbench:video-task:v1:9')).toBe('{corrupt');
  });
});
