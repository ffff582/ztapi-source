import path from 'path';
import { createRequire } from 'module';
import { fileURLToPath } from 'url';
import { defineConfig, loadEnv } from '@rsbuild/core';
import { pluginReact } from '@rsbuild/plugin-react';
import {
  createAdminFilenameConfig,
  createBuildTarget,
} from './src/ztapi/applicationSelection.js';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const require = createRequire(import.meta.url);
const semiUiDir = path.resolve(
  path.dirname(require.resolve('@douyinfe/semi-ui')),
  '../..',
);

export default defineConfig(({ envMode }) => {
  const env = loadEnv({ mode: envMode, prefixes: ['VITE_'] });
  const clientServerUrl =
    process.env.VITE_REACT_APP_SERVER_URL ||
    env.rawPublicVars.VITE_REACT_APP_SERVER_URL ||
    '';
  const ztapiAdminApp =
    process.env.VITE_ZTAPI_ADMIN_APP ||
    env.rawPublicVars.VITE_ZTAPI_ADMIN_APP ||
    '';
  const releaseVersion =
    process.env.VITE_REACT_APP_VERSION ||
    env.rawPublicVars.VITE_REACT_APP_VERSION ||
    '';
  const proxyServerUrl = clientServerUrl || 'http://localhost:3000';
  const buildTarget = createBuildTarget({
    VITE_ZTAPI_ADMIN_APP: ztapiAdminApp,
  });
  const isProd = envMode === 'production';
  const devProxy = Object.fromEntries(
    (['/api', '/mj', '/pg'] as const).map((key) => [
      key,
      { target: proxyServerUrl, changeOrigin: true },
    ]),
  ) as Record<string, { target: string; changeOrigin: boolean }>;

  return {
    plugins: [pluginReact()],
    source: {
      entry: {
        index: buildTarget.entry,
      },
      define: {
        'import.meta.env.VITE_REACT_APP_SERVER_URL':
          JSON.stringify(clientServerUrl),
        'import.meta.env.VITE_ZTAPI_ADMIN_APP': JSON.stringify(ztapiAdminApp),
      },
    },
    resolve: {
      alias: {
        '@': path.resolve(__dirname, './src'),
        '@douyinfe/semi-ui/dist/css/semi.css': path.resolve(
          semiUiDir,
          'dist/css/semi.css',
        ),
      },
    },
    html: {
      template: './index.html',
    },
    server: {
      host: '0.0.0.0',
      strictPort: true,
      proxy: devProxy,
    },
    output: {
      minify: isProd,
      target: 'web',
      cleanDistPath: buildTarget.cleanDistPath,
      filename: createAdminFilenameConfig(
        {
          VITE_ZTAPI_ADMIN_APP: ztapiAdminApp,
          VITE_REACT_APP_VERSION: releaseVersion,
        },
        isProd,
      ),
      distPath: {
        root: 'dist',
      },
    },
    performance: {
      removeConsole: isProd ? ['log'] : false,
      buildCache: {
        cacheDigest: [releaseVersion],
      },
    },
    tools: {
      rspack: {
        module: {
          rules: [
            {
              test: /src[\\/].*\.js$/,
              type: 'javascript/auto',
              use: [
                {
                  loader: 'builtin:swc-loader',
                  options: {
                    jsc: {
                      parser: {
                        syntax: 'ecmascript',
                        jsx: true,
                      },
                      transform: {
                        react: {
                          runtime: 'automatic',
                          development: !isProd,
                          refresh: !isProd,
                        },
                      },
                    },
                  },
                },
              ],
            },
          ],
        },
      },
    },
  };
});
