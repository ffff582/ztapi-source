import { useRef, useState, type FormEvent } from 'react';
import { LoaderCircle, Mail } from 'lucide-react';
import { Link } from 'react-router-dom';
import { apiClient, authErrorMessage } from '../../api/client';
import { useLocale } from '../../i18n/locale';
import { AuthShell } from './AuthShell';
import { InlineNotice } from './InlineNotice';

const emailPattern = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

export function ForgotPasswordPage() {
  const { t } = useLocale();
  const emailRef = useRef<HTMLInputElement>(null);
  const [email, setEmail] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [sent, setSent] = useState(false);
  const [pending, setPending] = useState(false);

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) return;

    const normalizedEmail = email.trim().toLowerCase();
    if (!emailPattern.test(normalizedEmail)) {
      setError(t('请输入有效的邮箱地址'));
      emailRef.current?.focus();
      return;
    }

    setError(null);
    setPending(true);
    try {
      await apiClient.post('/auth/password-reset/request', {
        email: normalizedEmail,
      });
      setSent(true);
    } catch (requestError) {
      setError(authErrorMessage(requestError));
    } finally {
      setPending(false);
    }
  }

  return (
    <AuthShell
      eyebrow="Account recovery"
      title={t('找回密码')}
      intro={t('输入注册邮箱，我们会发送一个 10 分钟内有效的重置链接。')}
    >
      {sent ? (
        <div className="auth-result" aria-live="polite">
          <Mail aria-hidden="true" />
          <p>{t('如果该邮箱已绑定账号，重置邮件将在几分钟内送达。')}</p>
        </div>
      ) : (
        <form className="auth-form" onSubmit={handleSubmit} noValidate>
          <InlineNotice message={error} />
          <div className="auth-field">
            <label htmlFor="forgot-email">{t('邮箱')}</label>
            <input
              ref={emailRef}
              id="forgot-email"
              name="email"
              type="email"
              autoFocus
              autoComplete="email"
              value={email}
              onChange={(event) => {
                setEmail(event.target.value);
                setError(null);
              }}
            />
          </div>
          <button className="auth-submit" type="submit" disabled={pending}>
            {pending ? (
              <LoaderCircle className="auth-submit__spinner" aria-hidden="true" />
            ) : (
              <Mail aria-hidden="true" />
            )}
            {pending ? t('发送中...') : t('发送重置邮件')}
          </button>
        </form>
      )}
      <p className="auth-switch">
        <Link to="/login">{t('返回登录')}</Link>
      </p>
    </AuthShell>
  );
}
