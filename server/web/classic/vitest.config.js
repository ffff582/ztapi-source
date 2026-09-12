import { defineConfig } from 'vitest/config';

export default defineConfig({
  test: {
    environment: 'jsdom',
    globals: true,
    include: [
      'src/ztapi/**/*.test.{js,jsx,ts,tsx}',
      'src/components/topup/**/*.test.{js,jsx,ts,tsx}',
      'src/components/auth/*.ztapi.test.{js,jsx,ts,tsx}',
    ],
    setupFiles: ['./src/ztapi/test/setup.js'],
  },
});
