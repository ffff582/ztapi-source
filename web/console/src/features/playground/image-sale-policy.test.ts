import { expect, it } from 'vitest';
import { partialImageSale } from '../models/image-sale.fixture';
import { approvedImageRequests, validatePartialImageRequest, estimateImagePromptTokens } from './image-sale-policy';

it('keeps 2K generation separate from editing', () => {
  expect(approvedImageRequests(partialImageSale, true).map((r) => r.size)).toEqual(['1024x1024']);
  expect(approvedImageRequests(partialImageSale, false)).toHaveLength(2);
});
it('closes unsupported count, prompt, reference area and operation', () => {
  const input = { size: '1024x1024', quality: 'low', count: 1, prompt: 'draw', references: [] as number[] };
  expect(validatePartialImageRequest(partialImageSale, input)).toBe('');
  for (const patch of [{ count: 2 }, { prompt: '中'.repeat(2001) }, { references: [4194305] },
    { references: [1, 1] }, { references: [1], size: '1152x2048' }, { quality: 'high' }]) {
    expect(validatePartialImageRequest(partialImageSale, { ...input, ...patch })).not.toBe('');
  }
  expect(validatePartialImageRequest(partialImageSale, { ...input, references: [4194304] })).toBe('');
  expect(estimateImagePromptTokens('中'.repeat(2000))).toBe(2000);
});
