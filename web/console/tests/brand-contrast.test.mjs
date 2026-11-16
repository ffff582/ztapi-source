import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { cwd } from 'node:process';
import { expect, test } from 'vitest';

const tokens = readFileSync(resolve(cwd(), 'src/brand/tokens.css'), 'utf8');

// Every theme block declares its own value, in declaration order.
function tokenValues(name) {
  return [...tokens.matchAll(new RegExp(`--${name}:\\s*(#[0-9a-f]{6})`, 'gi'))].map((match) => match[1]);
}

function luminance(hex) {
  const channels = hex.match(/[0-9a-f]{2}/gi).map((value) => Number.parseInt(value, 16) / 255);
  const [red, green, blue] = channels.map((value) => (
    value <= 0.03928 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4
  ));
  return 0.2126 * red + 0.7152 * green + 0.0722 * blue;
}

function contrast(foreground, background) {
  const foregroundLuminance = luminance(foreground);
  const backgroundLuminance = luminance(background);
  return (Math.max(foregroundLuminance, backgroundLuminance) + 0.05)
    / (Math.min(foregroundLuminance, backgroundLuminance) + 0.05);
}

test('primary action text meets WCAG AA contrast', () => {
  const ink = tokenValues('action-ink');
  // Dark and light themes each define a resting and a hover action colour.
  for (const name of ['action', 'action-hover']) {
    const backgrounds = tokenValues(name);
    expect(backgrounds).toHaveLength(2);
    expect(ink).toHaveLength(backgrounds.length);
    backgrounds.forEach((background, index) => {
      expect(contrast(ink[index], background)).toBeGreaterThanOrEqual(4.5);
    });
  }
});
