import { useState, type FormEvent } from 'react';
import { CheckCircle2, LoaderCircle, LockKeyhole } from 'lucide-react';
import { Link, useSearchParams } from 'react-router-dom';
import { apiClient, authErrorMessage } from '../../api/client';
import { useLocale } from '../../i18n/locale';
import { AuthShell } from './AuthShell';
import { InlineNotice } from './InlineNotice';
import { PasswordField } from './PasswordField';
import { validateRegistrationInput } from './RegisterPage';

export function ResetPasswordPage() {
  const { t } = useLocale();
  const [searchParams] = useSearchParams();
  const email = searchParams.get('email')?.trim().toLowerCase() ?? '';
  const token = searchParams.get('token')?.trim() ?? '';
  const [password, setPassword] = useState('');
  const [confirmation, setConfirmation] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);
  const [complete, setComplete] = useState(false);
  const validLink = email !== '' && token !== '';

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending || !validLink) return;

    const passwordError = validateRegistrationInput({
      username: 'valid-user',
      password,
    }).password;
    if (passwordError !== undefined) {
      setError(t(passwordError));
      return;
    }
    if (password !== confirmation) {
      setError(t('两次输入的密码不一致'));
      return;
    }

    setError(null);
    setPending(true);
    try {
      await apiClient.post('/auth/password-reset/confirm', {
        email,
        token,
        new_password: password,
      });
      setComplete(true);
    } catch (requestError) {
      setError(authErrorMessage(requestError));
    } finally {
      setPending(false);
    }
  }

  return (
    <AuthShell
      eyebrow="Secure reset"
      title={t('设置新密码')}
      intro={t('设置新密码后，其他设备上的登录状态会自动失效。')}
    >
      {complete ? (
        <div className="auth-result" aria-live="polite">
          <CheckCircle2 aria-hidden="true" />
          <p>{t('密码已更新，请重新登录。')}</p>
        </div>
      ) : validLink ? (
        <form className="auth-form" onSubmit={handleSubmit} noValidate>
          <InlineNotice message={error} />
          <PasswordField
            id="reset-password"
            label={t('新密码')}
            name="new-password"
            autoComplete="new-password"
            value={password}
            help={t('至少 10 个字符，最多 256 字节')}
            onChange={(event) => {
              setPassword(event.target.value);
              setError(null);
            }}
          />
          <PasswordField
            id="reset-password-confirmation"
            label={t('确认新密码')}
            name="new-password-confirmation"
            autoComplete="new-password"
            value={confirmation}
            onChange={(event) => {
              setConfirmation(event.target.value);
              setError(null);
            }}
          />
          <button className="auth-submit" type="submit" disabled={pending}>
            {pending ? (
              <LoaderCircle className="auth-submit__spinner" aria-hidden="true" />
            ) : (
              <LockKeyhole aria-hidden="true" />
            )}
            {pending ? t('修改中...') : t('确认修改')}
          </button>
        </form>
      ) : (
        <InlineNotice message={t('重置链接无效或已过期。')} />
      )}
      <p className="auth-switch">
        <Link to="/login">{t('返回登录')}</Link>
      </p>
    </AuthShell>
  );
}
