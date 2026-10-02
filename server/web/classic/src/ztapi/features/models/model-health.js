/*
Copyright (C) 2025 QuantumNous
SPDX-License-Identifier: AGPL-3.0-or-later
*/

import { adminRequest } from '../../auth/admin-session.js';

const field = (value, snake, go) => value?.[snake] ?? value?.[go];
const list = (value) => (Array.isArray(value) ? value : []);
const text = (value) => (typeof value === 'string' ? value.slice(0, 4096) : '');
const integer = (value) => {
  return Number.isSafeInteger(value) && value >= 0 ? value : null;
};

function outcomeMetadata(event) {
  const raw = field(event, 'outcome', 'Outcome');
  if (raw && typeof raw === 'object' && !Array.isArray(raw)) return raw;
  if (typeof raw !== 'string' || raw.length > 65536) return {};
  try {
    const parsed = JSON.parse(raw);
    return parsed && typeof parsed === 'object' ? parsed : {};
  } catch {
    return {};
  }
}

// This projection intentionally never spreads server records or raw outcomes.
export function normalizeModelHealth(data) {
  if (!data || typeof data !== 'object')
    throw new Error('invalid_health_response');
  const rawState = data.state;
  const state = rawState
    ? {
        modelID: integer(field(rawState, 'model_id', 'ModelID')),
        generation: integer(field(rawState, 'generation', 'Generation')),
        open:
          typeof field(rawState, 'open', 'Open') === 'boolean'
            ? field(rawState, 'open', 'Open')
            : null,
        sequence: integer(
          field(rawState, 'completion_sequence', 'CompletionSequence'),
        ),
        consecutiveFailures: integer(
          field(rawState, 'consecutive_failures', 'ConsecutiveFailures'),
        ),
        incidentID: integer(field(rawState, 'incident_id', 'IncidentID')),
        updatedAt: integer(field(rawState, 'updated_at', 'UpdatedAt')),
      }
    : null;
  const validSamples = integer(
    field(data.window, 'valid_samples', 'ValidSamples'),
  );
  const failures = integer(field(data.window, 'failures', 'Failures'));
  const failureRate =
    validSamples > 0 && failures !== null && failures <= validSamples
      ? (failures * 100) / validSamples
      : null;
  const enabled = data.enabled === true;
  const observed = data.observed === true;
  const status =
    state?.open === true
      ? 'tripped'
      : !enabled
        ? 'disabled'
        : observed && state?.open === false && failureRate !== null
          ? 'available'
          : 'unknown';
  return {
    modelID: integer(data.model_id),
    enabled,
    observed,
    state,
    status,
    window: {
      validSamples,
      failures,
      failureRate,
      start: integer(field(data.window, 'window_start', 'WindowStart')),
      end: integer(field(data.window, 'window_end', 'WindowEnd')),
    },
    coverage: list(data.coverage).map((row) => ({
      stream: field(row, 'stream', 'Stream') === true,
      source: text(field(row, 'source', 'Source')),
      validSamples: integer(field(row, 'valid_samples', 'ValidSamples')),
      unknownSamples: integer(field(row, 'unknown_samples', 'UnknownSamples')),
      excludedSamples: integer(
        field(row, 'excluded_samples', 'ExcludedSamples'),
      ),
      lastValidAt: integer(field(row, 'last_valid_at', 'LastValidAt')),
    })),
    events: list(data.events)
      .slice(0, 100)
      .map((row) => {
        const metadata = outcomeMetadata(row);
        return {
          id: integer(field(row, 'id', 'ID')),
          sequence: integer(
            field(row, 'completion_sequence', 'CompletionSequence'),
          ),
          generation: integer(field(row, 'generation', 'Generation')),
          configVersion: integer(field(row, 'config_version', 'ConfigVersion')),
          result: text(field(row, 'result', 'Result')),
          reason: text(field(row, 'reason', 'Reason')),
          source: text(field(row, 'source', 'Source')),
          stream: field(row, 'stream', 'Stream') === true,
          counted: field(row, 'counted', 'Counted') === true,
          staleGeneration:
            field(row, 'stale_generation', 'StaleGeneration') === true,
          channelID: integer(field(row, 'channel_id', 'ChannelID')),
          protocol: text(field(row, 'upstream_protocol', 'UpstreamProtocol')),
          httpStatus: integer(field(row, 'http_status', 'HTTPStatus')),
          requestID: text(field(row, 'request_id', 'RequestID')),
          upstreamRequestID: text(
            field(row, 'upstream_request_id', 'UpstreamRequestID'),
          ),
          providerErrorCode: text(
            field(row, 'provider_error_code', 'ProviderErrorCode'),
          ),
          finishReasons: list(
            field(row, 'finish_reasons', 'FinishReasons') ??
              field(metadata, 'finish_reasons', 'FinishReasons'),
          )
            .filter((value) => typeof value === 'string')
            .slice(0, 64)
            .map(text),
          terminalStatus: text(
            field(row, 'terminal_status', 'TerminalStatus') ??
              field(metadata, 'terminal_status', 'TerminalStatus'),
          ),
          completedAt: integer(field(row, 'completed_at', 'CompletedAt')),
        };
      })
      .sort((left, right) => right.sequence - left.sequence),
    incidents: list(data.incidents)
      .slice(0, 20)
      .map((row) => ({
        id: integer(field(row, 'id', 'ID')),
        generation: integer(field(row, 'generation', 'Generation')),
        rule: text(field(row, 'rule', 'Rule')),
        failures: integer(field(row, 'failures', 'Failures')),
        validSamples: integer(field(row, 'valid_samples', 'ValidSamples')),
        triggerEventID: integer(
          field(row, 'trigger_event_id', 'TriggerEventID'),
        ),
        openedAt: integer(field(row, 'opened_at', 'OpenedAt')),
        unpublishedAt: integer(field(row, 'unpublished_at', 'UnpublishedAt')),
        recoveredAt: integer(field(row, 'recovered_at', 'RecoveredAt')),
        recoveryOperatorID: integer(
          field(row, 'recovery_operator_id', 'RecoveryOperatorID'),
        ),
        recoveryEvidence: text(
          field(row, 'recovery_evidence', 'RecoveryEvidence'),
        ),
      })),
    outbox: list(data.outbox)
      .slice(0, 50)
      .map((row) => ({
        id: integer(field(row, 'id', 'ID')),
        kind: text(field(row, 'kind', 'Kind')),
        incidentID: integer(field(row, 'incident_id', 'IncidentID')),
        eventID: integer(field(row, 'event_id', 'EventID')),
        status: text(field(row, 'status', 'Status')),
        attempts: integer(field(row, 'attempts', 'Attempts')),
        nextAttemptAt: integer(field(row, 'next_attempt_at', 'NextAttemptAt')),
        lastError: text(field(row, 'last_error', 'LastError')),
        deliveredAt: integer(field(row, 'delivered_at', 'DeliveredAt')),
      })),
  };
}

function validModelID(id) {
  if (!Number.isSafeInteger(id) || id <= 0) throw new Error('invalid_model_id');
}

export async function loadModelHealth(modelID) {
  validModelID(modelID);
  const health = normalizeModelHealth(
    await adminRequest({
      method: 'GET',
      url: `/api/models/ztapi/${modelID}/health`,
    }),
  );
  if (
    health.modelID !== modelID ||
    (health.state?.modelID && health.state.modelID !== modelID)
  )
    throw new Error('invalid_health_model');
  return health;
}

export function canRecoverModelHealth(health, canWrite) {
  return (
    canWrite === true &&
    health?.observed === true &&
    health?.state?.open === true &&
    Number.isSafeInteger(health.state.generation) &&
    health.state.generation > 0
  );
}

export function healthEvidenceBytes(evidence) {
  return new TextEncoder().encode(String(evidence).trim()).length;
}

export async function recoverModelHealth(
  modelID,
  health,
  { canWrite, evidence, confirmed },
) {
  validModelID(modelID);
  const trimmed = String(evidence || '').trim();
  if (
    health.modelID !== modelID ||
    !canRecoverModelHealth(health, canWrite) ||
    confirmed !== true ||
    !trimmed ||
    healthEvidenceBytes(trimmed) > 4096
  )
    throw new Error('invalid_recovery_confirmation');
  const response = await adminRequest({
    method: 'POST',
    url: `/api/models/ztapi/${modelID}/health/recover`,
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      generation: health.state.generation,
      evidence: trimmed,
    }),
  });
  if (
    response?.model_id !== modelID ||
    response?.recovered !== true ||
    response?.published !== false
  )
    throw new Error('unconfirmed_recovery_response');
  return response;
}

export async function loadHealthWorkerStatus() {
  const data = await adminRequest({
    method: 'GET',
    url: '/api/models/ztapi/health/status',
  });
  if (!Array.isArray(data?.worker_status))
    throw new Error('invalid_worker_status');
  const allocated = integer(data.probe_budget?.allocated_nano_usd);
  const accounted = integer(data.probe_budget?.accounted_nano_usd);
  return {
    enabled: data.enabled === true,
    workers: data.worker_status.slice(0, 6).map((row) => ({
      component: text(row.component),
      code: text(row.code),
      updatedAt: integer(row.updated_at),
    })),
    budget:
      allocated !== null && accounted !== null
        ? { allocated, accounted }
        : null,
  };
}

export function healthErrorMessage(error, operation = 'read') {
  if (error?.status === 401) return '管理员会话已失效，请重新登录。';
  if (error?.status === 403)
    return operation === 'recover'
      ? '没有模型写入权限，无法执行人工恢复。'
      : '没有查看模型健康状态的权限。';
  if (error?.status === 409)
    return '健康代次已变化，请刷新状态后重新核对并确认。';
  if (operation === 'recover') {
    if (error?.status === 400)
      return '复验记录或健康代次无效，请刷新状态后重新核对。';
    return '恢复结果未确认，请刷新状态后再操作。';
  }
  return '健康状态暂时不可用，请重试。';
}
