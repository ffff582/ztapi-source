import { getAuthSession } from './client';
import { PlaygroundClientError, type PlaygroundVideoStatus, type PlaygroundVideoTask } from './playground';
import { workbenchGet } from './workbench-readiness';

export type WorkbenchVideoReference = { task_id: string; model: string };
export const videoReferenceKey = (userID: number) => `ztapi:workbench:video-task:v1:${userID}`;

export function readVideoReference(userID: number): { reference: WorkbenchVideoReference | null; writable: boolean } {
  try {
    const raw = window.localStorage.getItem(videoReferenceKey(userID));
    if (raw === null) return { reference: null, writable: true };
    const value: unknown = JSON.parse(raw);
    if (!record(value) || Object.keys(value).some((key) => key !== 'task_id' && key !== 'model') ||
      !safeReferenceString(value.task_id) || !safeReferenceString(value.model)) return { reference: null, writable: false };
    return { reference: { task_id: value.task_id, model: value.model }, writable: true };
  } catch { return { reference: null, writable: false }; }
}

export function saveVideoReference(userID: number, reference: WorkbenchVideoReference | null) {
  try {
    if (reference === null) window.localStorage.removeItem(videoReferenceKey(userID));
    else window.localStorage.setItem(videoReferenceKey(userID), JSON.stringify({ task_id: reference.task_id, model: reference.model }));
    return true;
  } catch { return false; }
}

function safeReferenceString(value: unknown): value is string {
  return typeof value === 'string' && value.trim() !== '' && value.length <= 512 && !/\p{Cc}/u.test(value);
}

function record(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function videoStatus(value: unknown): PlaygroundVideoStatus {
  switch (typeof value === 'string' ? value.trim().toLowerCase() : '') {
    case 'not_start': case 'queued': case 'submitted': case 'pending': return 'queued';
    case 'processing': case 'in_progress': case 'running': return 'processing';
    case 'success': case 'succeeded': case 'completed': return 'succeeded';
    case 'failure': case 'failed': case 'error': case 'cancelled': case 'canceled': return 'failed';
    default: return 'unknown';
  }
}

export function safeVideoTask(task: PlaygroundVideoTask): PlaygroundVideoTask {
  if (!safeReferenceString(task.task_id)) throw new PlaygroundClientError('invalid_response');
  const url = typeof task.url === 'string' && (/^https?:\/\//i.test(task.url) || /^\/(?!\/)/.test(task.url)) ? task.url : undefined;
  return {
    task_id: task.task_id, status: task.status, request_id: task.request_id,
    ...(url === undefined ? {} : { url }),
    ...(typeof task.progress === 'string' && /^(?:100|\d{1,2})%$/.test(task.progress) ? { progress: task.progress } : {}),
  };
}

export async function fetchWorkbenchVideo(taskID: string): Promise<PlaygroundVideoTask> {
  const ownerID = getAuthSession()?.user.id;
  const body = await workbenchGet(`/pg/video/generations/${encodeURIComponent(taskID)}`);
  if (!record(body) || ('code' in body && body.code !== 'success') || body.success === false) throw new PlaygroundClientError('invalid_response');
  const source = record(body.data) ? body.data : body;
  const id = source.task_id ?? source.id;
  if (id !== taskID || (source.user_id !== undefined && source.user_id !== ownerID)) throw new PlaygroundClientError('invalid_response');
  const rawURL = source.result_url ?? source.url;
  return safeVideoTask({
    task_id: taskID, status: videoStatus(source.status), request_id: '',
    ...(typeof rawURL === 'string' ? { url: rawURL } : {}),
    ...(typeof source.progress === 'string' ? { progress: source.progress } : {}),
  });
}
