import {
  createAdminFilenameConfig,
  createBuildTarget,
  selectApplication,
} from './applicationSelection.js';

it('selects the ZTAPI admin application in admin builds', () => {
  expect(selectApplication({ VITE_ZTAPI_ADMIN_APP: 'true' })).toBe('admin');
});

it('keeps the legacy application as the default build target', () => {
  expect(selectApplication({})).toBe('legacy');
});

it('uses an isolated entry and cleans stale output for admin builds', () => {
  expect(createBuildTarget({ VITE_ZTAPI_ADMIN_APP: 'true' })).toEqual({
    application: 'admin',
    entry: './src/ztapi/admin-entry.jsx',
    cleanDistPath: true,
  });
});

it('includes the release identifier in production administration assets', () => {
  expect(
    createAdminFilenameConfig(
      {
        VITE_ZTAPI_ADMIN_APP: 'true',
        VITE_REACT_APP_VERSION: '617040c79540b2d7022bd78001e6a65e7a9d156b',
      },
      true,
    ),
  ).toEqual({
    js: '[name].617040c79540.[contenthash:10].js',
    css: '[name].617040c79540.[contenthash:10].css',
  });
});
