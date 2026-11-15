import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react';
import { LoaderCircle, RefreshCw, UserPlus, X } from 'lucide-react';
import { Link, useLocation, useNavigate } from 'react-router-dom';
import { apiClient, authErrorMessage } from '../../api/client';
import { parseRegistrationCaptcha, type RegistrationCaptcha } from '../../api/contracts';
import { useAuth } from '../../auth/session';
import { AuthShell } from './AuthShell';
import { InlineNotice } from './InlineNotice';
import { PasswordField } from './PasswordField';
import { useLocale } from '../../i18n/locale';
import { captureInvitation, clearInvitation } from '../referrals/invitation';
import { consoleReturnTo } from './return-to';
import { referralsApi } from '../../api/referrals';

const USERNAME_ERROR =
  '账号需为 3-32 字节，仅可使用字母、数字、下划线或连字符';
const PASSWORD_ERROR = '密码需至少 10 个字符且不超过 256 字节';
const EMAIL_ERROR = '请输入有效的邮箱地址';
const usernamePattern = /^[\p{L}\p{N}_-]+$/u;
const emailPattern = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
const textEncoder = new TextEncoder();

interface RegistrationInput {
  username: string;
  password: string;
  email?: string;
}

interface RegistrationErrors {
  username?: string;
  password?: string;
  email?: string;
}

export function validateRegistrationInput({
  username,
  password,
  email = '',
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

  if (email.trim() !== '' && !emailPattern.test(email.trim())) {
    errors.email = EMAIL_ERROR;
  }

  return errors;
}

export function RegisterPage() {
  const { t } = useLocale();
  const navigate = useNavigate();
  const location = useLocation();
  const [inviteCode, setInviteCode] = useState(() => captureInvitation(location.search));
  const [invitationStatus, setInvitationStatus] = useState<{ code: string; state: 'open' | 'closed' | 'unknown' } | null>(null);
  const { register } = useAuth();
  const usernameRef = useRef<HTMLInputElement>(null);
  const passwordRef = useRef<HTMLInputElement>(null);
  const emailRef = useRef<HTMLInputElement>(null);
  const captchaRef = useRef<HTMLInputElement>(null);
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [email, setEmail] = useState('');
  const [captcha, setCaptcha] = useState<RegistrationCaptcha | null>(null);
  const [captchaCode, setCaptchaCode] = useState('');
  const [captchaPending, setCaptchaPending] = useState(true);
  const [fieldErrors, setFieldErrors] = useState<RegistrationErrors>({});
  const [serverError, setServerError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);

  const loadCaptcha = useCallback(async () => {
    setCaptchaPending(true);
    setCaptchaCode('');
    try {
      const value = await apiClient.get<unknown>('/auth/captcha');
      setCaptcha(parseRegistrationCaptcha(value));
    } catch {
      setCaptcha(null);
    } finally {
      setCaptchaPending(false);
    }
  }, []);

  useEffect(() => {
    void loadCaptcha();
  }, [loadCaptcha]);

  useEffect(() => {
    setInviteCode(captureInvitation(location.search));
  }, [location.search]);

  useEffect(() => {
    if (!inviteCode) return;
    const code = inviteCode;
    const controller = new AbortController();
    void referralsApi.status(controller.signal).then(
      (campaign) => {
        if (!controller.signal.aborted) setInvitationStatus({ code, state: campaign.enabled && campaign.accepting_invites && campaign.blocked_reasons.length === 0 ? 'open' : 'closed' });
      },
      () => { if (!controller.signal.aborted) setInvitationStatus({ code, state: 'unknown' }); },
    );
    return () => controller.abort();
  }, [inviteCode]);

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

  function updateRegistrationEmail(value: string) {
    setEmail(value);
    setServerError(null);
    setFieldErrors((current) => ({ ...current, email: undefined }));
  }

  function removeInvitation() {
    clearInvitation();
    setInviteCode(undefined);
    setServerError(null);
    const params = new URLSearchParams(location.search);
    params.delete('invite');
    navigate({ pathname: location.pathname, search: params.size ? `?${params}` : '' }, { replace: true });
  }

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();

    if (pending) {
      return;
    }

    const errors = validateRegistrationInput({ username, password, email });
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

    if (errors.email !== undefined) {
      emailRef.current?.focus();
      return;
    }

    if (captcha === null) {
      setServerError(t('验证码加载失败，请点击刷新'));
      return;
    }

    if (captchaCode.trim() === '') {
      setServerError(t('请输入图中的验证码'));
      captchaRef.current?.focus();
      return;
    }

    setPending(true);

    try {
      const normalizedEmail = email.trim().toLowerCase();
      await register({
        username: username.trim(),
        password,
        captcha_id: captcha.captcha_id,
        captcha_code: captchaCode.trim(),
        ...(normalizedEmail === '' ? {} : { email: normalizedEmail }),
        ...(inviteCode === undefined ? {} : { invite_code: inviteCode }),
      });
      clearInvitation();
      navigate(consoleReturnTo(location.search), { replace: true });
    } catch (error) {
      setServerError(t(authErrorMessage(error)));
      // Every challenge is single use, so a failed attempt needs a fresh one.
      void loadCaptcha();
      captchaRef.current?.focus();
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
        {inviteCode !== undefined && <div className="auth-field">
          <p className="auth-field__help">{t('已携带邀请码，邀请是否生效以后台活动与绑定状态为准。活动关闭时可正常注册，不承诺计佣。')}</p>
          <p className="auth-field__help" role="status" aria-live="polite">{t(invitationStatus?.code !== inviteCode ? '正在确认推广活动状态...' : invitationStatus.state === 'closed' ? '推广活动当前未开放，暂不接受现金返佣绑定；本次是否绑定以注册结果为准。' : invitationStatus.state === 'unknown' ? '当前无法确认推广活动状态，是否建立返佣关系以注册结果为准。' : '活动状态允许接收新邀请，是否绑定仍以本次注册结果为准。')}</p>
          <button type="button" className="auth-invitation-remove" disabled={pending} onClick={removeInvitation}><X size={15} aria-hidden="true" />{t('清除邀请码并普通注册')}</button>
        </div>}
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
        <div className="auth-field">
          <label htmlFor="register-email">{t('邮箱（选填）')}</label>
          <input
            ref={emailRef}
            id="register-email"
            name="email"
            type="email"
            autoComplete="email"
            value={email}
            aria-describedby={
              fieldErrors.email === undefined
                ? 'register-email-help'
                : 'register-email-error'
            }
            aria-invalid={fieldErrors.email !== undefined}
            onChange={(event) => updateRegistrationEmail(event.target.value)}
          />
          {fieldErrors.email === undefined ? (
            <p id="register-email-help" className="auth-field__help">
              {t('可在注册后验证，用于找回密码。')}
            </p>
          ) : (
            <p id="register-email-error" className="auth-field__error">
              {t(fieldErrors.email)}
            </p>
          )}
        </div>
        <div className="auth-field">
          <label htmlFor="register-captcha">{t('验证码')}</label>
          <div className="auth-code-row">
            <input
              ref={captchaRef}
              id="register-captcha"
              name="captcha"
              autoComplete="off"
              inputMode="text"
              maxLength={8}
              value={captchaCode}
              onChange={(event) => {
                setCaptchaCode(event.target.value);
                setServerError(null);
              }}
            />
            <button
              type="button"
              className="auth-captcha"
              aria-label={t('刷新验证码')}
              onClick={() => void loadCaptcha()}
              disabled={captchaPending}
            >
              {captcha === null ? (
                <RefreshCw aria-hidden="true" />
              ) : (
                <img src={captcha.captcha_image} alt={t('验证码图片')} />
              )}
            </button>
          </div>
          <p className="auth-field__help">
            {captcha === null
              ? t('验证码加载失败，请点击刷新')
              : t('看不清可以点击图片换一张')}
          </p>
        </div>
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
        {t('已有账号？')} <Link to={`/login${new URLSearchParams(location.search).has('redirect') ? `?redirect=${encodeURIComponent(consoleReturnTo(location.search))}` : ''}`}>{t('登录')}</Link>
      </p>
    </AuthShell>
  );
}
