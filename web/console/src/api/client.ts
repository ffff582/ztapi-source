import type {
  ApiResponse,
  AuthCredentials,
  AuthSessionData,
  AuthUser,
} from './contracts';

const API_BASE_PATH = '/api';

export type AuthErrorKind =
  | 'invalid_credentials'
  | 'username_unavailable'
  | 'invalid_registration'
  | 'unauthorized'
  | 'unknown';

export class AuthClientError extends Error {
  readonly kind: AuthErrorKind;

  constructor(kind: AuthErrorKind) {
    super('Authentication request failed.');
    this.name = 'AuthClientError';
    this.kind = kind;
  }
}

type AuthListener = (user: AuthUser | null) => void;

let memorySession: AuthSessionData | null = null;
let sessionGeneration = 0;
let refreshAttempt: {
  generation: number;
  controller: AbortController;
  promise: Promise<AuthSessionData>;
} | null = null;
let logoutPromise: Promise<void> | null = null;
let authMutationTail: Promise<void> = Promise.resolve();
const authListeners = new Set<AuthListener>();

function queueAuthMutation<T>(operation: () => Promise<T>): Promise<T> {
  const result = authMutationTail.then(operation, operation);
  authMutationTail = result.then(
    () => undefined,
    () => undefined,
  );
  return result;
}

function notifyAuthListeners() {
  const user = memorySession?.user ?? null;
  authListeners.forEach((listener) => listener(user));
}

export function getAuthSession() {
  return memorySession;
}

export function setAuthSession(session: AuthSessionData) {
  sessionGeneration += 1;
  refreshAttempt?.controller.abort();
  refreshAttempt = null;
  memorySession = session;
  notifyAuthListeners();
}

export function clearAuthSession() {
  sessionGeneration += 1;
  refreshAttempt?.controller.abort();
  refreshAttempt = null;
  memorySession = null;
  notifyAuthListeners();
}

export function subscribeAuthSession(listener: AuthListener) {
  authListeners.add(listener);

  return () => {
    authListeners.delete(listener);
  };
}

function classifyServerError(message: unknown, status: number): AuthErrorKind {
  const normalized = typeof message === 'string' ? message.trim().toLowerCase() : '';

  if (
    normalized.includes('invalid credentials') ||
    normalized.includes('invalid username or password')
  ) {
    return 'invalid_credentials';
  }

  if (
    normalized.includes('username unavailable') ||
    normalized.includes('username already exists') ||
    normalized.includes('username is taken')
  ) {
    return 'username_unavailable';
  }

  if (
    normalized.includes('invalid registration') ||
    normalized.includes('invalid registration input')
  ) {
    return 'invalid_registration';
  }

  if (
    status === 401 ||
    normalized.includes('invalid session') ||
    normalized === 'unauthorized'
  ) {
    return 'unauthorized';
  }

  return 'unknown';
}

function isApiResponse<T>(value: unknown): value is ApiResponse<T> {
  if (typeof value !== 'object' || value === null || !('success' in value)) {
    return false;
  }

  if (value.success === true) {
    return true;
  }

  return value.success === false && 'message' in value;
}

async function readResponse<T>(
  response: Response,
  returnFullResponse = false,
): Promise<T> {
  let body: unknown;

  try {
    body = await response.json();
  } catch {
    throw new AuthClientError('unknown');
  }

  if (!isApiResponse<T>(body)) {
    throw new AuthClientError('unknown');
  }

  if (!response.ok || body.success === false) {
    const message = body.success === false ? body.message : undefined;
    throw new AuthClientError(classifyServerError(message, response.status));
  }

  return (returnFullResponse ? body : body.data) as T;
}

function isAuthSessionData(value: unknown): value is AuthSessionData {
  if (typeof value !== 'object' || value === null) {
    return false;
  }

  const candidate = value as Partial<AuthSessionData>;
  const user = candidate.user as Partial<AuthUser> | undefined;

  return (
    typeof candidate.access_token === 'string' &&
    candidate.access_token.length > 0 &&
    typeof candidate.expires_in === 'number' &&
    Number.isFinite(candidate.expires_in) &&
    candidate.expires_in > 0 &&
    typeof user === 'object' &&
    user !== null &&
    typeof user.id === 'number' &&
    Number.isInteger(user.id) &&
    user.id > 0 &&
    typeof user.username === 'string' &&
    user.username.length > 0 &&
    typeof user.role === 'number' &&
    Number.isInteger(user.role) &&
    typeof user.group === 'string'
  );
}

async function performAuthSessionRequest(
  path: string,
  init: RequestInit,
): Promise<AuthSessionData> {
  const session = await performRequest<unknown>(path, init, false, false);

  if (!isAuthSessionData(session)) {
    throw new AuthClientError('unknown');
  }

  return session;
}

async function performRequest<T>(
  path: string,
  init: RequestInit,
  retryUnauthorized: boolean,
  attachAccessToken: boolean,
  returnFullResponse = false,
): Promise<T> {
  const tokenAtRequest = attachAccessToken
    ? memorySession?.access_token
    : undefined;
  const headers = new Headers(init.headers);
  headers.set('Accept', 'application/json');

  if (tokenAtRequest !== undefined) {
    headers.set('Authorization', `Bearer ${tokenAtRequest}`);
    headers.set('New-Api-User', String(memorySession?.user.id));
  }

  let response: Response;

  try {
    response = await fetch(`${API_BASE_PATH}${path}`, {
      ...init,
      headers,
      credentials: 'include',
    });
  } catch {
    throw new AuthClientError('unknown');
  }

  if (
    response.status === 401 &&
    retryUnauthorized &&
    tokenAtRequest !== undefined
  ) {
    const currentToken = memorySession?.access_token;

    if (currentToken === undefined) {
      throw new AuthClientError('unauthorized');
    }

    try {
      if (currentToken === tokenAtRequest) {
        await refreshSession();
      }
    } catch {
      if (memorySession?.access_token === tokenAtRequest) {
        clearAuthSession();
      }

      throw new AuthClientError('unauthorized');
    }

    return performRequest<T>(path, init, false, true, returnFullResponse);
  }

  try {
    return await readResponse<T>(response, returnFullResponse);
  } catch (error) {
    if (
      error instanceof AuthClientError &&
      error.kind === 'unauthorized' &&
      tokenAtRequest !== undefined &&
      memorySession?.access_token === tokenAtRequest
    ) {
      clearAuthSession();
    }

    throw error;
  }
}

export function authRequest<T>(
  path: string,
  init: RequestInit = {},
): Promise<T> {
  return performRequest<T>(`/auth${path}`, init, true, true);
}

export const apiClient = {
  get<T>(path: string, init: RequestInit = {}) {
    return performRequest<T>(
      path,
      { ...init, method: 'GET' },
      true,
      true,
    );
  },
  getResponse<T>(path: string, init: RequestInit = {}) {
    return performRequest<T>(
      path,
      { ...init, method: 'GET' },
      true,
      true,
      true,
    );
  },
  post<T>(path: string, body?: unknown, init: RequestInit = {}) {
    const headers = new Headers(init.headers);
    let requestBody = init.body;

    if (body !== undefined) {
      headers.set('Content-Type', 'application/json');
      requestBody = JSON.stringify(body);
    }

    return performRequest<T>(
      path,
      {
        ...init,
        method: 'POST',
        headers,
        body: requestBody,
      },
      true,
      true,
    );
  },
  put<TResponse, TBody>(
    path: string,
    body: TBody,
    init: RequestInit = {},
  ) {
    const headers = new Headers(init.headers);
    headers.set('Content-Type', 'application/json');

    return performRequest<TResponse>(
      path,
      {
        ...init,
        method: 'PUT',
        headers,
        body: JSON.stringify(body),
      },
      true,
      true,
    );
  },
  delete<T>(path: string, init: RequestInit = {}) {
    return performRequest<T>(
      path,
      { ...init, method: 'DELETE' },
      true,
      true,
    );
  },
};

export function refreshSession(): Promise<AuthSessionData> {
  const generation = sessionGeneration;

  if (
    refreshAttempt !== null &&
    refreshAttempt.generation === generation
  ) {
    return refreshAttempt.promise;
  }

  const controller = new AbortController();
  const promise = queueAuthMutation(() =>
    performAuthSessionRequest(
      '/auth/refresh',
      { method: 'POST', signal: controller.signal },
    ),
  )
    .then((session) => {
      if (generation === sessionGeneration) {
        sessionGeneration += 1;
        memorySession = session;
        notifyAuthListeners();
      }

      return session;
    })
    .catch((error: unknown) => {
      if (generation === sessionGeneration) {
        sessionGeneration += 1;
        memorySession = null;
        notifyAuthListeners();
      }

      throw error;
    })
    .finally(() => {
      if (refreshAttempt?.promise === promise) {
        refreshAttempt = null;
      }
    });

  refreshAttempt = { generation, controller, promise };
  return promise;
}

async function submitCredentials(
  path: '/auth/login' | '/auth/register',
  credentials: AuthCredentials,
) {
  return queueAuthMutation(async () => {
    clearAuthSession();
    const generation = sessionGeneration;

    const session = await performAuthSessionRequest(
      path,
      {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(credentials),
      },
    );

    if (generation !== sessionGeneration) {
      throw new AuthClientError('unauthorized');
    }

    setAuthSession(session);
    return session;
  });
}

export function login(credentials: AuthCredentials) {
  return submitCredentials('/auth/login', credentials);
}

export function register(credentials: AuthCredentials) {
  return submitCredentials('/auth/register', credentials);
}

export function logoutSession(): Promise<void> {
  if (logoutPromise !== null) {
    return logoutPromise;
  }

  clearAuthSession();

  const request = queueAuthMutation(() =>
    performRequest<unknown>(
      '/auth/logout',
      { method: 'POST' },
      false,
      true,
    ),
  )
    .then(() => undefined)
    .catch(() => {
      // Local logout is authoritative even when the server is unavailable.
    })
    .finally(() => {
      if (logoutPromise === request) {
        logoutPromise = null;
      }
    });
  logoutPromise = request;
  return request;
}

export function authErrorMessage(error: unknown) {
  if (error instanceof AuthClientError) {
    switch (error.kind) {
      case 'invalid_credentials':
        return '账号或密码错误';
      case 'username_unavailable':
        return '账号不可用，请更换后重试';
      case 'invalid_registration':
        return '账号格式或密码不符合要求';
      case 'unauthorized':
      case 'unknown':
        break;
    }
  }

  return '服务暂不可用，请稍后重试';
}
