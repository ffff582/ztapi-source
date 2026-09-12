import { Check, Copy, ShieldAlert } from 'lucide-react';
import { useLayoutEffect, useRef, useState, type KeyboardEvent } from 'react';
import { createPortal } from 'react-dom';
import { useLocale } from '../../i18n/locale';

interface KeyCreatedDialogProps {
  plaintextKey: string;
  onAcknowledge: () => void;
}

export function KeyCreatedDialog({
  plaintextKey,
  onAcknowledge,
}: KeyCreatedDialogProps) {
  const { t } = useLocale();
  const dialogRef = useRef<HTMLDialogElement>(null);
  const copyRef = useRef<HTMLButtonElement>(null);
  const acknowledgeRef = useRef<HTMLButtonElement>(null);
  const [copied, setCopied] = useState(false);

  useLayoutEffect(() => {
    const dialog = dialogRef.current;
    if (dialog === null) {
      return;
    }

    if (typeof dialog.showModal === 'function') {
      dialog.showModal();
    } else {
      dialog.setAttribute('open', '');
    }
    acknowledgeRef.current?.focus();

    const backgroundElements = Array.from(document.body.children).filter(
      (element) => element !== dialog,
    );
    const previousInert = backgroundElements.map((element) =>
      element.hasAttribute('inert'),
    );
    backgroundElements.forEach((element) => element.setAttribute('inert', ''));

    const preventUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = '';
    };
    window.addEventListener('beforeunload', preventUnload);

    return () => {
      window.removeEventListener('beforeunload', preventUnload);
      backgroundElements.forEach((element, index) => {
        if (!previousInert[index]) {
          element.removeAttribute('inert');
        }
      });
      if (dialog.open && typeof dialog.close === 'function') {
        dialog.close();
      }
    };
  }, []);

  function keepFocusInside(event: KeyboardEvent<HTMLDialogElement>) {
    if (event.key === 'Escape') {
      event.preventDefault();
      return;
    }
    if (event.key !== 'Tab') {
      return;
    }

    if (event.shiftKey && document.activeElement === copyRef.current) {
      event.preventDefault();
      acknowledgeRef.current?.focus();
    } else if (
      !event.shiftKey &&
      document.activeElement === acknowledgeRef.current
    ) {
      event.preventDefault();
      copyRef.current?.focus();
    }
  }

  async function copyKey() {
    try {
      await navigator.clipboard.writeText(plaintextKey);
      setCopied(true);
    } catch {
      setCopied(false);
    }
  }

  return createPortal(
    <dialog
      aria-describedby="created-key-warning"
      aria-labelledby="created-key-title"
      className="key-dialog"
      ref={dialogRef}
      onCancel={(event) => event.preventDefault()}
      onKeyDown={keepFocusInside}
    >
      <div className="key-dialog__heading">
        <ShieldAlert aria-hidden="true" size={22} />
        <div>
          <p className="console-eyebrow">{t('仅显示一次')}</p>
          <h2 id="created-key-title">{t('保存新的 API Key')}</h2>
        </div>
      </div>
      <p id="created-key-warning" className="key-dialog__warning">
        {t('请立即保存。确认后，ZTAPI 不会再次显示完整 Key。')}
      </p>
      <code className="key-dialog__secret">{plaintextKey}</code>
      <div className="key-dialog__actions">
        <button
          className="console-button console-button--secondary"
          ref={copyRef}
          type="button"
          onClick={copyKey}
        >
          {copied ? (
            <Check aria-hidden="true" size={17} />
          ) : (
            <Copy aria-hidden="true" size={17} />
          )}
          {copied ? t('已复制') : t('复制 Key')}
        </button>
        <button
          className="console-button console-button--primary"
          ref={acknowledgeRef}
          type="button"
          onClick={onAcknowledge}
        >
          {t('我已安全保存')}
        </button>
      </div>
    </dialog>,
    document.body,
  );
}
