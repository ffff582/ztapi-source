import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { UsageGuidePage } from './UsageGuidePage';

function renderGuide() {
  return render(
    <MemoryRouter>
      <UsageGuidePage />
    </MemoryRouter>,
  );
}

function setClipboard(writeText: ReturnType<typeof vi.fn>) {
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: { writeText },
  });
}

function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  const promise = new Promise<T>((promiseResolve) => {
    resolve = promiseResolve;
  });
  return { promise, resolve };
}

afterEach(() => {
  cleanup();
  Reflect.deleteProperty(navigator, 'clipboard');
  vi.restoreAllMocks();
});

describe('UsageGuidePage', () => {
  it('documents the verified unified endpoint and links to the required setup pages', () => {
    renderGuide();

    const reference = screen.getByRole('region', { name: '接口信息' });
    expect(within(reference).getByText('https://ztapi.vip/v1')).toBeVisible();
    expect(within(reference).getByText('POST /v1/chat/completions')).toBeVisible();
    expect(
      within(reference).getByText('Authorization: Bearer YOUR_ZTAPI_API_KEY'),
    ).toBeVisible();
    expect(screen.getByRole('link', { name: '创建 API 密钥' })).toHaveAttribute(
      'href',
      '/console/keys',
    );
    expect(screen.getByRole('link', { name: '查看可用模型' })).toHaveAttribute(
      'href',
      '/console/models',
    );
    expect(
      screen.getByText('不要在浏览器前端、移动端安装包或公开仓库中暴露 API Key。'),
    ).toBeVisible();
    expect(screen.getByRole('note', { name: '端点选择' })).toHaveTextContent('Responses-only');
    expect(screen.getByRole('note', { name: '端点选择' })).toHaveTextContent('/v1/responses');
    expect(screen.getByRole('note', { name: '端点选择' })).toHaveTextContent('不适用 Chat Completions 示例');
  });

  it('explains which upstream-reported usage is billable', () => {
    renderGuide();

    const note = screen.getByRole('note', { name: '用量与计费' });
    expect(note).toHaveTextContent('计费以上游返回的实际用量为准');
    expect(note).toHaveTextContent(
      '部分模型上游不严格遵守 max_tokens，实际输出可能超出该值并计入用量',
    );
    expect(note).not.toHaveTextContent('在推理过程中');
    expect(note).toHaveTextContent('Claude 全线');
    expect(note).toHaveTextContent('GPT 5.4 及以上');
    expect(note).toHaveTextContent('GLM / Qwen 推理系');
  });

  it('switches between cURL, Python, and Node.js integration examples', () => {
    renderGuide();

    const curlTab = screen.getByRole('tab', { name: 'cURL' });
    expect(curlTab).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByTestId('guide-code')).toHaveTextContent(
      'curl https://ztapi.vip/v1/chat/completions',
    );

    fireEvent.click(screen.getByRole('tab', { name: 'Python' }));
    expect(screen.getByTestId('guide-code')).toHaveTextContent(
      'from openai import OpenAI',
    );
    expect(screen.getByTestId('guide-code')).toHaveTextContent(
      'base_url="https://ztapi.vip/v1"',
    );
    expect(screen.getByTestId('guide-code')).toHaveTextContent(
      'os.environ["ZTAPI_API_KEY"]',
    );
    expect(screen.getByTestId('guide-code')).not.toHaveTextContent(
      'api_key="YOUR_ZTAPI_API_KEY"',
    );

    fireEvent.click(screen.getByRole('tab', { name: 'Node.js' }));
    expect(screen.getByTestId('guide-code')).toHaveTextContent(
      'import OpenAI from "openai";',
    );
    expect(screen.getByTestId('guide-code')).toHaveTextContent(
      'model: "YOUR_MODEL_ID"',
    );
  });

  it('copies the currently selected example and announces the result', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    setClipboard(writeText);
    renderGuide();

    fireEvent.click(screen.getByRole('tab', { name: 'Python' }));
    const displayedCode = screen.getByTestId('guide-code').textContent;
    fireEvent.click(screen.getByRole('button', { name: '复制代码' }));

    await waitFor(() => expect(writeText).toHaveBeenCalledWith(displayedCode));
    expect(screen.getByRole('status')).toHaveTextContent('已复制');
  });

  it('switches endpoint, SDK calls, and copied payload together for Responses-only models', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    setClipboard(writeText);
    renderGuide();
    fireEvent.change(screen.getByLabelText('调用接口'), { target: { value: 'responses' } });
    expect(screen.getByText('POST /v1/responses')).toBeVisible();
    for (const [language, expected] of [
      ['cURL', 'curl https://ztapi.vip/v1/responses'],
      ['Python', 'client.responses.create('],
      ['Node.js', 'client.responses.create({'],
    ]) {
      fireEvent.click(screen.getByRole('tab', { name: language }));
      const code = screen.getByTestId('guide-code');
      expect(code).toHaveTextContent(expected);
      expect(code).toHaveTextContent('input');
      expect(code).toHaveTextContent('max_output_tokens');
      expect(code).not.toHaveTextContent('chat/completions');
      expect(code).not.toHaveTextContent('chat.completions');
      expect(code).not.toHaveTextContent('messages');
      fireEvent.click(screen.getByRole('button', { name: '复制代码' }));
      await waitFor(() => expect(writeText).toHaveBeenLastCalledWith(code.textContent));
    }
    fireEvent.change(screen.getByLabelText('调用接口'), { target: { value: 'chat' } });
    expect(screen.getByTestId('guide-code')).toHaveTextContent('client.chat.completions.create');
    expect(screen.getByRole('status')).toBeEmptyDOMElement();
  });

  it('provides and copies three input-only embedding examples without chat or output parameters', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    setClipboard(writeText);
    renderGuide();
    fireEvent.change(screen.getByLabelText('调用接口'), { target: { value: 'embeddings' } });
    expect(screen.getByText('POST /v1/embeddings')).toBeVisible();
    for (const [language, expected] of [
      ['cURL', 'curl https://ztapi.vip/v1/embeddings'],
      ['Python', 'client.embeddings.create('], ['Node.js', 'client.embeddings.create({'],
    ]) {
      fireEvent.click(screen.getByRole('tab', { name: language }));
      const code = screen.getByTestId('guide-code');
      expect(code).toHaveTextContent(expected);
      expect(code).toHaveTextContent('YOUR_MODEL_ID');
      expect(code).toHaveTextContent('input');
      expect(code).toHaveTextContent('Hello');
      expect(code.textContent).not.toMatch(/messages|chat[./]completions|responses.create|max_.*tokens|stream|output_text/);
      if (language !== 'cURL') expect(code).toHaveTextContent('data[0].embedding');
      fireEvent.click(screen.getByRole('button', { name: '复制代码' }));
      await waitFor(() => expect(writeText).toHaveBeenLastCalledWith(code.textContent));
    }
    fireEvent.change(screen.getByLabelText('调用接口'), { target: { value: 'responses' } });
    expect(screen.getByTestId('guide-code')).toHaveTextContent('client.responses.create');
    expect(screen.getByRole('status')).toBeEmptyDOMElement();
    fireEvent.change(screen.getByLabelText('调用接口'), { target: { value: 'chat' } });
    expect(screen.getByTestId('guide-code')).toHaveTextContent('client.chat.completions.create');
  });

  it('shows exact ZTAPI image generation and asynchronous video task flows', () => {
    renderGuide();
    fireEvent.change(screen.getByLabelText('调用接口'), { target: { value: 'images' } });
    expect(screen.getByText('POST /v1/images/generations')).toBeVisible();
    expect(screen.getByTestId('guide-code')).toHaveTextContent('https://ztapi.vip/v1/images/generations');
    expect(screen.getByTestId('guide-code')).toHaveTextContent('response_format');
    expect(screen.getByTestId('guide-code')).not.toHaveTextContent(/yunxin|provider|channel_id/i);

    fireEvent.change(screen.getByLabelText('调用接口'), { target: { value: 'video-tasks' } });
    expect(screen.getByText('POST /v1/video/generations')).toBeVisible();
    expect(screen.getByTestId('guide-code')).toHaveTextContent('https://ztapi.vip/v1/video/generations');
    expect(screen.getByTestId('guide-code')).toHaveTextContent('task_id');
    expect(screen.getByTestId('guide-code')).toHaveTextContent('https://ztapi.vip/v1/video/generations/${TASK_ID}');
    expect(screen.getByTestId('guide-code')).not.toHaveTextContent(/yunxin|provider|credential/i);
  });

  it('ignores stale clipboard completion after switching the endpoint', async () => {
    const pending = deferred<void>();
    setClipboard(vi.fn().mockReturnValue(pending.promise));
    renderGuide();
    fireEvent.click(screen.getByRole('button', { name: '复制代码' }));
    fireEvent.change(screen.getByLabelText('调用接口'), { target: { value: 'responses' } });
    await act(async () => { pending.resolve(); await pending.promise; });
    expect(screen.getByRole('status')).toBeEmptyDOMElement();
  });

  it('ignores a stale copy result after switching examples', async () => {
    const pendingCopy = deferred<void>();
    const writeText = vi.fn().mockReturnValue(pendingCopy.promise);
    setClipboard(writeText);
    renderGuide();

    fireEvent.click(screen.getByRole('button', { name: '复制代码' }));
    fireEvent.click(screen.getByRole('tab', { name: 'Python' }));

    await act(async () => {
      pendingCopy.resolve();
      await pendingCopy.promise;
    });

    expect(screen.getByRole('status')).toBeEmptyDOMElement();
  });
});
