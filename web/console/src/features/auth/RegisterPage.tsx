import { useRef, useState, type FormEvent } from 'react';
import { LoaderCircle, UserPlus } from 'lucide-react';
import { Link, useNavigate } from 'react-router-dom';
import { authErrorMessage } from '../../api/client';
import { useAuth } from '../../auth/session';
import { AuthShell } from './AuthShell';
import { InlineNotice } from './InlineNotice';
import { PasswordField } from './PasswordField';
import { useLocale } from '../../i18n/locale';

const USERNAME_ERROR =
  '账号需为 3-32 字节，仅可使用字母、数字、下划线或连字符';
const PASSWORD_ERROR = '密码需至少 10 个字符且不超过 256 字节';
const usernamePattern = /^[\p{L}\p{N}_-]+$/u;
const textEncoder = new TextEncoder();

interface RegistrationInput {
  username: string;
  password: string;
}

interface RegistrationErrors {
  username?: string;
  password?: string;
}

export function validateRegistrationInput({
  username,
  password,
}: RegistrationInput): RegistrationErrors {
  const errors: RegistrationErrors = {};
  const trimmedUsername = username.trim();
  const usernameBytes = textEncoder.encode(trimmedUsername).byteLength;
  const passwordCharacters = Array.from(password).length;
  const passwordBytes = textEncoder.encode(password).byteLength;

  if (
    usernameBytes < 3 ||
    usernameBytes > 32 ||
    !usernamePattern.test(trimmedUsername)
  ) {
    errors.username = USERNAME_ERROR;
  }

  if (passwordCharacters < 10 || passwordBytes > 256) {
    errors.password = PASSWORD_ERROR;
  }

  return errors;
}

export function RegisterPage() {
  const { t } = useLocale();
  const navigate = useNavigate();
  const { register } = useAuth();
  const usernameRef = useRef<HTMLInputElement>(null);
  const passwordRef = useRef<HTMLInputElement>(null);
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [fieldErrors, setFieldErrors] = useState<RegistrationErrors>({});
  const [serverError, setServerError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);

  function updateRegistrationUsername(value: string) {
    setUsername(value);
    setServerError(null);
    setFieldErrors((current) => ({ ...current, username: undefined }));
  }

  function updateRegistrationPassword(value: string) {
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
      await register({ username: username.trim(), password });
      navigate('/console', { replace: true });
    } catch (error) {
      setServerError(authErrorMessage(error));
    } finally {
      setPending(false);
    }
  }

  return (
    <AuthShell
      eyebrow="Create account"
      title={t('创建 ZTAPI 账号')}
      intro={t('创建账号后即可进入控制台并生成 API Key。')}
    >
      <form className="auth-form" onSubmit={handleSubmit} noValidate>
        <InlineNotice message={serverError} />
        <div className="auth-field">
          <label htmlFor="register-username">{t('账号')}</label>
          <input
            ref={usernameRef}
            id="register-username"
            name="username"
            autoFocus
            autoComplete="username"
            value={username}
            aria-describedby={
              fieldErrors.username === undefined
                ? 'register-username-help'
                : 'register-username-error'
            }
            aria-invalid={fieldErrors.username !== undefined}
            onChange={(event) => updateRegistrationUsername(event.target.value)}
          />
          {fieldErrors.username === undefined ? (
            <p id="register-username-help" className="auth-field__help">
              {t('3-32 字节，可使用字母、数字、下划线或连字符')}
            </p>
          ) : (
            <p id="register-username-error" className="auth-field__error">
              {t(fieldErrors.username)}
            </p>
          )}
        </div>
        <PasswordField
          ref={passwordRef}
          id="register-password"
          label={t('密码')}
          name="password"
          autoComplete="new-password"
          value={password}
          help={t('至少 10 个字符，最多 256 字节')}
          error={fieldErrors.password === undefined ? undefined : t(fieldErrors.password)}
          onChange={(event) => updateRegistrationPassword(event.target.value)}
        />
        <button className="auth-submit" type="submit" disabled={pending}>
          {pending ? (
            <LoaderCircle className="auth-submit__spinner" aria-hidden="true" />
          ) : (
            <UserPlus aria-hidden="true" />
          )}
          {pending ? t('创建中...') : t('创建账号')}
        </button>
      </form>
      <p className="auth-switch">
        {t('已有账号？')} <Link to="/login">{t('登录')}</Link>
      </p>
    </AuthShell>
  );
}
