import React, { useEffect, useRef, useState } from 'react';
import { Image, X } from 'lucide-react';
import { supportAdminApi } from './support-api';

function imageSource(image) {
  if (
    !image ||
    !['image/png', 'image/jpeg', 'image/webp'].includes(image.mime_type) ||
    typeof image.data_base64 !== 'string' ||
    image.data_base64.length > 4 * 1024 * 1024 ||
    !/^[A-Za-z0-9+/]+={0,2}$/.test(image.data_base64)
  )
    throw new Error('Invalid screenshot');
  const bytes = atob(image.data_base64);
  if (!bytes.length || bytes.length > 3 * 1024 * 1024)
    throw new Error('Invalid screenshot');
  const valid =
    image.mime_type === 'image/png'
      ? bytes.startsWith('\x89PNG\r\n\x1a\n')
      : image.mime_type === 'image/jpeg'
        ? bytes.startsWith('\xff\xd8\xff')
        : bytes.startsWith('RIFF') && bytes.slice(8, 12) === 'WEBP';
  if (!valid) throw new Error('Invalid screenshot');
  return `data:${image.mime_type};base64,${image.data_base64}`;
}

export default function SupportAttachment({ attachmentId }) {
  const [source, setSource] = useState('');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  async function open() {
    if (loading) return;
    setLoading(true);
    setError('');
    try {
      const value = await supportAdminApi.attachment(attachmentId);
      const dataUrl = imageSource(value);
      if (mounted.current) setSource(dataUrl);
    } catch {
      if (mounted.current)
        setError('截图读取失败，请确认登录和工单访问权限后重试。');
    } finally {
      if (mounted.current) setLoading(false);
    }
  }
  return (
    <div className='ztapi-support-attachment'>
      <button
        type='button'
        disabled={loading}
        onClick={() => {
          if (source) setSource('');
          else void open();
        }}
      >
        {source ? <X size={15} /> : <Image size={15} />}
        {source ? '关闭截图' : loading ? '正在读取截图...' : '查看截图'}
      </button>
      {error && <p role='alert'>{error}</p>}
      {source && <img src={source} alt='工单截图' />}
    </div>
  );
}
