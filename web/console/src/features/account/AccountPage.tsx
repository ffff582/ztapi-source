import { LoaderCircle, MailCheck, ShieldCheck } from 'lucide-react';
import { useEffect, useState, type FormEvent } from 'react';
import { apiClient } from '../../api/client';
import { useLocale } from '../../i18n/locale';

type AccountProfile = {
  email: string;
  pendingEmail: string;
  emailVerified: boolean;
};

function parseAccountProfile(value: unknown): AccountProfile {
  const candidate = typeof value === 'object' && value !== null
    ? value as Record<string, unknown>
    : {};
  const email = typeof candidate.email === 'string' ? candidate.email : '';
  const pendingEmail = typeof candidate.pending_email === 'string'
    ? candidate.pending_email
    : '';

  return {
    email,
    pendingEmail,
    emailVerified: candidate.email_verified === true && email !== '',
  };
}

const emailPattern = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

export function AccountPage() {
  const { t } = useLocale();
  const [profile, setProfile] = useState<AccountProfile | null>(null);
  const [email, setEmail] = useState('');
  const [verificationCode, setVerificationCode] = useState('');
  const [codeRequested, setCodeRequested] = useState(false);
  const [pendingAction, setPendingAction] = useState<'send' | 'bind' | null>(null);
  const [error, setError] = useState('');

  useEffect(() => {
    let active = true;
    void apiClient.get<unknown>('/user/self')
      .then(parseAccountProfile)
      .then((nextProfile) => {
        if (!active) return;
        setProfile(nextProfile);
        setEmail(nextProfile.pendingEmail || nextProfile.email);
      })
      .catch(() => {
        if (active) setError(t('账号信息加载失败，请刷新后重试。'));
      });

    return () => {
      active = false;
    };
  }, [t]);

  function normalizedEmail() {
    return email.trim().toLowerCase();
  }

  async function requestCode() {
    const normalized = normalizedEmail();
    if (!emailPattern.test(normalized)) {
      setError(t('请输入有效的邮箱地址'));
      return;
    }
    setPendingAction('send');
    setError('');
    try {
      await apiClient.post('/user/self/email/verification', { email: normalized });
      setEmail(normalized);
      setVerificationCode('');
      setCodeRequested(true);
    } catch {
      setError(t('验证码发送失败，请确认邮箱未被其他账号使用后重试。'));
    } finally {
      setPendingAction(null);
    }
  }

  async function bindEmail(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const normalized = normalizedEmail();
    const code = verificationCode.trim();
    if (!emailPattern.test(normalized) || code === '') {
      setError(t('请填写有效邮箱和邮箱验证码'));
      return;
    }
    setPendingAction('bind');
    setError('');
    try {
      await apiClient.put<unknown, { email: string; verification_code: string }>(
        '/user/self/email',
        { email: normalized, verification_code: code },
      );
      setProfile({ email: normalized, pendingEmail: '', emailVerified: true });
      setCodeRequested(false);
      setVerificationCode('');
    } catch {
      setError(t('邮箱绑定失败，请检查验证码是否正确或重新发送。'));
    } finally {
      setPendingAction(null);
    }
  }

  return (
    <div className="console-page">
      <header className="console-page__header">
        <div>
          <p className="console-eyebrow">{t('账户安全')}</p>
          <h1>{t('账号设置')}</h1>
        </div>
        <p>{t('补充并验证邮箱后，可通过邮件找回密码。未验证邮箱不能用于重置密码。')}</p>
      </header>

      <section className="console-panel" aria-labelledby="account-email-heading">
        <div className="console-panel__heading">
          <MailCheck aria-hidden="true" />
          <h2 id="account-email-heading">{t('登录邮箱')}</h2>
        </div>

        {profile === null && error === '' ? (
          <div className="console-state">
            <LoaderCircle aria-hidden="true" />
            <span>{t('正在加载账号信息...')}</span>
          </div>
        ) : (
          <form className="key-form" onSubmit={bindEmail} noValidate>
            {profile?.emailVerified === true && (
              <div className="console-status console-status--success key-form__wide">
                <ShieldCheck size={15} aria-hidden="true" />
                {t('邮箱已验证，可用于找回密码。')}
              </div>
            )}

            <div className="console-field key-form__wide">
              <label htmlFor="account-email">{t('邮箱地址')}</label>
              <input
                id="account-email"
                type="email"
                autoComplete="email"
                value={email}
                onChange={(event) => {
                  setEmail(event.target.value);
                  setCodeRequested(false);
                  setVerificationCode('');
                  setError('');
                }}
              />
              <p className="console-field__help">
                {profile?.emailVerified === true
                  ? t('更换邮箱时，原邮箱会保留到新邮箱验证成功。')
                  : t('验证码发送成功后，再输入验证码完成绑定。')}
              </p>
            </div>

            {codeRequested && (
              <div className="console-field key-form__wide">
                <label htmlFor="account-email-code">{t('邮箱验证码')}</label>
                <input
                  id="account-email-code"
                  autoComplete="one-time-code"
                  value={verificationCode}
                  onChange={(event) => {
                    setVerificationCode(event.target.value);
                    setError('');
                  }}
                />
              </div>
            )}

            {error !== '' && (
              <div className="console-alert key-form__wide" role="alert">{error}</div>
            )}

            <div className="console-actions key-form__wide">
              <button
                className="console-button console-button--secondary"
                type="button"
                disabled={pendingAction !== null}
                onClick={() => void requestCode()}
              >
                {pendingAction === 'send' && <LoaderCircle size={16} aria-hidden="true" />}
                {codeRequested ? t('重新发送') : t('发送验证码')}
              </button>
              {codeRequested && (
                <button
                  className="console-button console-button--primary"
                  type="submit"
                  disabled={pendingAction !== null}
                >
                  {pendingAction === 'bind' && <LoaderCircle size={16} aria-hidden="true" />}
                  {t('确认绑定')}
                </button>
              )}
            </div>
          </form>
        )}
      </section>
    </div>
  );
}
