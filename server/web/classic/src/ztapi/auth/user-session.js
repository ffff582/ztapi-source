let memorySession = null;
let refreshPromise = null;
let logoutPromise = null;

export class UserSessionError extends Error {
  constructor(status = 0, kind = 'unknown') {
    super('User authentication request failed.');
    this.name = 'UserSessionError';
    this.status = status;
    this.kind = kind;
  }
}

function isSession(value) {
  return (
    value &&
    typeof value === 'object' &&
    typeof value.access_token === 'string' &&
    value.access_token.length > 0 &&
    Number.isFinite(value.expires_in) &&
    value.expires_in > 0 &&
    value.user &&
    Number.isInteger(value.user.id) &&
    value.user.id > 0 &&
    typeof value.user.username === 'string' &&
    value.user.username.length > 0 &&
    Number.isInteger(value.user.role)
  );
}

async function readJSON(response) {
  try {
    return await response.json();
  } catch {
    return undefined;
  }
}

function normalizeBody(body, headers) {
  if (body === undefined || typeof body === 'string') return body;
  headers['Content-Type'] = 'application/json';
  return JSON.stringify(body);
}

async function fetchJSON(config, attachSession) {
  const { url, body, headers: inputHeaders, ...requestInit } = config;
  const sessionAtRequest = attachSession ? memorySession : null;
  const headers = { Accept: 'application/json', ...(inputHeaders || {}) };
  if (sessionAtRequest) {
    headers.Authorization = `Bearer ${sessionAtRequest.access_token}`;
    headers['New-API-User'] = String(sessionAtRequest.user.id);
  }

  let response;
  try {
    response = await fetch(url, {
      ...requestInit,
      headers,
      body: normalizeBody(body, headers),
      credentials: 'include',
    });
  } catch {
    throw new UserSessionError(0, 'network');
  }
  const responseBody = await readJSON(response);
  return { response, body: responseBody, sessionAtRequest };
}

function requireSession(body) {
  const session = body?.success === true ? body.data : body;
  if (!isSession(session)) {
    throw new UserSessionError(401, 'invalid_session');
  }
  return session;
}

async function requestSession(url, credentials) {
  const { response, body } = await fetchJSON(
    {
      method: 'POST',
      url,
      body: credentials,
    },
    false,
  );
  if (!response.ok || body?.success === false) {
    throw new UserSessionError(response.status, 'unauthorized');
  }
  return requireSession(body);
}

export function getUserSession() {
  return memorySession;
}

export function setUserSession(session) {
  if (!isSession(session)) {
    throw new UserSessionError(401, 'invalid_session');
  }
  memorySession = session;
  return memorySession;
}

export function clearUserSession() {
  memorySession = null;
  refreshPromise = null;
}

export async function userLogin(credentials) {
  clearUserSession();
  const session = await requestSession('/api/auth/login', credentials);
  return setUserSession(session);
}

export function userRefresh() {
  if (refreshPromise) return refreshPromise;
  refreshPromise = requestSession('/api/auth/refresh', undefined)
    .then(setUserSession)
    .catch((error) => {
      clearUserSession();
      throw error;
    })
    .finally(() => {
      refreshPromise = null;
    });
  return refreshPromise;
}

async function performUserRequest(config, retryUnauthorized) {
  if (!memorySession) {
    if (!retryUnauthorized) {
      throw new UserSessionError(401, 'unauthorized');
    }
    await userRefresh();
  }

  const { response, body, sessionAtRequest } = await fetchJSON(config, true);
  if (response.status === 401 && retryUnauthorized) {
    if (memorySession?.access_token === sessionAtRequest?.access_token) {
      await userRefresh();
    }
    return performUserRequest(config, false);
  }
  if (!response.ok || body?.success === false) {
    if (response.status === 401) clearUserSession();
    throw new UserSessionError(
      response.status,
      response.status === 401 ? 'unauthorized' : 'request_failed',
    );
  }
  return body;
}

export function userRequest(config) {
  return performUserRequest({ method: 'GET', ...config }, true);
}

export function userLogout() {
  if (logoutPromise) return logoutPromise;
  clearUserSession();
  logoutPromise = fetchJSON(
    { method: 'POST', url: '/api/auth/logout' },
    false,
  )
    .then(() => undefined)
    .catch(() => undefined)
    .finally(() => {
      logoutPromise = null;
    });
  return logoutPromise;
}
