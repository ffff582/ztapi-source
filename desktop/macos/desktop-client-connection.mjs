import { createHash } from 'node:crypto';
import path from 'node:path';
import { invokeMacEngine } from '../desktop/macos/engine-host.mjs';
import { createProjectRpc } from '../desktop/macos/engine-project-rpc.mjs';
import { MacProjectTask } from '../desktop/macos/project-task.mjs';
import { createMacProjectPorts } from '../desktop/macos/project-ports.mjs';
import { createReviewDiff } from './review-diff.mjs';
import { startClaudeTaskBroker } from './open-code-dependencies.mjs';
import { runOpenCodeProject } from './desktop-project-runner.mjs';
import { projectTools } from './project-tool-spec.mjs';

const bundleId = 'com.ztapi.workbench.engine';
const engineModel = 'ztapi-task-model';

export async function invokeMacSandbox({ launch, gateway, taskId, model, engineModel, tools, scope,
  input, inspect, stage, requests, toolLimit, signal, onReceipt, onEvidence }) {
  const rpc = inspect ? createProjectRpc({
    tools: projectTools, limit: toolLimit, execute: (name, input) => tools.execute(name, input),
  }) : null;
  const broker = await startClaudeTaskBroker({ task: taskId, exchange: gateway.exchange,
    model, engineModel, stage, maxRequests: requests, scope, onReceipt });
  const seen = new Set();
  let calls = 0;
  const fetchReply = async ({ channel, body }, life) => {
    const stop = signal ? AbortSignal.any([signal, life]) : life;
    stop.throwIfAborted();
    if (scope.failure()) throw Error(scope.failure());
    if (channel === 'projectTool' && rpc) return rpc(body);
    if (channel !== 'model') throw Error('MAC_ENGINE_MODEL_REJECTED');
    const digest = createHash('sha256').update(body).digest('hex');
    if (seen.has(digest) || calls >= requests) throw Error('MAC_ENGINE_MODEL_REJECTED');
    seen.add(digest);
    calls++;
    const response = await fetch(broker.baseURL + '/v1/messages', {
      method: 'POST', redirect: 'error', signal: stop, body,
      headers: { authorization: `Bearer ${broker.auth}`, 'content-type': 'application/json' },
    });
    if (scope.failure()) {
      await response.body?.cancel();
      throw Error(scope.failure());
    }
    const type = response.headers.get('content-type')?.split(';')[0]?.trim();
    const result = await response.text();
    if (Buffer.byteLength(result) > 1048576 || !['application/json', 'text/event-stream'].includes(type) ||
        response.ok && type !== 'text/event-stream') throw Error('GATEWAY_RESPONSE_UNCONFIRMED');
    return { status: response.status, type, body: result };
  };
  try {
    const result = await launch({ prompt: input, mode: inspect ? 'project' : 'text', signal, dispatch: fetchReply });
    if (scope.failure()) throw Error(scope.failure());
    if (!calls) throw Error('MAC_ENGINE_RUN_FAILED');
    onEvidence({ stage, frames: result.frames, requests: calls, sandboxVerified: true });
    return result.summary;
  } finally { await broker.close(); }
}

export function createMacConnection(resources, state) {
  const native = path.join(resources, 'ztapi-native');
  const host = path.join(native, 'ZTAPI Engine.app', 'Contents', 'MacOS', 'ZTAPIOpenCode');
  const worker = path.join(native, 'ZTAPI Tests.app', 'Contents', 'MacOS', 'ZTAPITestWorker');
  const broker = path.join(native, 'ZTAPIProjectBroker');
  const launch = options => invokeMacEngine({ ...options, host, bundleId, state });
  let preflight;
  const prepare = () => preflight ??= launch({ mode: 'probe', timeoutMs: 15000 }).catch(error => {
    preflight = undefined;
    throw error;
  });
  return {
    async projectGatewayFactory({ account, model, confirm, taskId }, run) {
      await prepare();
      return account.withClaudeProjectGateway(model, confirm, run, taskId);
    },
    async claudeTaskRunner(options) {
      return runOpenCodeProject({ ...options, launch, invokeEngine: invokeMacSandbox, engineModel });
    },
    async projectFactory(options) {
      const ports = await createMacProjectPorts({ broker, worker, state: options.state, diff: createReviewDiff });
      return new MacProjectTask(options, ports);
    },
  };
}
