import { readFileSync, existsSync } from 'node:fs';
import { resolve } from 'node:path';
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { LocaleProvider } from '../../i18n/locale';
import { PublicDocsPage } from './PublicDocsPage';

function renderDocs(section = 'integration', hash = '') {
  return render(<LocaleProvider><MemoryRouter initialEntries={[`/docs/${section}${hash}`]}>
    <Routes><Route path="/docs/:section" element={<PublicDocsPage />} /></Routes>
  </MemoryRouter></LocaleProvider>);
}

afterEach(() => {
  cleanup();
  Reflect.deleteProperty(navigator, 'clipboard');
  vi.restoreAllMocks();
});

describe('public documentation', () => {
  it('explains resource boundaries publicly without certifying individual models', () => {
    renderDocs('resource-disclosure');
    expect(screen.getByRole('heading', { level: 1, name: '资源来源与价格说明' })).toBeVisible();
    const article = screen.getByRole('article');
    expect(article).toHaveTextContent('技术反向代理');
    expect(article).toHaveTextContent('网页或订阅账号池');
    expect(article).toHaveTextContent('反代本身既不能证明模型被掺水，也不能证明模型真实');
    expect(article).toHaveTextContent('ZTAPI 不主动将所选型号替换为其他型号或静默降级');
    expect(article).toHaveTextContent('逐模型来源未经独立核验，不作来源认证');
    expect(article).toHaveTextContent('请求编号');
    expect(article).toHaveTextContent('人工核查');
    expect(article).toHaveTextContent('高于官网');
    expect(within(article).getByRole('link', { name: '查看当前模型与价格' })).toHaveAttribute('href', '/models');
    fireEvent.click(screen.getByRole('button', { name: '切换到英文' }));
    expect(screen.getByRole('heading', { level: 1, name: 'Resource sources and pricing' })).toBeVisible();
    expect(article).toHaveTextContent('does not actively replace');
  });

  it('puts one-click script downloads in the Codex heading before troubleshooting', () => {
    renderDocs('codex');
    const downloads = screen.getByRole('navigation', { name: 'Codex 一键脚本' });
    expect(downloads.closest('.docs-heading')).not.toBeNull();
    expect(within(downloads).getByRole('link', { name: '下载 Bash 启动脚本' })).toHaveAttribute('download');
    expect(within(downloads).getByRole('link', { name: '下载 PowerShell 启动脚本' })).toHaveAttribute('download');
    expect(within(downloads).getByRole('link', { name: '查看一键启动命令' })).toHaveAttribute('href', '#quick-launch');
    const launcher = screen.getByRole('heading', { name: '推荐：CLI 安全启动脚本' }).closest('section');
    expect(launcher).toHaveAttribute('id', 'quick-launch');
    const warning = screen.getByRole('heading', { name: '先解决 Missing environment variable' });
    expect(downloads.compareDocumentPosition(warning) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });
  it('opens homepage quick-launch deep links at the actual launcher section', () => {
    const scroll = vi.fn();
    const prior = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'scrollIntoView');
    Object.defineProperty(HTMLElement.prototype, 'scrollIntoView', { configurable: true, value: scroll });
    try {
      renderDocs('codex', '#quick-launch');
      expect(scroll).toHaveBeenCalledWith({ block: 'start' });
      expect(scroll.mock.instances[0]).toBe(document.getElementById('quick-launch'));
    } finally {
      if (prior) Object.defineProperty(HTMLElement.prototype, 'scrollIntoView', prior);
      else Reflect.deleteProperty(HTMLElement.prototype, 'scrollIntoView');
    }
  });
  it('explains the exact missing-variable error before the manual configuration and offers safe launchers', () => {
    renderDocs('codex');
    const text = document.body.textContent ?? '';
    expect(text).toContain('Missing environment variable');
    expect(text).toContain('env_key 是环境变量名称，不是密钥本身');
    expect(text).toContain('auth.json');
    expect(text).toContain('同一个终端');
    expect(text.indexOf('Missing environment variable')).toBeLessThan(text.indexOf('2. 合并完整配置'));
    expect(screen.getByRole('link', { name: '下载 Bash 启动脚本' })).toHaveAttribute('href', '/assets/ztapi-codex-20261104.sh');
    expect(screen.getByRole('link', { name: '下载 PowerShell 启动脚本' })).toHaveAttribute('href', '/assets/ztapi-codex-20261104.ps1');
    expect(text).not.toContain('Set-ExecutionPolicy');
  });
  it.each([
    ['api', 'API 手册'], ['integration', '集成指南'], ['codex', 'Codex 接入'], ['user-guide', '用户指南'], ['faq', '常见问题'], ['resource-disclosure', '资源来源与价格说明'],
  ])('keeps section navigation and model links public on %s', (section, title) => {
    renderDocs(section);
    expect(screen.getByRole('heading', { level: 1, name: title })).toBeVisible();
    const nav = screen.getByRole('navigation', { name: '文档分类' });
    expect(within(nav).getAllByRole('link')).toHaveLength(6);
    expect(within(nav).getByRole('link', { name: title })).toHaveAttribute('aria-current', 'page');
    expect(screen.getByRole('link', { name: '查看模型目录' })).toHaveAttribute('href', '/models');
  });

  it('copies the displayed example and confirms success', async () => {
    let copied = '';
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: {
      writeText: async (text: string) => { copied = text; },
    } });
    renderDocs();
    fireEvent.click(screen.getByRole('button', { name: '复制 Python 示例' }));
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('已复制'));
    expect(copied).toBe(document.querySelector('.docs-code pre code')?.textContent);
    expect(copied).toContain('YOUR_MODEL_ID');
  });

  it('shows an actionable clipboard failure without claiming success', async () => {
    renderDocs();
    fireEvent.click(screen.getByRole('button', { name: '复制 Python 示例' }));
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('复制失败，请手动选择代码'));
  });

  it('switches example prompt and document text to English', () => {
    renderDocs();
    fireEvent.click(screen.getByRole('button', { name: '切换到英文' }));
    expect(screen.getByRole('heading', { level: 1, name: 'Integration guide' })).toBeVisible();
    expect(document.querySelector('.docs-code pre code')).toHaveTextContent('Hello');
    expect(document.querySelector('.docs-code pre code')).not.toHaveTextContent('你好');
    expect(screen.getByRole('button', { name: 'Copy Python example' })).toBeVisible();
  });

  it('publishes the public documentation index with the same endpoint and section manifest', () => {
    const file = resolve(process.cwd(), 'public/llms.txt');
    expect(existsSync(file)).toBe(true);
    const index = readFileSync(file, 'utf8');
    for (const path of ['api', 'integration', 'codex', 'user-guide', 'faq']) {
      expect(index).toContain(`https://ztapi.vip/docs/${path}`);
    }
    expect(index).toContain('/v1/responses');
    expect(index).toContain('/v1/messages');
    expect(index).toContain('/v1/audio/transcriptions');
    expect(index).toContain('/v1/images/edits');
    expect(index).toContain('/v1/videos/{task_id}/content');
    expect(index).toContain('/v1beta/models/{model}:generateContent');
    expect(index).toContain('/v1/video/generations/{task_id}');
    expect(index).toContain('Never silently substitute a different model');
    expect(index).toContain('model_catalog_url="https://ztapi.vip/codex/models"');
  });

  it('publishes complete Codex configuration with root live search and provider-scoped authenticated catalog', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
    renderDocs('codex');
    fireEvent.click(screen.getByRole('button', { name: '复制 config.toml 示例' }));
    await waitFor(() => expect(writeText).toHaveBeenCalledTimes(1));
    const config = writeText.mock.calls[0][0] as string;
    expect(config).toContain('model = "zt-claude-opus-5"');
    expect(config).toContain('model_provider = "ztapi"');
    expect(config.indexOf('web_search = "live"')).toBeLessThan(config.indexOf('[model_providers.ztapi]'));
    expect(config).toContain('wire_api = "responses"');
    expect(config).toContain('env_key = "ZTAPI_API_KEY"');
    expect(config).toContain('requires_openai_auth = false');
    expect(config).toContain('model_catalog_url = "https://ztapi.vip/codex/models"');
    expect(config.indexOf('model_catalog_url')).toBeGreaterThan(config.indexOf('[model_providers.ztapi]'));
    expect(config).not.toMatch(/approval_policy|danger-full-access|experimental_bearer_token|model_catalog_json|web_search = "disabled"/);
    expect([...document.querySelectorAll('pre code')].some(e => e.textContent === config)).toBe(true);
    expect(screen.getByText(/0\.01 元/)).toBeVisible();
    expect(screen.getByText(/0\.165\.0-alpha\.1\.2/)).toBeVisible();
  });

  it('provides secret-safe Windows and POSIX setup, desktop environment guidance and explicit search/read prompts', () => {
    renderDocs('codex');
    const text = document.body.textContent ?? '';
    expect(text).toContain('%USERPROFILE%\\.codex\\config.toml');
    expect(text).toContain('~/.codex/config.toml');
    expect(text).toContain('Read-Host');
    expect(text).toContain('-AsSecureString');
    expect(text).toContain('export ZTAPI_API_KEY');
    expect(text).toContain('桌面版');
    expect(screen.getByRole('button', { name: '复制 搜索验证 示例' })).toBeVisible();
    expect(screen.getByRole('button', { name: '复制 网页读取验证 示例' })).toBeVisible();
    expect(screen.getByText(/不要覆盖已有配置/)).toBeVisible();
    expect(screen.getByRole('link', { name: 'OpenAI 官方网关配置' })).toHaveAttribute('href', expect.stringContaining('learn.chatgpt.com/docs/enterprise/connect-to-a-gateway'));
  });

  it('links the public integration guide to complete Codex setup', () => {
    renderDocs();
    expect(screen.getByRole('link', { name: '查看 Codex 完整接入说明' })).toHaveAttribute('href', '/docs/codex');
  });

  it('translates the Codex guide without translating configuration fields', () => {
    renderDocs('codex');
    fireEvent.click(screen.getByRole('button', { name: '切换到英文' }));
    expect(screen.getByRole('heading', { level: 1, name: 'Codex setup' })).toBeVisible();
    expect(screen.getByRole('heading', { name: 'Tool fees and limitations' })).toBeVisible();
    expect(screen.getByRole('button', { name: 'Copy config.toml example' })).toBeVisible();
    expect(screen.getByRole('button', { name: 'Copy config.toml example' }).closest('.docs-code')?.querySelector('pre code')).toHaveTextContent('model_catalog_url');
    expect(screen.getByRole('button', { name: 'Copy Search check example' })).toBeVisible();
    expect(screen.getByRole('button', { name: 'Copy Windows user environment variable example' })).toBeVisible();
    expect(screen.getByRole('button', { name: 'Copy macOS desktop session example' })).toBeVisible();
  });

  it('does not keep a stale copy confirmation after changing the displayed prompt language', async () => {
    let done!: () => void;
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: {
      writeText: () => new Promise<void>(resolve => { done = resolve; }),
    } });
    renderDocs('codex');
    fireEvent.click(screen.getByRole('button', { name: '复制 搜索验证 示例' }));
    fireEvent.click(screen.getByRole('button', { name: '切换到英文' }));
    await act(async () => { done(); await Promise.resolve(); });
    await waitFor(() => expect(screen.getAllByRole('status').every(e => e.textContent === '')).toBe(true));
  });
});
