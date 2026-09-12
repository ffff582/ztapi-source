import { useEffect, useRef, useState, type KeyboardEvent } from 'react';
import { getCodeExample, MODEL_FAMILIES, type ModelFamily } from './homeContent';
import { useLocale } from '../../i18n/locale';

type IntegrationWorkbenchProps = {
  selectedFamily: ModelFamily;
  onSelectFamily: (family: ModelFamily) => void;
};

type CopyStatus = 'idle' | 'success' | 'error';

export function IntegrationWorkbench({
  selectedFamily,
  onSelectFamily,
}: IntegrationWorkbenchProps) {
  const { t } = useLocale();
  const [copyStatus, setCopyStatus] = useState<CopyStatus>('idle');
  const copyLabel = copyStatus === 'idle'
    ? t('复制代码')
    : copyStatus === 'success'
      ? t('已复制')
      : t('复制失败，请手动复制');
  const example = getCodeExample(selectedFamily);
  const tabRefs = useRef<Array<HTMLButtonElement | null>>([]);

  useEffect(() => {
    setCopyStatus('idle');
  }, [selectedFamily]);

  const handleTabKeyDown = (
    event: KeyboardEvent<HTMLButtonElement>,
    currentIndex: number,
  ) => {
    let nextIndex: number;

    switch (event.key) {
      case 'ArrowRight':
        nextIndex = (currentIndex + 1) % MODEL_FAMILIES.length;
        break;
      case 'ArrowLeft':
        nextIndex = (currentIndex - 1 + MODEL_FAMILIES.length) % MODEL_FAMILIES.length;
        break;
      case 'Home':
        nextIndex = 0;
        break;
      case 'End':
        nextIndex = MODEL_FAMILIES.length - 1;
        break;
      default:
        return;
    }

    event.preventDefault();
    onSelectFamily(MODEL_FAMILIES[nextIndex].id);
    tabRefs.current[nextIndex]?.focus();
  };

  const handleCopy = async () => {
    setCopyStatus('idle');
    const clipboard = navigator.clipboard;

    if (!clipboard?.writeText) {
      setCopyStatus('error');
      return;
    }

    try {
      await clipboard.writeText(example);
      setCopyStatus('success');
    } catch {
      setCopyStatus('error');
    }
  };

  return (
    <section id="quickstart" className="integration-band" aria-label={t('快速接入')}>
      <div className="public-shell integration-band__inner">
        <div className="integration-band__heading">
          <div>
            <p className="section-kicker">{t('3 分钟接入')}</p>
            <h2>{t('只需替换 Base URL')}</h2>
          </div>
          <p>{t('保留熟悉的 SDK 和调用方式，使用 ZTAPI Key 即可请求公开模型。')}</p>
        </div>
        <div className="integration-workbench">
          <div className="integration-workbench__models">
            <div role="tablist" aria-label={t('模型系列')}>
              {MODEL_FAMILIES.map((family, index) => (
                <button
                  ref={(node) => {
                    tabRefs.current[index] = node;
                  }}
                  id={`family-tab-${family.id}`}
                  key={family.id}
                  type="button"
                  role="tab"
                  aria-controls="integration-code-panel"
                  aria-selected={selectedFamily === family.id}
                  tabIndex={selectedFamily === family.id ? 0 : -1}
                  onClick={() => onSelectFamily(family.id)}
                  onKeyDown={(event) => handleTabKeyDown(event, index)}
                >
                  <strong>{family.label}</strong>
                  <span>{t(family.description)}</span>
                </button>
              ))}
            </div>
            <ol className="request-lifecycle" aria-label={t('请求流程')}>
              <li>
                <span>01</span>
                <strong>SDK</strong>
                <small>{t('发送兼容请求')}</small>
              </li>
              <li>
                <span>02</span>
                <strong>ZTAPI</strong>
                <small>{t('鉴权并选择路由')}</small>
              </li>
              <li>
                <span>03</span>
                <strong>MODEL</strong>
                <small>{t('返回流式或完整响应')}</small>
              </li>
            </ol>
          </div>
          <div
            id="integration-code-panel"
            className="integration-workbench__code"
            role="tabpanel"
            aria-labelledby={`family-tab-${selectedFamily}`}
          >
            <div className="integration-workbench__toolbar">
              <span>JavaScript</span>
              <button type="button" onClick={handleCopy}>
                {copyLabel}
              </button>
              <span
                className="integration-workbench__copy-announcement"
                role="status"
                aria-live="polite"
              >
                {copyStatus === 'idle' ? '' : copyLabel}
              </span>
            </div>
            <pre>
              <code>{example}</code>
            </pre>
          </div>
        </div>
      </div>
    </section>
  );
}
