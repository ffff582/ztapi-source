import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { cwd } from 'node:process';
import { expect, test } from 'vitest';

const publicHeaderCss = readFileSync(
  resolve(cwd(), 'src/components/layout/public-header.css'),
  'utf8',
);

test('uses a responsive, toggleable mobile navigation layout with visible focus', () => {
  expect(publicHeaderCss).toMatch(/@media \(max-width: 640px\)/);
  expect(publicHeaderCss).toMatch(
    /\.public-header__menu\s*{[^}]*display: inline-grid;/s,
  );
  expect(publicHeaderCss).toMatch(
    /\.public-header__nav\[data-open="true"\]\s*{[^}]*display: grid;/s,
  );
  expect(publicHeaderCss).toMatch(
    /\.public-header__menu:focus-visible\s*{[^}]*outline: 2px solid var\(--state-cyan\);/s,
  );
});
