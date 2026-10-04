import { getAuthSession, refreshSession } from './client';

export type PlaygroundErrorKind =
  | 'unauthorized'
  | 'insufficient_balance'
  | 'model_unavailable'
  | 'rate_limited'
  | 'invalid_response'
  | 'unknown';

export class PlaygroundClientError extends Error {
  readonly kind: PlaygroundErrorKind;

  constructor(kind: PlaygroundErrorKind) {
    super('Playground request failed.');
    this.name = 'PlaygroundClientError';
    this.kind = kind;
  }
}

export interface PlaygroundChatInput {
  model: string;
  prompt: string;
}

export interface PlaygroundUsage {
  prompt_tokens: number;
  completion_tokens: number;
  total_tokens: number;
}

export interface PlaygroundChatResult {
  text: string;
  response_id: string;
  request_id: string;
  finish_reason: string;
  usage?: PlaygroundUsage;
}

export interface PlaygroundImageInput {
  model: string;
  prompt: string;
  size?: string;
  quality?: string;
  n?: number;
  response_format?: string;
}

export interface PlaygroundImageEditOptions extends PlaygroundImageInput {
  input_field?: string;
}

export interface PlaygroundImageResult {
  created: number;
  request_id: string;
  images: Array<{
    url?: string;
    b64_json?: string;
    revised_prompt?: string;
  }>;
}

export interface PlaygroundVideoInput {
  model: string;
  prompt: string;
  size?: string;
  duration?: number;
  input_reference?: string;
}

export type PlaygroundVideoStatus = 'queued' | 'processing' | 'succeeded' | 'failed' | 'unknown';

export interface PlaygroundVideoTask {
  task_id: string;
  status: PlaygroundVideoStatus;
  request_id: string;
  url?: string;
  progress?: string;
  error?: string;
}

function record(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function nonNegativeInteger(value: unknown): value is number {
  return typeof value === 'number' && Number.isInteger(value) && value >= 0;
}

function responseErrorCode(body: unknown) {
  if (!record(body) || !record(body.error)) {
    return '';
  }
  return typeof body.error.code === 'string' ? body.error.code : '';
}

function classifyFailure(status: number, body: unknown): PlaygroundErrorKind {
  const code = responseErrorCode(body).toLowerCase();
  if (status === 401) {
    return 'unauthorized';
  }
  if (
    status === 403 &&
    (code.includes('insufficient') || code.includes('quota'))
  ) {
    return 'insufficient_balance';
  }
  if (
    status === 404 ||
    status === 503 ||
    code.includes('model_not_found') ||
    code.includes('model_unavailable')
  ) {
    return 'model_unavailable';
  }
  if (status === 429 || code.includes('rate_limit')) {
    return 'rate_limited';
  }
  return 'unknown';
}

function parseUsage(value: unknown): PlaygroundUsage | undefined {
  if (value === undefined) {
    return undefined;
  }
  if (
    !record(value) ||
    !nonNegativeInteger(value.prompt_tokens) ||
    !nonNegativeInteger(value.completion_tokens) ||
    !nonNegativeInteger(value.total_tokens)
  ) {
    throw new PlaygroundClientError('invalid_response');
  }
  return {
    prompt_tokens: value.prompt_tokens,
    completion_tokens: value.completion_tokens,
    total_tokens: value.total_tokens,
  };
}

function parseChatResult(body: unknown, requestID: string): PlaygroundChatResult {
  if (!record(body) || !Array.isArray(body.choices) || body.choices.length === 0) {
    throw new PlaygroundClientError('invalid_response');
  }
  const first = body.choices[0];
  if (
    !record(first) ||
    !record(first.message) ||
    typeof first.message.content !== 'string' ||
    first.message.content.trim() === '' ||
    (first.finish_reason !== undefined &&
      first.finish_reason !== null &&
      typeof first.finish_reason !== 'string')
  ) {
    throw new PlaygroundClientError('invalid_response');
  }
  return {
    text: first.message.content,
    response_id: typeof body.id === 'string' ? body.id : '',
    request_id: requestID,
    finish_reason:
      typeof first.finish_reason === 'string' ? first.finish_reason : '',
    ...(body.usage === undefined ? {} : { usage: parseUsage(body.usage) }),
  };
}

function firstString(...values: unknown[]) {
  return values.find((value): value is string => typeof value === 'string' && value.trim() !== '')?.trim() ?? '';
}

function parseImageResult(body: unknown, requestID: string): PlaygroundImageResult {
  if (!record(body) || !Array.isArray(body.data) || body.data.length === 0) {
    throw new PlaygroundClientError('invalid_response');
  }
  const images = body.data.map((value) => {
    if (!record(value)) {
      throw new PlaygroundClientError('invalid_response');
    }
    const url = typeof value.url === 'string' && value.url.trim() !== '' ? value.url : undefined;
    const b64JSON = typeof value.b64_json === 'string' && value.b64_json.trim() !== '' ? value.b64_json : undefined;
    if (url === undefined && b64JSON === undefined) {
      throw new PlaygroundClientError('invalid_response');
    }
    return {
      ...(url === undefined ? {} : { url }),
      ...(b64JSON === undefined ? {} : { b64_json: b64JSON }),
      ...(typeof value.revised_prompt === 'string' ? { revised_prompt: value.revised_prompt } : {}),
    };
  });
  return {
    created: typeof body.created === 'number' && Number.isFinite(body.created) ? body.created : 0,
    request_id: requestID,
    images,
  };
}

function normalizeVideoStatus(value: string): PlaygroundVideoStatus {
  switch (value.trim().toLowerCase()) {
    case 'queued':
    case 'submitted':
    case 'pending':
      return 'queued';
    case 'processing':
    case 'in_progress':
    case 'running':
      return 'processing';
    case 'succeeded':
    case 'completed':
    case 'success':
      return 'succeeded';
    case 'failed':
    case 'error':
    case 'cancelled':
    case 'canceled':
      return 'failed';
    default:
      return 'unknown';
  }
}

function parseVideoTask(body: unknown, requestID: string): PlaygroundVideoTask {
  if (!record(body)) {
    throw new PlaygroundClientError('invalid_response');
  }
  const source = record(body.data) ? body.data : body;
  const taskID = firstString(source.task_id, source.id, body.task_id, body.id);
  if (taskID === '') {
    throw new PlaygroundClientError('invalid_response');
  }
  const statusValue = firstString(source.status, body.status);
  const errorValue = record(source.error) ? source.error.message : source.fail_reason;
  return {
    task_id: taskID,
    status: normalizeVideoStatus(statusValue),
    request_id: requestID,
    ...(firstString(source.url, source.result_url, body.url, body.result_url) === ''
      ? {}
      : { url: firstString(source.url, source.result_url, body.url, body.result_url) }),
    ...(typeof source.progress === 'string' ? { progress: source.progress } : {}),
    ...(typeof errorValue === 'string' && errorValue.trim() !== '' ? { error: errorValue } : {}),
  };
}

async function mediaJSONRequest<T>(
  path: string,
  init: RequestInit,
  parse: (body: unknown, requestID: string) => T,
  retryUnauthorized: boolean,
): Promise<T> {
  const session = getAuthSession();
  if (session === null) {
    throw new PlaygroundClientError('unauthorized');
  }

  const headers = new Headers(init.headers);
  headers.set('Accept', 'application/json');
  headers.set('Authorization', `Bearer ${session.access_token}`);
  headers.set('New-Api-User', String(session.user.id));

  let response: Response;
  try {
    response = await fetch(path, { ...init, headers, credentials: 'include' });
  } catch {
    throw new PlaygroundClientError('unknown');
  }

  if (response.status === 401 && retryUnauthorized) {
    try {
      await refreshSession();
    } catch {
      throw new PlaygroundClientError('unauthorized');
    }
    return mediaJSONRequest(path, init, parse, false);
  }

  let body: unknown;
  try {
    body = await response.json();
  } catch {
    throw new PlaygroundClientError('invalid_response');
  }
  if (!response.ok) {
    throw new PlaygroundClientError(classifyFailure(response.status, body));
  }
  const requestID = response.headers.get('X-Request-ID') ?? (record(body) && typeof body.request_id === 'string' ? body.request_id : '');
  return parse(body, requestID);
}

async function chatRequest(
  input: PlaygroundChatInput,
  retryUnauthorized: boolean,
): Promise<PlaygroundChatResult> {
  const session = getAuthSession();
  if (session === null) {
    throw new PlaygroundClientError('unauthorized');
  }

  let response: Response;
  try {
    response = await fetch('/pg/chat/completions', {
      method: 'POST',
      credentials: 'include',
      headers: {
        Accept: 'application/json',
        Authorization: `Bearer ${session.access_token}`,
        'Content-Type': 'application/json',
        'New-Api-User': String(session.user.id),
      },
      body: JSON.stringify({
        model: input.model,
        messages: [
          { role: 'system', content: '你是一个简洁、可靠的助手。' },
          { role: 'user', content: input.prompt },
        ],
        stream: false,
        max_tokens: 256,
      }),
    });
  } catch {
    throw new PlaygroundClientError('unknown');
  }

  if (response.status === 401 && retryUnauthorized) {
    try {
      await refreshSession();
    } catch {
      throw new PlaygroundClientError('unauthorized');
    }
    return chatRequest(input, false);
  }

  let body: unknown;
  try {
    body = await response.json();
  } catch {
    throw new PlaygroundClientError('invalid_response');
  }
  if (!response.ok) {
    throw new PlaygroundClientError(classifyFailure(response.status, body));
  }
  return parseChatResult(body, response.headers.get('X-Request-ID') ?? '');
}

export const playgroundClient = {
  chat(input: PlaygroundChatInput) {
    return chatRequest(input, true);
  },
  imageGeneration(input: PlaygroundImageInput) {
    return mediaJSONRequest(
      '/pg/images/generations',
      {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(input),
      },
      parseImageResult,
      true,
    );
  },
  imageEdit(files: File[], options: PlaygroundImageEditOptions) {
    const body = new FormData();
    const inputField = options.input_field ?? 'image';
    files.forEach((file) => body.append(inputField, file, file.name));
    body.append('model', options.model);
    body.append('prompt', options.prompt);
    if (options.size !== undefined) body.append('size', options.size);
    if (options.quality !== undefined) body.append('quality', options.quality);
    if (options.n !== undefined) body.append('n', String(options.n));
    if (options.response_format !== undefined) body.append('response_format', options.response_format);
    return mediaJSONRequest(
      '/pg/images/edits',
      { method: 'POST', body },
      parseImageResult,
      true,
    );
  },
  createVideo(input: PlaygroundVideoInput) {
    return mediaJSONRequest(
      '/pg/video/generations',
      {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(input),
      },
      parseVideoTask,
      true,
    );
  },
  fetchVideo(taskID: string) {
    return mediaJSONRequest(
      `/pg/video/generations/${encodeURIComponent(taskID)}`,
      { method: 'GET' },
      parseVideoTask,
      true,
    );
  },
};
