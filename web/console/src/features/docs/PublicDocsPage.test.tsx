import { readFileSync, existsSync } from 'node:fs';
import { resolve } from 'node:path';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { LocaleProvider } from '../../i18n/locale';
import { PublicDocsPage } from './PublicDocsPage';

function renderDocs(section = 'integration') {
  return render(<LocaleProvider><MemoryRouter initialEntries={[`/docs/${section}`]}>
    <Routes><Route path="/docs/:section" element={<PublicDocsPage />} /></Routes>
  </MemoryRouter></LocaleProvider>);
}

afterEach(() => {
  cleanup();
  Reflect.deleteProperty(navigator, 'clipboard');
  vi.restoreAllMocks();
});

describe('public documentation', () => {
  it.each([
    ['api', 'API 手册'], ['integration', '集成指南'], ['user-guide', '用户指南'], ['faq', '常见问题'],
  ])('keeps section navigation and model links public on %s', (section, title) => {
    renderDocs(section);
    expect(screen.getByRole('heading', { level: 1, name: title })).toBeVisible();
    const nav = screen.getByRole('navigation', { name: '文档分类' });
    expect(within(nav).getAllByRole('link')).toHaveLength(4);
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
    for (const path of ['api', 'integration', 'user-guide', 'faq']) {
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
  });
});
