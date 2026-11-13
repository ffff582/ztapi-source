import { useCallback, useEffect, useRef, useState } from 'react';
import { getAuthSession } from '../../api/client';
import type { PlaygroundVideoTask } from '../../api/playground';
import { fetchWorkbenchVideo, readVideoReference, safeVideoTask, saveVideoReference, type WorkbenchVideoReference } from '../../api/workbench-video';

const POLL_INTERVAL_MS = 1200;
const MAX_POLLS = 50;
type QueryState = 'idle' | 'loading' | 'watching' | 'error' | 'paused';
type VideoState = {
  ownerID?: number;
  reference: WorkbenchVideoReference | null;
  task: PlaygroundVideoTask | null;
  query: QueryState;
  warning: string;
};
const emptyVideo: VideoState = { reference: null, task: null, query: 'idle', warning: '' };

export function useVideoTask(userID: number | undefined, active: boolean, persist: boolean, canQuery: boolean) {
  const [state, setState] = useState<VideoState>(emptyVideo);
  const [revision, setRevision] = useState(0);
  const writable = useRef(true);
  const shouldRun = useRef(false);
  const immediate = useRef(true);
  const owned = userID !== undefined && state.ownerID === userID;
  const reference = owned ? state.reference : null;

  useEffect(() => {
    const restored = persist && userID !== undefined ? readVideoReference(userID) : { reference: null, writable: true };
    writable.current = restored.writable;
    shouldRun.current = restored.reference !== null;
    immediate.current = true;
    setState({
      ...emptyVideo, ownerID: userID, reference: restored.reference,
      task: restored.reference === null ? null : { task_id: restored.reference.task_id, status: 'unknown', request_id: '' },
      warning: restored.writable ? '' : '视频任务引用无法读取，原记录未覆盖。',
    });
  }, [persist, userID]);

  useEffect(() => {
    if (!active || !canQuery || reference === null || !shouldRun.current) return;
    let cancelled = false;
    let timer: number | undefined;
    let polls = 0;
    const current = () => !cancelled && getAuthSession()?.user.id === userID;
    const poll = async () => {
      if (!current()) return;
      setState((previous) => ({ ...previous, query: 'loading' }));
      try {
        const task = await fetchWorkbenchVideo(reference.task_id);
        if (!current()) return;
        const terminal = task.status === 'succeeded' || task.status === 'failed';
        const paused = task.status === 'unknown' || ++polls >= MAX_POLLS;
        shouldRun.current = !terminal && !paused;
        setState((previous) => ({ ...previous, task: { ...task, request_id: previous.task?.request_id ?? '' }, query: terminal ? 'idle' : paused ? 'paused' : 'watching' }));
        if (shouldRun.current) timer = window.setTimeout(() => { void poll(); }, POLL_INTERVAL_MS);
      } catch {
        if (!current()) return;
        shouldRun.current = false;
        setState((previous) => ({ ...previous, query: 'error' }));
      }
    };
    timer = window.setTimeout(() => { void poll(); }, immediate.current ? 0 : POLL_INTERVAL_MS);
    return () => { cancelled = true; window.clearTimeout(timer); };
  }, [active, canQuery, reference, revision, userID]);

  const accept = useCallback((task: PlaygroundVideoTask, model: string) => {
    if (userID === undefined || getAuthSession()?.user.id !== userID) return;
    task = safeVideoTask(task);
    const next = { task_id: task.task_id, model };
    const saved = !persist || (writable.current && saveVideoReference(userID, next));
    shouldRun.current = task.status !== 'succeeded' && task.status !== 'failed';
    immediate.current = task.status === 'unknown';
    setState({ ownerID: userID, reference: next, task, query: shouldRun.current ? 'watching' : 'idle', warning: saved ? '' : '视频任务引用未能保存，刷新后可能无法续查。' });
  }, [persist, userID]);

  const retry = () => {
    if (!owned || reference === null || !canQuery || state.query === 'loading') return;
    shouldRun.current = true;
    immediate.current = true;
    setRevision((value) => value + 1);
  };
  const dismiss = () => {
    if (!owned || userID === undefined || getAuthSession()?.user.id !== userID) return;
    const saved = !persist || saveVideoReference(userID, null);
    writable.current = saved;
    shouldRun.current = false;
    setState({ ...emptyVideo, ownerID: userID, warning: saved ? '' : '本地视频任务引用未能移除，刷新后仍可能恢复。' });
  };

  return {
    task: owned ? state.task : null, reference,
    query: owned ? state.query : 'idle', warning: owned ? state.warning : '',
    pending: owned && state.reference !== null && state.task?.status !== 'succeeded' && state.task?.status !== 'failed',
    accept, retry, dismiss,
  };
}
