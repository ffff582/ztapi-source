import { fileURLToPath } from 'node:url';

const deps = fileURLToPath(new URL('../../../../../../node_modules/.pnpm/node_modules/', import.meta.url));
export default {
  css: { postcss: { plugins: [] } },
  resolve: {
    alias: Object.fromEntries(['react', 'react-dom', 'react-router-dom', 'lucide-react', 'vitest', '@testing-library/react', '@testing-library/jest-dom'].map((name) => [name, `${deps}/${name}`])),
  },
  test: {
    environment: 'jsdom',
    globals: true,
    include: ['src/ztapi/**/*.test.{js,jsx}'],
    setupFiles: ['./src/ztapi/test/setup.js'],
    cache: false,
    maxWorkers: 2,
  },
};
