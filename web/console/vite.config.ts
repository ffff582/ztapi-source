import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';
import { loadEnv } from 'vite';

export default defineConfig(({ mode }) => {
  const environment = loadEnv(mode, process.cwd(), '');
  const apiOrigin =
    environment.ZTAPI_API_ORIGIN || 'http://localhost:3000';
  const proxy = Object.fromEntries(
    ['/api', '/pg', '/v1', '/v1beta'].map((route) => [
      route,
      {
        target: apiOrigin,
        changeOrigin: false,
      },
    ]),
  );

  return {
    plugins: [react()],
    server: { proxy },
    test: {
      environment: 'jsdom',
      globals: true,
      setupFiles: './src/test/setup.ts',
    },
  };
});
