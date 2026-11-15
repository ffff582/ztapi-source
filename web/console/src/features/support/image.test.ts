import { describe, expect, it } from 'vitest';
import { readSupportImage, supportImageDataUrl } from './image';

describe('private support screenshot validation', () => {
  it.each([
    ['image/png', [137, 80, 78, 71, 13, 10, 26, 10]],
    ['image/jpeg', [255, 216, 255]],
    ['image/webp', [82, 73, 70, 70, 0, 0, 0, 0, 87, 69, 66, 80]],
  ])(
    'accepts only the matching binary signature for %s',
    async (mime, bytes) => {
      const image = await readSupportImage(
        [
          new File([new Uint8Array(bytes as number[])], 'screenshot', {
            type: mime as string,
          }),
        ],
        3 * 1024 * 1024,
      );
      expect(supportImageDataUrl(image)).toBe(
        `data:${mime};base64,${image.data_base64}`,
      );
    },
  );

  it.each([
    { mime_type: 'image/svg+xml', data_base64: 'PHN2Zy8+' },
    { mime_type: 'text/html', data_base64: 'PHNjcmlwdD4=' },
    { mime_type: 'image/png', data_base64: 'not base64!' },
    { mime_type: 'image/png', data_base64: '' },
    { mime_type: 'image/png', data_base64: 'eA==' },
    { mime_type: 'image/png', data_base64: 'A'.repeat(4 * 1024 * 1024 + 4) },
  ])('rejects unsafe or oversized private attachment data', (image) => {
    expect(() => supportImageDataUrl(image)).toThrow();
  });

  it('honors a smaller server image limit', async () => {
    await expect(
      readSupportImage(
        [new File([new Uint8Array(10)], 'a.png', { type: 'image/png' })],
        9,
      ),
    ).rejects.toThrow('3MB');
  });
});
