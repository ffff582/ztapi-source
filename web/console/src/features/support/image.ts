import type { SupportImage } from '../../api/support';

export function supportImageDataUrl(image: SupportImage): string {
  if (
    !image ||
    !['image/png', 'image/jpeg', 'image/webp'].includes(image.mime_type) ||
    typeof image.data_base64 !== 'string' ||
    image.data_base64.length > 4 * 1024 * 1024 ||
    !/^[A-Za-z0-9+/]+={0,2}$/.test(image.data_base64)
  )
    throw new Error('截图格式无效');
  const bytes = atob(image.data_base64);
  if (!bytes.length || bytes.length > 3 * 1024 * 1024)
    throw new Error('截图大小无效');
  const valid =
    image.mime_type === 'image/png'
      ? bytes.startsWith('\x89PNG\r\n\x1a\n')
      : image.mime_type === 'image/jpeg'
        ? bytes.startsWith('\xff\xd8\xff')
        : bytes.startsWith('RIFF') && bytes.slice(8, 12) === 'WEBP';
  if (!valid) throw new Error('截图内容无效');
  return `data:${image.mime_type};base64,${image.data_base64}`;
}

export async function readSupportImage(
  files: FileList | File[],
  maxBytes: number,
): Promise<SupportImage> {
  if (files.length !== 1) throw new Error('最多 1 张截图');
  const file = files[0];
  if (!['image/png', 'image/jpeg', 'image/webp'].includes(file.type))
    throw new Error('仅支持 PNG、JPEG 或 WebP');
  if (file.size === 0 || file.size > Math.min(3 * 1024 * 1024, maxBytes))
    throw new Error('截图大小不能超过 3MB 或服务端限制');
  const dataUrl = await new Promise<string>((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result));
    reader.onerror = () => reject(new Error('截图读取失败'));
    reader.readAsDataURL(file);
  });
  const encoded = dataUrl.slice(dataUrl.indexOf(',') + 1);
  const bytes = atob(encoded);
  const valid =
    file.type === 'image/png'
      ? bytes.startsWith('\x89PNG\r\n\x1a\n')
      : file.type === 'image/jpeg'
        ? bytes.startsWith('\xff\xd8\xff')
        : bytes.startsWith('RIFF') && bytes.slice(8, 12) === 'WEBP';
  if (!valid) throw new Error('图片内容与格式不匹配');
  return { mime_type: file.type, data_base64: encoded };
}
