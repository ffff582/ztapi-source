import '@testing-library/jest-dom/vitest';
import { cleanup } from '@testing-library/react';
import { afterEach, beforeEach, vi } from 'vitest';

beforeEach(() => {
  vi.spyOn(window.navigator, 'language', 'get').mockReturnValue('zh-CN');
});

afterEach(() => {
  cleanup();
  localStorage.clear();
  document.documentElement.lang = '';
  vi.restoreAllMocks();
});
