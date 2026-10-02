/*
Copyright (C) 2025 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/

import { isKnownAdminRole } from './admin-permission-policy';

const authPaths = new Set([
  '/api/auth/login',
  '/api/auth/refresh',
  '/api/auth/logout',
]);

let memorySession = null;
let sessionGeneration = 0;
let refreshRecord = null;
let logoutPromise = null;
let mutationTail = Promise.resolve();
const listeners = new Set();

export class AdminSessionError extends Error {
  constructor(status = 0, kind = 'unknown', data) {
    super('Administrator authentication request failed.');
    this.name = 'AdminSessionError';
    this.status = status;
    this.kind = kind;
    this.data = data;
  }
}

const safeConflictKeys = new Set([
  'id',
  'user_id',
  'username',
  'amount',
  'money',
  'trade_no',
  'payment_method',
  'payment_provider',
  'create_time',
  'complete_time',
  'status',
]);

function safeConflictData(value) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    return undefined;
  }
  const result = {};
  for (const [key, item] of Object.entries(value)) {
    if (
      safeConflictKeys.has(key) &&
      (typeof item === 'string' ||
        typeof item === 'number' ||
        typeof item === 'boolean' ||
        item === null)
    ) {
      result[key] = item;
    }
  }
  return Object.keys(result).length ? result : undefined;
}

function queueMutation(operation) {
  const result = mutationTail.then(operation, operation);
  mutationTail = result.then(
    () => undefined,
    () => undefined,
  );
  return result;
}

function notify() {
  listeners.forEach((listener) => listener(memorySession));
}

function isSession(value) {
  return (
    value &&
    typeof value === 'object' &&
    typeof value.access_token === 'string' &&
    value.access_token.length > 0 &&
    typeof value.expires_in === 'number' &&
    Number.isFinite(value.expires_in) &&
    value.expires_in > 0 &&
    value.user &&
    typeof value.user === 'object' &&
    Number.isInteger(value.user.id) &&
    value.user.id > 0 &&
    Number.isInteger(value.user.role)
  );
}

function isAdminSession(session) {
  return isSession(session) && isKnownAdminRole(session.user.role);
}

function getSessionData(body) {
  if (body && body.success === true) {
    return body.data;
  }
  return body;
}

function asAdminError(error, status = 0) {
  if (error instanceof AdminSessionError) {
    return error;
  }
  return new AdminSessionError(status);
}

async function readBody(response) {
  if (response.status === 204) {
    return undefined;
  }

  try {
    return await response.json();
  } catch {
    return undefined;
  }
}

async function performRequest(config, retryUnauthorized, attachSession) {
  const { url, ...requestInit } = config;
  const sessionAtRequest = attachSession ? memorySession : null;
  const headers = {};
  new Headers(requestInit.headers).forEach((value, key) => {
    headers[key] = value;
  });
  headers.Accept = 'application/json';

  if (sessionAtRequest) {
    headers.Authorization = `Bearer ${sessionAtRequest.access_token}`;
    headers['New-API-User'] = String(sessionAtRequest.user.id);
  }

  let response;
  try {
    response = await fetch(url, {
      ...requestInit,
      headers,
      credentials: 'include',
    });
  } catch (error) {
    throw asAdminError(error);
  }

  const body = await readBody(response);

  if (response.status === 401 && retryUnauthorized && sessionAtRequest) {
    const currentSession = memorySession;
    if (!currentSession) {
      throw new AdminSessionError(401, 'unauthorized');
    }

    try {
      if (currentSession.access_token === sessionAtRequest.access_token) {
        await adminRefresh();
      }
    } catch (error) {
      if (memorySession?.access_token === sessionAtRequest.access_token) {
        clearAdminSession();
      }
      throw asAdminError(error, 401);
    }

    return performRequest(config, false, true);
  }

  if (!response.ok || (body && body.success === false)) {
    if (
      response.status === 401 &&
      sessionAtRequest?.access_token === memorySession?.access_token
    ) {
      clearAdminSession();
    }
    throw new AdminSessionError(
      response.status,
      response.status === 401 ? 'unauthorized' : 'unknown',
      response.status === 409 ? safeConflictData(body?.data) : undefined,
    );
  }

  const data = getSessionData(body);
  if (
    body?.warning &&
    data &&
    typeof data === 'object' &&
    !Array.isArray(data)
  ) {
    return { ...data, warning: body.warning };
  }
  return data;
}

function requestSession(url, credentials) {
  return performRequest(
    {
      method: 'POST',
      url,
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(credentials),
    },
    false,
    false,
  ).then((value) => {
    const session = getSessionData(value);
    if (!isSession(session)) {
      throw new AdminSessionError(200, 'invalid_session');
    }
    return session;
  });
}

export function getAdminSession() {
  return memorySession;
}

export function setAdminSession(session) {
  if (session === null) {
    clearAdminSession();
    return;
  }
  if (!isSession(session)) {
    throw new AdminSessionError(200, 'invalid_session');
  }
  sessionGeneration += 1;
  refreshRecord = null;
  memorySession = session;
  notify();
}

export function clearAdminSession() {
  sessionGeneration += 1;
  refreshRecord = null;
  memorySession = null;
  notify();
}

export function subscribeAdminSession(listener) {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function adminRefresh() {
  const generation = sessionGeneration;
  if (refreshRecord?.generation === generation) {
    return refreshRecord.promise;
  }

  const controller = new AbortController();
  const promise = queueMutation(() =>
    requestSession('/api/auth/refresh', undefined),
  )
    .then((session) => {
      if (!isAdminSession(session)) {
        throw new AdminSessionError(200, 'forbidden');
      }
      return session;
    })
    .then((session) => {
      if (generation === sessionGeneration) {
        sessionGeneration += 1;
        memorySession = session;
        notify();
      }
      return session;
    })
    .catch((error) => {
      if (generation === sessionGeneration) {
        clearAdminSession();
      }
      throw asAdminError(error, 401);
    })
    .finally(() => {
      if (refreshRecord?.promise === promise) {
        refreshRecord = null;
      }
    });

  refreshRecord = { controller, generation, promise };
  return promise;
}

export function adminLogin(credentials) {
  return queueMutation(async () => {
    clearAdminSession();
    const generation = sessionGeneration;
    const session = await requestSession('/api/auth/login', credentials);

    if (!isAdminSession(session)) {
      throw new AdminSessionError(403, 'forbidden');
    }

    if (generation !== sessionGeneration) {
      throw new AdminSessionError(401, 'unauthorized');
    }

    setAdminSession(session);
    return session;
  });
}

export function adminLogout() {
  if (logoutPromise) {
    return logoutPromise;
  }

  clearAdminSession();
  logoutPromise = queueMutation(() =>
    performRequest({ method: 'POST', url: '/api/auth/logout' }, false, false),
  )
    .then(() => undefined)
    .catch(() => undefined)
    .finally(() => {
      logoutPromise = null;
    });

  return logoutPromise;
}

export function adminRequest(config) {
  const requestConfig = { method: 'GET', ...config };
  if (authPaths.has(requestConfig.url)) {
    return performRequest(requestConfig, false, false);
  }
  return performRequest(requestConfig, true, true);
}

async function performDownload(config, retryUnauthorized) {
  const { url, ...requestInit } = config;
  const sessionAtRequest = memorySession;
  if (!sessionAtRequest) {
    throw new AdminSessionError(401, 'unauthorized');
  }
  const headers = {};
  new Headers(requestInit.headers).forEach((value, key) => {
    headers[key] = value;
  });
  headers.Accept = 'text/csv';
  headers.Authorization = `Bearer ${sessionAtRequest.access_token}`;
  headers['New-API-User'] = String(sessionAtRequest.user.id);

  let response;
  try {
    response = await fetch(url, {
      method: 'GET',
      ...requestInit,
      headers,
      credentials: 'include',
    });
  } catch (error) {
    throw asAdminError(error);
  }

  if (response.status === 401 && retryUnauthorized) {
    await adminRefresh();
    return performDownload(config, false);
  }
  if (!response.ok) {
    throw new AdminSessionError(
      response.status,
      response.status === 401 ? 'unauthorized' : 'unknown',
    );
  }
  return response.blob();
}

export async function adminDownload(config, filename) {
  const blob = await performDownload(config, true);
  const objectUrl = URL.createObjectURL(blob);
  const anchor = document.createElement('a');
  anchor.href = objectUrl;
  anchor.download = filename;
  anchor.hidden = true;
  document.body.appendChild(anchor);
  try {
    anchor.click();
  } finally {
    anchor.remove();
    URL.revokeObjectURL(objectUrl);
  }
}
