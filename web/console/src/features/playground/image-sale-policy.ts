import type { ImageSale } from '../../api/contracts';

export function approvedImageRequests(sale: ImageSale, editing: boolean) {
  return sale.allowed_requests?.filter((r) => r.image_operation === (editing ? 'edit' : 'generation')) ?? [];
}

// Local heuristic, not an upstream tokenizer.
export function estimateImagePromptTokens(prompt: string) {
  return Math.ceil([...prompt].reduce((sum, char) => sum + (char.charCodeAt(0) <= 127 ? 0.25 : 1), 0));
}

export function validatePartialImageRequest(sale: ImageSale, input: {
  size: string; quality: string; count: number; prompt: string; references: number[];
}) {
  if (sale.version !== 2) return '';
  if (input.count !== 1) return '当前仅开放每次 1 张图片。';
  if (estimateImagePromptTokens(input.prompt) > 2000) return '提示词超过 2000 个本地估算 Token（非精确分词计数），未发送。';
  if (input.references.length > 1 || input.references.some((pixels) => !Number.isSafeInteger(pixels) || pixels <= 0 || pixels > 4194304)) return '编辑仅支持 1 张参考图，最多 4194304 像素，未发送。';
  if (!approvedImageRequests(sale, input.references.length > 0).some((r) => r.size === input.size && r.quality === input.quality)) return '当前生成或编辑规格尚未开放，未发送。';
  return '';
}

export async function referenceImagePixels(file: File): Promise<number> {
  const bitmap = await createImageBitmap(file);
  try { return bitmap.width * bitmap.height; } finally { bitmap.close(); }
}
