import { Image, X } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';
import { supportApi } from '../../api/support';
import { useLocale } from '../../i18n/locale';
import { supportImageDataUrl } from './image';

export function SupportAttachment({ attachmentId }: { attachmentId: number }) {
  const { t } = useLocale();
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
      const value = await supportApi.attachment(attachmentId);
      const dataUrl = supportImageDataUrl(value);
      if (mounted.current) setSource(dataUrl);
    } catch {
      if (mounted.current)
        setError('截图读取失败，请确认登录和访问权限后重试。');
    } finally {
      if (mounted.current) setLoading(false);
    }
  }

  return (
    <div className="support-attachment">
      <button
        className="console-button console-button--secondary"
        type="button"
        disabled={loading}
        onClick={() => {
          if (source) setSource('');
          else void open();
        }}
      >
        {source ? <X size={15} /> : <Image size={15} />}
        {t(source ? '关闭截图' : loading ? '正在读取截图...' : '查看截图')}
      </button>
      {error && <p role="alert">{t(error)}</p>}
      {source && <img src={source} alt={t('会话截图')} />}
    </div>
  );
}
