import { useRef, useState, type FormEvent } from 'react';
import { LoaderCircle, Send } from 'lucide-react';
import { referralMoneyMicros, type ReferralOverview, type ReferralWithdrawalInput } from '../../api/contracts';
import { isTronAddress, normalizeReferralAmount } from '../../api/referrals';
import { useLocale } from '../../i18n/locale';
import type { MutationOutcome } from './ReferralPage';

type Attempt = Pick<ReferralWithdrawalInput, 'amount' | 'address' | 'idempotency_key'>;

export function WithdrawalForm({ data, enabled, busy, submit }: { data: ReferralOverview | null; enabled: boolean; busy: boolean; submit: (input: ReferralWithdrawalInput) => Promise<MutationOutcome> }) {
  const { t } = useLocale();
  const [amount, setAmount] = useState('');
  const [address, setAddress] = useState('');
  const [password, setPassword] = useState('');
  const [totp, setTotp] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [attempt, setAttempt] = useState<Attempt | null>(null);
  const lock = useRef(false);
  const enoughAvailable = !!data && referralMoneyMicros(data.summary.available_commission) >= referralMoneyMicros(data.campaign.minimum_withdrawal);
  const unavailable = !enabled || !data || data.summary.withdrawal_reauth_available === false || (!attempt && !enoughAvailable);
  async function handleSubmit(event: FormEvent) {
    event.preventDefault();
    if (busy || lock.current || unavailable || !data) return;
    setError(null);
    const normalized = normalizeReferralAmount(amount, data.campaign.minimum_withdrawal, data.summary.available_commission);
    if (!attempt && normalized === null) { setError('金额须至少 20 USDT、不超过可提佣金，最多 6 位小数，不支持指数。'); return; }
    if (!isTronAddress(address)) { setError('请输入有效的 TRC20 收款地址，最终校验以服务器为准。'); return; }
    if (!password) { setError('请输入当前账号密码进行提现验证。'); return; }
    if ((totp !== '' && !/^\d{6}$/.test(totp)) || (data.summary.requires_totp && totp === '')) { setError('请填写有效的 6 位二次验证码。'); return; }
    const current = attempt ?? { amount: normalized!, address, idempotency_key: crypto.randomUUID() };
    lock.current = true;
    setPassword('');
    setTotp('');
    try {
      const outcome = await submit({ ...current, password, ...(totp === '' ? {} : { totp_code: totp }) });
      // An ambiguous request retains its immutable key/payload, never the credentials.
      setAttempt(outcome === 'unknown' ? current : null);
      if (outcome === 'success') setAmount('');
    } finally { lock.current = false; }
  }
  return <form className="referral-form" onSubmit={(event) => void handleSubmit(event)} noValidate autoComplete="off">
    {data?.summary.withdrawal_reauth_available === false && <p className="referral-warning">{t('请先在账号安全设置密码，再申请提现。')} <a href="/console/account">{t('账号安全设置')}</a></p>}
    {data && !enoughAvailable && !attempt && <p className="referral-muted">{t('可提佣金达到 20 USDT 后可申请提现。')}</p>}
    {error && <p className="referral-error" role="alert">{t(error)}</p>}
    {attempt && <p className="referral-warning">{t('上次申请结果尚未确认。核对记录后，可重新验证并重试同一申请，不会生成新的申请编号。')}</p>}
    <fieldset disabled={busy || unavailable}>
      <div className="referral-fields">
        <label htmlFor="referral-amount">{t('提现金额 (USDT)')}<input id="referral-amount" type="text" inputMode="decimal" maxLength={20} value={amount} readOnly={attempt !== null} onChange={(event) => { setAmount(event.target.value); setError(null); }} /></label>
        <label htmlFor="referral-address">{t('TRC20 收款地址')}<input id="referral-address" type="text" autoComplete="off" spellCheck={false} maxLength={34} value={address} readOnly={attempt !== null} onChange={(event) => { setAddress(event.target.value); setError(null); }} /></label>
        <label htmlFor="referral-password">{t('当前账号密码')}<input id="referral-password" type="password" autoComplete="off" maxLength={1024} value={password} onChange={(event) => setPassword(event.target.value)} /></label>
        <label htmlFor="referral-totp">{t('二次验证码（选填）')}<input id="referral-totp" type="password" inputMode="numeric" autoComplete="off" maxLength={6} pattern="[0-9]{6}" aria-describedby="referral-totp-help" value={totp} onChange={(event) => setTotp(event.target.value)} /></label>
      </div>
      <p className="referral-muted" id="referral-totp-help">{t('已启用两步验证时，请填写 6 位二次验证码。密码和验证码不保存。')}</p>
      <button className="referral-command" type="submit" aria-label={t(attempt ? '重试原提现' : '申请提现')} disabled={busy || unavailable}>{busy ? <LoaderCircle className="referral-spinner" size={16} aria-hidden="true" /> : <Send size={16} aria-hidden="true" />}{t(attempt ? '重试原提现' : '申请提现')}</button>
    </fieldset>
  </form>;
}
