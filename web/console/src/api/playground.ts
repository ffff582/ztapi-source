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
};
