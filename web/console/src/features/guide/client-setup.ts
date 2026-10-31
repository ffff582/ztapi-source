import type { UserModelCatalogItem, UserModelEndpointType } from '../../api/contracts';

export type GuideClient = 'openai' | 'codex' | 'claude-code';

export const guideClients: ReadonlyArray<{ id: GuideClient; label: string }> = [
  { id: 'openai', label: 'OpenAI 兼容 Chat / SDK' },
  { id: 'codex', label: 'Codex CLI / Responses' },
  { id: 'claude-code', label: 'Claude Code / Messages' },
];

const clientDetails: Record<GuideClient, {
  endpoint: UserModelEndpointType;
  path: string;
  baseURL: string;
  auth: string;
  format: string;
  snippet: string;
}> = {
  openai: {
    endpoint: 'openai', path: '/v1/chat/completions', baseURL: 'https://ztapi.vip/v1',
    auth: 'Authorization: Bearer YOUR_ZTAPI_API_KEY', format: 'Python / OpenAI SDK',
    snippet: [
      '# PowerShell: $env:ZTAPI_API_KEY = "YOUR_ZTAPI_API_KEY"',
      'import os',
      'from openai import OpenAI',
      '',
      'client = OpenAI(',
      '    api_key=os.environ["ZTAPI_API_KEY"],',
      '    base_url="https://ztapi.vip/v1",',
      ')',
      'response = client.chat.completions.create(',
      '    model="YOUR_MODEL_ID",',
      '    messages=[{"role": "user", "content": "YOUR_PROMPT"}],',
      ')',
      'print(response.choices[0].message.content)',
    ].join('\n'),
  },
  codex: {
    endpoint: 'openai-response', path: '/v1/responses', baseURL: 'https://ztapi.vip/v1',
    auth: 'Authorization: Bearer YOUR_ZTAPI_API_KEY', format: '~/.codex/config.toml',
    snippet: [
      '# PowerShell: $env:ZTAPI_API_KEY = "YOUR_ZTAPI_API_KEY"',
      'model_provider = "ztapi"',
      'model = "YOUR_MODEL_ID"',
      '',
      '[model_providers.ztapi]',
      'name = "ZTAPI"',
      'base_url = "https://ztapi.vip/v1"',
      'wire_api = "responses"',
      'env_key = "ZTAPI_API_KEY"',
      'requires_openai_auth = false',
    ].join('\n'),
  },
  'claude-code': {
    endpoint: 'anthropic', path: '/v1/messages', baseURL: 'https://ztapi.vip',
    auth: 'x-api-key: YOUR_ZTAPI_API_KEY', format: 'PowerShell / Claude Code',
    snippet: [
      '$env:ANTHROPIC_BASE_URL = "https://ztapi.vip"',
      '$env:ANTHROPIC_API_KEY = "YOUR_ZTAPI_API_KEY"',
      'Remove-Item Env:ANTHROPIC_AUTH_TOKEN -ErrorAction SilentlyContinue',
      'claude --model "YOUR_MODEL_ID"',
    ].join('\n'),
  },
};

type SetupModel = Pick<UserModelCatalogItem, 'model_name' | 'modality' | 'supported_endpoint_types'>;

export function getClientSetup(model: SetupModel | undefined, client: GuideClient) {
  const details = clientDetails[client];
  let status: 'verified' | 'declared' | 'unsupported' | 'unverified';
  let message: string;
  if (!model) {
    status = 'unverified';
    message = '未验证：请选择当前账户目录中的模型。';
  } else if (model.modality !== 'text') {
    status = 'unsupported';
    message = '不支持此文本客户端配置：向量、图片和视频模型需使用各自专用端点，不能作为聊天或编码模型。';
  } else if (client === 'codex' && model.model_name.toLowerCase().includes('opus')) {
    status = 'unverified';
    message = '未验证：Codex Opus 完整上游客户端验收尚未完成；验收代理冲突已修复，隔离工具链回归通过，但不能替代真实上游验收。';
  } else if (client === 'codex' && model.model_name === 'zt-kimi-k2.6'
    && model.supported_endpoint_types.includes('openai')) {
    status = 'verified';
    message = '2026-10-31 已验收：Codex 0.165.0-alpha.1.2，Kimi K2.6 经 Responses 转换完成工具往返、补丁写入和命令执行；不代表所有客户端版本或扩展能力。';
  } else if (client === 'claude-code' && model.model_name === 'zt-claude-sonnet-5'
    && model.supported_endpoint_types.includes('anthropic')) {
    status = 'verified';
    message = '已验收：Claude Code 2.1.312，Sonnet 5 经 Messages 完成读取、写入和工具往返；沿用 R3 验收证据，未宣称所有扩展能力。';
  } else if (model.supported_endpoint_types.includes(details.endpoint)) {
    status = 'declared';
    message = '目录已声明所需端点；所选模型与客户端版本的完整兼容性仍未验证。';
  } else if (client === 'codex' && model.supported_endpoint_types.includes('openai')) {
    status = 'unverified';
    message = '未验证：目录未声明 Responses；部分路由存在 Responses → Chat 转换，但不能据此推断所选模型支持 Codex。';
  } else {
    status = 'unsupported';
    message = '不支持此配置：当前模型目录未声明该客户端所需端点，请换用匹配的客户端；这不是对所有后端能力的判定。';
  }
  const snippet = model && (status === 'verified' || status === 'declared')
    ? details.snippet.replace('"YOUR_MODEL_ID"', () => client === 'claude-code'
      ? `'${model.model_name.replaceAll("'", "''")}'`
      : JSON.stringify(model.model_name))
    : null;
  return { ...details, status, message, snippet };
}
