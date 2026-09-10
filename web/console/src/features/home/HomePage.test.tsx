import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import postcss, { type AtRule, type Declaration, type Root, type Rule } from 'postcss';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { HomePage } from './HomePage';

declare const process: { cwd: () => string };

const homeCss = readFileSync(resolve(process.cwd(), 'src/features/home/home.css'), 'utf8');
const homeStyles = postcss.parse(homeCss);

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function defaultFetch(input: RequestInfo | URL) {
  const url = input.toString();
  if (url.endsWith('/api/status')) {
    return Promise.resolve(jsonResponse({ success: true, data: { quota_per_unit: 250_000 } }));
  }
  if (url.endsWith('/api/pricing')) {
    return Promise.resolve(jsonResponse({
      success: true,
      data: [],
      group_ratio: { default: 1 },
      usable_group: { default: '默认分组' },
      pricing_version: 'homepage-shell-v1',
    }));
  }
  return Promise.resolve(jsonResponse({ success: false }, 500));
}

function renderHomePage(initialEntry = '/') {
  vi.stubGlobal('fetch', vi.fn(defaultFetch));
  return render(
    <MemoryRouter initialEntries={[initialEntry]}>
      <HomePage />
    </MemoryRouter>,
  );
}

function setClipboard(clipboard: Pick<Clipboard, 'writeText'> | undefined) {
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: clipboard,
  });
}

function mediaRule(params: string): AtRule {
  const matches = (homeStyles.nodes ?? []).filter(
    (node): node is AtRule => node.type === 'atrule' && node.name === 'media' && node.params === params,
  );
  expect(matches).toHaveLength(1);
  return matches[0];
}

function lastDeclaration(root: Root | AtRule, selector: string, property: string) {
  const rules = (root.nodes ?? []).filter(
    (node): node is Rule => node.type === 'rule' && node.selectors.includes(selector),
  );
  const values = rules.flatMap((rule) => (rule.nodes ?? [])
    .filter((node): node is Declaration => node.type === 'decl' && node.prop === property)
    .map((node) => node.value));
  return values.at(-1);
}

afterEach(() => {
  cleanup();
  Reflect.deleteProperty(navigator, 'clipboard');
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  Object.defineProperty(window, 'scrollY', { configurable: true, value: 0 });
});

describe('ZTAPI public homepage', () => {
  it('makes the brand the hero and uses the project visual without canvas', async () => {
    renderHomePage();

    const hero = screen.getByRole('region', { name: 'ZTAPI' });
    expect(within(hero).getByRole('heading', { level: 1, name: 'ZTAPI' })).toBeVisible();
    expect(within(hero).getByText('一个 Key，连接全球主流 AI 模型')).toBeVisible();
    expect(hero.querySelector('canvas')).toBeNull();
    expect(hero.querySelector('[data-testid="hero-scene"]')).toBeNull();
    const visual = hero.querySelector<HTMLImageElement>('.gateway-hero__visual img');
    expect(visual).toHaveAttribute('src', '/brand/ztapi-hero-infrastructure.webp');
    expect(within(hero).getByRole('link', { name: '开始使用' })).toHaveAttribute('href', '/register');
    expect(within(hero).getByRole('link', { name: '查看模型价格' })).toHaveAttribute('href', '/models');
    expect(within(hero).getByText('https://ztapi.vip/v1')).toBeVisible();
  });

  it('keeps one main landmark and stable public section targets', async () => {
    renderHomePage();

    expect(await screen.findAllByRole('main')).toHaveLength(1);
    expect(screen.getByRole('region', { name: '网关能力' })).toHaveAttribute('id', 'capabilities');
    expect(screen.getByRole('region', { name: '快速接入' })).toHaveAttribute('id', 'quickstart');
    expect(
      screen.getByRole('heading', {
        name: '文本、图片与视频，通过一个账户统一调用',
      }),
    ).toBeVisible();
    expect(document.querySelectorAll('.card .card')).toHaveLength(0);
  });

  it('preserves the required upstream attribution', async () => {
    renderHomePage();
    const attribution = await screen.findByText(
      'Frontend design and development by New API contributors.',
    );
    expect(attribution).toHaveAttribute('href', 'https://github.com/QuantumNous/new-api');
    expect(attribution).toHaveAttribute('target', '_blank');
    expect(attribution).toHaveAttribute('rel', 'noreferrer');
  });

  it('links network users to the corresponding ZTAPI source', () => {
    renderHomePage();

    expect(screen.getByRole('link', { name: 'ZTAPI 对应源码' })).toHaveAttribute(
      'href',
      '/.well-known/source',
    );
  });

  it('links every public navigation item to a usable destination', async () => {
    renderHomePage();
    const navigation = await screen.findByRole('navigation', { name: '公共导航' });
    const expected = [
      ['模型价格', '/models'],
      ['快速接入', '/#quickstart'],
      ['网关能力', '/#capabilities'],
      ['登录', '/login'],
      ['开始使用', '/register'],
    ];
    for (const [name, href] of expected) {
      expect(within(navigation).getByRole('link', { name })).toHaveAttribute('href', href);
    }
  });

  it('opens the mobile menu and restores focus when Escape closes it', async () => {
    renderHomePage();
    const menu = await screen.findByRole('button', { name: '打开导航菜单' });
    const navigation = screen.getByRole('navigation', { name: '公共导航' });
    menu.focus();
    fireEvent.click(menu);
    expect(navigation).toHaveAttribute('data-open', 'true');
    fireEvent.keyDown(window, { key: 'Escape' });
    expect(navigation).toHaveAttribute('data-open', 'false');
    expect(menu).toHaveFocus();
  });

  it('marks the sticky public header after scrolling', async () => {
    renderHomePage();
    const header = await screen.findByRole('banner');
    expect(header).toHaveAttribute('data-scrolled', 'false');
    Object.defineProperty(window, 'scrollY', { configurable: true, value: 20 });
    fireEvent.scroll(window);
    expect(header).toHaveAttribute('data-scrolled', 'true');
  });

  it('keeps model tabs and code copy keyboard operable', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    setClipboard({ writeText });
    renderHomePage();

    const tabs = screen.getAllByRole('tab');
    tabs[0].focus();
    fireEvent.keyDown(tabs[0], { key: 'End' });
    expect(tabs[2]).toHaveFocus();
    expect(tabs[2]).toHaveAttribute('aria-selected', 'true');

    fireEvent.click(screen.getByRole('button', { name: '复制代码' }));
    await waitFor(() => expect(writeText).toHaveBeenCalledTimes(1));
    expect(screen.getByRole('status')).toHaveTextContent('已复制');
  });

  it('reports copy failure without blocking manual copying', async () => {
    setClipboard(undefined);
    renderHomePage();
    fireEvent.click(screen.getByRole('button', { name: '复制代码' }));
    expect(await screen.findByRole('status')).toHaveTextContent('复制失败，请手动复制');
  });

  it('has explicit mobile and reduced-motion contracts', () => {
    const mobile = mediaRule('(max-width: 760px)');
    const narrow = mediaRule('(max-width: 420px)');
    const reduced = mediaRule('(prefers-reduced-motion: reduce)');
    expect(lastDeclaration(mobile, '.gateway-hero', 'min-height')).toBe('690px');
    expect(lastDeclaration(narrow, '.gateway-hero h1', 'font-size')).toBe('72px');
    expect(lastDeclaration(narrow, '.gateway-hero__actions', 'flex-direction')).toBe('column');
    expect(lastDeclaration(reduced, '.gateway-hero__signal', 'animation')).toBe('none');
  });

  it('does not ship the retired Three.js hero dependency from homepage code', () => {
    const source = readFileSync(resolve(process.cwd(), 'src/features/home/HomePage.tsx'), 'utf8');
    expect(source).not.toContain('HeroScene');
    expect(source).not.toContain("from 'three'");
    expect(homeCss).not.toContain('.hero-scene');
  });
});
