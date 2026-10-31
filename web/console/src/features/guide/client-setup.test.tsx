import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { clearAuthSession, setAuthSession } from '../../api/client';
import { UsageGuidePage } from './UsageGuidePage';
import { getClientSetup } from './client-setup';

function model(model_name: string, supported_endpoint_types = ['openai'], overrides = {}) {
  return {
    modality: 'text', model_name, supported_endpoint_types,
    provider_family: 'openai', provider_name: 'OpenAI', protocol: 'openai_compatible',
    enable_groups: ['default'], input_price_per_million: '1', output_price_per_million: '2',
    billing_dimensions: ['input_tokens', 'output_tokens'],
    sale_usd: { input_tokens: '1', output_tokens: '2' },
    billing_rule: 'token', pricing_version: 'guide-fixture', ...overrides,
  };
}

const models = [
  model('public-chat', ['openai', 'anthropic']),
  model('public-responses', ['openai-response']),
  model('zt-claude-opus-5', ['openai', 'anthropic'], { provider_family: 'anthropic' }),
  model('public-native-messages', ['anthropic'], { protocol: 'anthropic' }),
  model('claude-misleading-name', ['gemini'], { protocol: 'gemini', provider_family: 'google' }),
  model('public-vector', ['embeddings'], {
    modality: 'embedding', output_price_per_million: '0',
    billing_dimensions: ['input_tokens'], sale_usd: { input_tokens: '1' }, billing_rule: 'input_only',
  }),
  model('zt-kimi-k2.6', ['openai', 'anthropic']),
  model('zt-claude-sonnet-5', ['openai', 'anthropic']),
];

function response(catalog = models) {
  return new Response(JSON.stringify({ success: true, data: catalog.map((item) => item.model_name), catalog }));
}

function session(id = 1) {
  setAuthSession({ access_token: 'guide-session-fixture', expires_in: 900,
    user: { id, username: 'guide-fixture', group: 'default', role: 1 } });
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

function clipboard(writeText = vi.fn().mockResolvedValue(undefined)) {
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
  return writeText;
}

async function setup() {
  render(<MemoryRouter><UsageGuidePage /></MemoryRouter>);
  const guide = within(screen.getByRole('region', { name: '模型与客户端配置' }));
  await guide.findByRole('option', { name: 'public-chat' });
  return guide;
}

function select(guide: ReturnType<typeof within>, selectedModel: string, client = 'openai') {
  fireEvent.change(guide.getByLabelText('接入模型'), { target: { value: selectedModel } });
  fireEvent.change(guide.getByLabelText('客户端'), { target: { value: client } });
}

beforeEach(() => {
  session();
  vi.stubGlobal('fetch', vi.fn().mockImplementation(() => Promise.resolve(response())));
});

afterEach(() => {
  cleanup();
  clearAuthSession();
  Reflect.deleteProperty(navigator, 'clipboard');
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('model-aware client setup', () => {
  it('loads only the account catalog and keeps configuration unavailable until a model is selected', async () => {
    const guide = await setup();
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(fetch).toHaveBeenCalledWith('/api/user/models', expect.objectContaining({ method: 'GET' }));
    expect(guide.queryByRole('button', { name: '复制配置' })).not.toBeInTheDocument();
    expect(guide.getByRole('note', { name: '客户端接入状态' })).toHaveTextContent('未验证');
    expect(guide.queryByLabelText(/API Key/)).not.toBeInTheDocument();
  });

  it('generates OpenAI SDK setup with the selected public model and placeholder credentials', async () => {
    const guide = await setup();
    select(guide, 'public-chat');
    expect(guide.getByLabelText('所选模型 ID')).toHaveTextContent('public-chat');
    expect(guide.getByRole('note', { name: '客户端接入状态' })).toHaveTextContent('目录已声明');
    const code = guide.getByTestId('client-setup-code');
    expect(code).toHaveTextContent('base_url="https://ztapi.vip/v1"');
    expect(code).toHaveTextContent('os.environ["ZTAPI_API_KEY"]');
    expect(code).toHaveTextContent('model="public-chat"');
    expect(code).toHaveTextContent('client.chat.completions.create(');
    expect(code).not.toHaveTextContent(/guide-session-fixture|YOUR_MODEL_ID/);
    expect(guide.getByRole('note', { name: '能力边界' })).toHaveTextContent('未验证');
  });

  it('uses Responses for Codex without Chat, stored login, or fabricated model metadata', async () => {
    const guide = await setup();
    select(guide, 'public-responses', 'codex');
    const code = guide.getByTestId('client-setup-code');
    expect(code).toHaveTextContent('model_provider = "ztapi"');
    expect(code).toHaveTextContent('model = "public-responses"');
    expect(code).toHaveTextContent('[model_providers.ztapi]');
    expect(code).toHaveTextContent('base_url = "https://ztapi.vip/v1"');
    expect(code).toHaveTextContent('wire_api = "responses"');
    expect(code).toHaveTextContent('env_key = "ZTAPI_API_KEY"');
    expect(code).toHaveTextContent('requires_openai_auth = false');
    expect(code).not.toHaveTextContent(/chat.completions|model_catalog_json|OPENAI_API_KEY|approval_policy/);
    expect(guide.getByRole('note', { name: '客户端接入状态' })).toHaveTextContent('未验证');
    expect(guide.getByText('POST /v1/responses')).toBeVisible();
  });

  it('uses the origin without /v1 and API-key auth for Claude Code Messages', async () => {
    const guide = await setup();
    select(guide, 'public-chat', 'claude-code');
    const code = guide.getByTestId('client-setup-code');
    expect(code).toHaveTextContent('$env:ANTHROPIC_BASE_URL = "https://ztapi.vip"');
    expect(code).toHaveTextContent('$env:ANTHROPIC_API_KEY = "YOUR_ZTAPI_API_KEY"');
    expect(code).toHaveTextContent('Remove-Item Env:ANTHROPIC_AUTH_TOKEN -ErrorAction SilentlyContinue');
    expect(code).toHaveTextContent("claude --model 'public-chat'");
    expect(code).not.toHaveTextContent(/ztapi.vip\/v1|Bearer|YOUR_MODEL_ID/);
    expect(guide.getByText('POST /v1/messages')).toBeVisible();
    expect(guide.getByText('x-api-key: YOUR_ZTAPI_API_KEY')).toBeVisible();
    select(guide, 'public-native-messages', 'claude-code');
    expect(guide.getByTestId('client-setup-code')).toHaveTextContent('ANTHROPIC_API_KEY');
  });

  it('blocks chat SDK setup for Responses-only models instead of reusing the chat example', async () => {
    const guide = await setup();
    select(guide, 'public-responses');
    expect(guide.getByRole('note', { name: '客户端接入状态' })).toHaveTextContent('不支持');
    expect(guide.queryByTestId('client-setup-code')).not.toBeInTheDocument();
    expect(guide.queryByRole('button', { name: '复制配置' })).not.toBeInTheDocument();
  });

  it('does not infer Codex Responses support from a Chat endpoint or a Claude-like model name', async () => {
    const guide = await setup();
    select(guide, 'public-chat', 'codex');
    expect(guide.getByRole('note', { name: '客户端接入状态' })).toHaveTextContent('未验证');
    expect(guide.queryByTestId('client-setup-code')).not.toBeInTheDocument();
    select(guide, 'claude-misleading-name', 'claude-code');
    expect(guide.getByRole('note', { name: '客户端接入状态' })).toHaveTextContent('不支持');
    expect(guide.queryByTestId('client-setup-code')).not.toBeInTheDocument();
  });

  it('keeps Codex Opus explicitly unresolved with no copyable claim of completed setup', async () => {
    const guide = await setup();
    select(guide, 'zt-claude-opus-5', 'codex');
    expect(guide.getByRole('note', { name: '客户端接入状态' })).toHaveTextContent('完整上游客户端验收尚未完成');
    expect(guide.queryByRole('button', { name: '复制配置' })).not.toBeInTheDocument();
    select(guide, 'zt-claude-opus-5', 'claude-code');
    expect(guide.getByTestId('client-setup-code')).toHaveTextContent('ANTHROPIC_API_KEY');
  });

  it('provides the accepted Kimi Codex conversion configuration without claiming all endpoints passed', async () => {
    const guide = await setup();
    select(guide, 'zt-kimi-k2.6', 'codex');
    expect(guide.getByRole('note', { name: '客户端接入状态' })).toHaveTextContent('0.165.0-alpha.1.2');
    expect(guide.getByRole('note', { name: '客户端接入状态' })).toHaveTextContent('2026-10-31');
    expect(guide.getByTestId('client-setup-code')).toHaveTextContent('model = "zt-kimi-k2.6"');
    select(guide, 'zt-claude-sonnet-5', 'claude-code');
    expect(guide.getByRole('note', { name: '客户端接入状态' })).toHaveTextContent('2.1.312');
    expect(guide.getByTestId('client-setup-code')).toHaveTextContent("claude --model 'zt-claude-sonnet-5'");
  });

  it('escapes public model IDs as data in Python, TOML and PowerShell', () => {
    const selected = { model_name: 'public-"\\\n\'$env:SECRET', modality: 'text' as const,
      supported_endpoint_types: ['openai', 'openai-response', 'anthropic'] as const };
    const safe = { ...selected, supported_endpoint_types: [...selected.supported_endpoint_types] };
    expect(getClientSetup(safe, 'openai').snippet).toContain(`model=${JSON.stringify(selected.model_name)}`);
    expect(getClientSetup(safe, 'codex').snippet).toContain(`model = ${JSON.stringify(selected.model_name)}`);
    expect(getClientSetup(safe, 'claude-code').snippet).toContain(`claude --model '${selected.model_name.replaceAll("'", "''")}'`);
  });

  it('does not configure embedding models as coding or chat clients', async () => {
    const guide = await setup();
    for (const client of ['openai', 'codex', 'claude-code']) {
      select(guide, 'public-vector', client);
      expect(guide.getByRole('note', { name: '客户端接入状态' })).toHaveTextContent('不支持');
      expect(guide.queryByTestId('client-setup-code')).not.toBeInTheDocument();
    }
  });

  it('copies only the displayed placeholder configuration and resets the result on model/client changes', async () => {
    const writeText = clipboard();
    const guide = await setup();
    select(guide, 'public-chat');
    fireEvent.click(guide.getByRole('button', { name: '复制配置' }));
    await waitFor(() => expect(guide.getByRole('status')).toHaveTextContent('已复制'));
    expect(writeText).toHaveBeenLastCalledWith(guide.getByTestId('client-setup-code').textContent);
    select(guide, 'public-chat', 'claude-code');
    expect(guide.getByRole('status')).toBeEmptyDOMElement();
    fireEvent.click(guide.getByRole('button', { name: '复制配置' }));
    await waitFor(() => expect(guide.getByRole('status')).toHaveTextContent('已复制'));
    select(guide, 'public-native-messages', 'claude-code');
    expect(guide.getByRole('status')).toBeEmptyDOMElement();
    expect(writeText).toHaveBeenCalledTimes(2);
    expect(fetch).toHaveBeenCalledTimes(1);
  });

  it.each(['missing', 'denied'])('announces %s clipboard access without leaking an exception', async (kind) => {
    if (kind === 'denied') clipboard(vi.fn().mockRejectedValue(new Error('private-error-body')));
    const guide = await setup();
    select(guide, 'public-chat');
    fireEvent.click(guide.getByRole('button', { name: '复制配置' }));
    await waitFor(() => expect(guide.getByRole('status')).toHaveTextContent('复制失败'));
    expect(guide.getByRole('status')).not.toHaveTextContent('private-error-body');
  });

  it('ignores a clipboard completion after switching to another model', async () => {
    const pending = deferred<void>();
    clipboard(vi.fn().mockReturnValue(pending.promise));
    const guide = await setup();
    select(guide, 'public-chat', 'claude-code');
    fireEvent.click(guide.getByRole('button', { name: '复制配置' }));
    select(guide, 'public-native-messages', 'claude-code');
    await act(async () => { pending.resolve(); await pending.promise; });
    expect(guide.getByRole('status')).toBeEmptyDOMElement();
  });

  it('exposes loading, safe failure, and a read-only catalog retry', async () => {
    const pending = deferred<Response>();
    vi.mocked(fetch).mockReturnValueOnce(pending.promise).mockResolvedValueOnce(response());
    render(<MemoryRouter><UsageGuidePage /></MemoryRouter>);
    const guide = within(screen.getByRole('region', { name: '模型与客户端配置' }));
    expect(guide.getByText('正在加载模型目录...')).toBeVisible();
    expect(guide.getByLabelText('接入模型')).toBeDisabled();
    await act(async () => { pending.resolve(new Response(JSON.stringify({ success: false, message: 'private-provider-error' }), { status: 500 })); });
    expect(await guide.findByRole('alert')).toHaveTextContent('模型目录加载失败');
    expect(guide.getByRole('alert')).not.toHaveTextContent('private-provider-error');
    fireEvent.click(guide.getByRole('button', { name: '重试加载模型' }));
    expect(await guide.findByRole('option', { name: 'public-chat' })).toBeInTheDocument();
    expect(fetch).toHaveBeenCalledTimes(2);
  });

  it.each(['empty', 'malformed'])('fails closed for an %s catalog', async (kind) => {
    vi.mocked(fetch).mockResolvedValueOnce(kind === 'empty' ? response([]) : new Response(JSON.stringify({ success: true, data: ['unverified-model'] })));
    render(<MemoryRouter><UsageGuidePage /></MemoryRouter>);
    const guide = within(screen.getByRole('region', { name: '模型与客户端配置' }));
    if (kind === 'empty') expect(await guide.findByText('当前账户暂无可调用模型。')).toBeVisible();
    else expect(await guide.findByRole('alert')).toHaveTextContent('模型目录加载失败');
    expect(guide.queryByRole('button', { name: '复制配置' })).not.toBeInTheDocument();
  });

  it('invalidates the selected model and ignores old catalog completions when the account changes', async () => {
    const stale = deferred<Response>();
    vi.mocked(fetch).mockReturnValueOnce(stale.promise).mockResolvedValueOnce(response([model('account-two-model')]));
    render(<MemoryRouter><UsageGuidePage /></MemoryRouter>);
    const guide = within(screen.getByRole('region', { name: '模型与客户端配置' }));
    act(() => session(2));
    expect(await guide.findByRole('option', { name: 'account-two-model' })).toBeInTheDocument();
    await act(async () => { stale.resolve(response()); await stale.promise; });
    expect(guide.queryByRole('option', { name: 'public-chat' })).not.toBeInTheDocument();
    expect(guide.queryByRole('button', { name: '复制配置' })).not.toBeInTheDocument();
  });
});
