import { useRef, useState, type FormEvent } from 'react';
import { LoaderCircle, LogIn } from 'lucide-react';
import { Link, useNavigate } from 'react-router-dom';
import { authErrorMessage } from '../../api/client';
import { useAuth } from '../../auth/session';
import { AuthShell } from './AuthShell';
import { InlineNotice } from './InlineNotice';
import { PasswordField } from './PasswordField';
import { validateRegistrationInput } from './RegisterPage';
import { useLocale } from '../../i18n/locale';

export function LoginPage() {
  const { t } = useLocale();
  const navigate = useNavigate();
  const { login } = useAuth();
  const usernameRef = useRef<HTMLInputElement>(null);
  const passwordRef = useRef<HTMLInputElement>(null);
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [fieldErrors, setFieldErrors] = useState<
    ReturnType<typeof validateRegistrationInput>
  >({});
  const [serverError, setServerError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);

  function updateUsername(value: string) {
    setUsername(value);
    setServerError(null);
    setFieldErrors((current) => ({ ...current, username: undefined }));
  }

  function updatePassword(value: string) {
    setPassword(value);
    setServerError(null);
    setFieldErrors((current) => ({ ...current, password: undefined }));
  }

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();

    if (pending) {
      return;
    }

    const errors = validateRegistrationInput({ username, password });
    setFieldErrors(errors);
    setServerError(null);

    if (errors.username !== undefined) {
      usernameRef.current?.focus();
      return;
    }

    if (errors.password !== undefined) {
      passwordRef.current?.focus();
      return;
    }

    setPending(true);

    try {
      await login({ username: username.trim(), password });
      navigate('/console', { replace: true });
    } catch (error) {
      setServerError(authErrorMessage(error));
    } finally {
      setPending(false);
    }
  }

  return (
    <AuthShell
      eyebrow="Console access"
      title={t('登录 ZTAPI')}
      intro={t('进入控制台，管理 API Key、调用记录与用量。')}
    >
      <form className="auth-form" onSubmit={handleSubmit} noValidate>
        <InlineNotice message={serverError} />
        <div className="auth-field">
          <label htmlFor="login-username">{t('账号')}</label>
          <input
            ref={usernameRef}
            id="login-username"
            name="username"
            autoFocus
            autoComplete="username"
            value={username}
            aria-describedby={
              fieldErrors.username === undefined
                ? undefined
                : 'login-username-error'
            }
            aria-invalid={fieldErrors.username !== undefined}
            onChange={(event) => updateUsername(event.target.value)}
          />
          {fieldErrors.username !== undefined ? (
            <p id="login-username-error" className="auth-field__error">
              {t(fieldErrors.username)}
            </p>
          ) : null}
        </div>
        <PasswordField
          ref={passwordRef}
          id="login-password"
          label={t('密码')}
          name="password"
          autoComplete="current-password"
          value={password}
          error={fieldErrors.password === undefined ? undefined : t(fieldErrors.password)}
          onChange={(event) => updatePassword(event.target.value)}
        />
        <button className="auth-submit" type="submit" disabled={pending}>
          {pending ? (
            <LoaderCircle className="auth-submit__spinner" aria-hidden="true" />
          ) : (
            <LogIn aria-hidden="true" />
          )}
          {pending ? t('登录中...') : t('登录')}
        </button>
      </form>
      <p className="auth-switch">
        {t('还没有账号？')} <Link to="/register">{t('创建账号')}</Link>
      </p>
    </AuthShell>
  );
}
