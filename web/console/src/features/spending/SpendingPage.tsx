import { RefreshCw, Save, Undo2 } from 'lucide-react';
import { useEffect, useRef, useState, type FormEvent } from 'react';
import { Link } from 'react-router-dom';
import { getAuthSession } from '../../api/client';
import { spendingApi, SpendingError, validateSpendingPreferences, type SpendingConfig, type SpendingOverview, type SpendingPreferences } from '../../api/spending';
import { localeTag, useLocale } from '../../i18n/locale';
import { SpendingInbox } from './SpendingInbox';
import { SpendingReauthenticate } from './SpendingReauthenticate';
import { useSpendingInbox, useSpendingSessionKey } from './useSpendingInbox';
import './spending.css';

type Resource<T> = { data: T | null; status: 'loading' | 'ready' | 'error' | 'session-expired' };
export function SpendingPage() {
  const identity = useSpendingSessionKey();
  return <OwnerSpendingPage key={identity} />;
}
function OwnerSpendingPage() {
  const { t, locale } = useLocale();
  const inbox = useSpendingInbox();
  const [config, setConfig] = useState<Resource<SpendingConfig>>({ data: null, status: 'loading' });
  const [overview, setOverview] = useState<Resource<SpendingOverview>>({ data: null, status: 'loading' });
  const [revision, setRevision] = useState(0);
  const [overviewRevision, setOverviewRevision] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setConfig({ data: null, status: 'loading' });
    if (!getAuthSession()) return () => controller.abort();
    void spendingApi.config(controller.signal).then((data) => { if (!controller.signal.aborted) setConfig({ data, status: 'ready' }); }).catch((error) => { if (!controller.signal.aborted) setConfig({ data: null, status: error instanceof SpendingError && error.status === 401 ? 'session-expired' : 'error' }); });
    return () => controller.abort();
  }, [revision]);
  useEffect(() => {
    const controller = new AbortController();
    setOverview({ data: null, status: 'loading' });
    if (getAuthSession()) void spendingApi.overview(controller.signal).then((data) => { if (!controller.signal.aborted) setOverview({ data, status: 'ready' }); }).catch((error) => { if (!controller.signal.aborted) setOverview({ data: null, status: error instanceof SpendingError && error.status === 401 ? 'session-expired' : 'error' }); });
    return () => controller.abort();
  }, [overviewRevision]);
  const summary = overview.data;
  const sessionExpired = config.status === 'session-expired' || overview.status === 'session-expired' || inbox.status === 'session-expired';
  return <div className="console-page spending-page">
    <header className="console-page__header"><div><h1>{t('消费预警')}</h1><p className="spending-muted">{t('北京时间 (Asia/Shanghai)')}</p></div>
      <button className="spending-icon-button" type="button" title={t('刷新消费概览')} aria-label={t('刷新消费概览')} disabled={overview.status === 'loading'} onClick={() => setOverviewRevision((value) => value + 1)}><RefreshCw size={18} aria-hidden="true" /></button>
    </header>
    {sessionExpired && <SpendingReauthenticate />}
    <section className="spending-section" aria-labelledby="spending-overview-title">
      <h2 id="spending-overview-title">{t('消费概览')}</h2>
      {overview.status === 'loading' && <p role="status">{t('正在读取消费概览...')}</p>}
      {overview.status === 'error' && <p role="status">{t('消费概览暂不可用，请稍后重试。')}</p>}
      {summary && <>
        {!summary.complete && <p className="spending-warning" role="status">{t('汇总不完整，已确认金额可能尚未覆盖全部记录。')}</p>}
        <dl className="spending-metrics">
          {([['今日已确认消费', summary.confirmed_daily_u], ['近1小时已确认消费', summary.confirmed_hourly_u], ['今日确认退款', summary.confirmed_refunds_daily_u], ['可用余额', summary.available_balance_u]] as const).map(([label, amount]) => <div key={label}><dt>{t(label)}</dt><dd>{amount === null ? t('暂不可用') : `${amount} U`}</dd></div>)}
        </dl>
        <dl className="spending-counts"><div><dt>{t('待确认请求')}</dt><dd aria-label={t('待确认请求')}>{summary.pending_requests}</dd></div><div><dt>{t('证据不完整请求')}</dt><dd aria-label={t('证据不完整请求')}>{summary.incomplete_requests}</dd></div></dl>
        <p className="spending-muted">{t('预留不是最终扣费。')} {t('消费阈值按已确认消费累计计算，退款单独列示，不抵减阈值统计。')}</p>
        <p className="spending-muted">{t('数据截至')} <time dateTime={summary.as_of}>{new Date(summary.as_of).toLocaleString(localeTag(locale), { timeZone: 'Asia/Shanghai', hour12: false })}</time></p>
      </>}
    </section>
    <section className="spending-section" aria-labelledby="spending-settings-title"><h2 id="spending-settings-title">{t('预警设置')}</h2>
      {config.status === 'loading' && <p role="status">{t('正在读取预警设置...')}</p>}
      {config.status === 'error' && <div><p role="status">{t('预警服务暂不可用，请稍后重试。')}</p><button className="console-button console-button--secondary" type="button" onClick={() => setRevision((value) => value + 1)}><RefreshCw size={16} aria-hidden="true" />{t('重新加载预警设置')}</button></div>}
      {config.data && <SpendingSettings initial={config.data} showSessionNotice={!sessionExpired} onReload={() => setRevision((value) => value + 1)} />}
    </section>
    <section className="spending-section" aria-labelledby="spending-records-title"><h2 id="spending-records-title">{t('近期 20 条通知')}</h2><SpendingInbox showSessionNotice={false} /></section>
  </div>;
}

function editable(config: SpendingConfig): SpendingPreferences {
  const { enabled, daily_spend_u, hourly_spend_u, low_balance_u, input_tokens, telegram, version } = config;
  return { enabled, daily_spend_u, hourly_spend_u, low_balance_u, input_tokens, telegram, version };
}
function SpendingSettings({ initial, onReload, showSessionNotice }: { initial: SpendingConfig; onReload: () => void; showSessionNotice: boolean }) {
  const { t } = useLocale();
  const [saved, setSaved] = useState(initial);
  const [draft, setDraft] = useState(() => editable(initial));
  const [tokenText, setTokenText] = useState(String(initial.input_tokens));
  const [status, setStatus] = useState<'idle' | 'saving' | 'saved' | 'error' | 'conflict' | 'session-expired' | 'telegram_binding_required' | 'telegram_unavailable'>('idle');
  const request = useRef<AbortController | null>(null);
  useEffect(() => () => request.current?.abort(), []);
  const tokensValid = /^\d{1,10}$/.test(tokenText) && Number(tokenText) <= 1_000_000_000;
  const validation = !tokensValid ? '输入 Token 须为 0 至 1000000000 的整数。' : validateSpendingPreferences(draft);
  const dirty = JSON.stringify(draft) !== JSON.stringify(editable(saved)) || tokenText !== String(saved.input_tokens);
  const busy = status === 'saving';
  function update(patch: Partial<SpendingPreferences>) { setDraft((value) => ({ ...value, ...patch })); if (status === 'saved' || status === 'idle') setStatus('idle'); }
  async function save(event: FormEvent) {
    event.preventDefault();
    if (!dirty || validation || request.current || status === 'conflict' || status === 'error' || status === 'session-expired') return;
    const controller = new AbortController(); request.current = controller; setStatus('saving');
    try {
      const result = await spendingApi.save(draft, controller.signal);
      if (!controller.signal.aborted) { setSaved({ ...saved, ...result }); setDraft(result); setTokenText(String(result.input_tokens)); setStatus('saved'); }
    } catch (error) {
      if (!controller.signal.aborted) {
        if (error instanceof SpendingError && error.status === 401) setStatus('session-expired');
        else if (error instanceof SpendingError && error.status === 409 && error.code === 'version_conflict') setStatus('conflict');
        else if (error instanceof SpendingError && error.status === 409 && (error.code === 'telegram_binding_required' || error.code === 'telegram_unavailable')) {
          setStatus(error.code);
          setSaved((value) => ({ ...value, ...(error.code === 'telegram_binding_required' ? { telegram_bound: false } : { telegram_available: false }) }));
        } else setStatus('error');
      }
    } finally { if (request.current === controller) request.current = null; }
  }
  return <form className="spending-settings" onSubmit={(event) => void save(event)}>
    <fieldset disabled={busy}>
      <label className="spending-toggle"><input type="checkbox" checked={draft.enabled} onChange={(event) => update({ enabled: event.target.checked })} />{t('启用消费预警')}</label>
      <div className="spending-fields">
        {([['daily_spend_u', '今日确认消费阈值 (U)'], ['hourly_spend_u', '近 1 小时确认消费阈值 (U)'], ['low_balance_u', '低余额阈值 (U)']] as const).map(([key, label]) => <label key={key}>{t(label)}<input type="text" inputMode="decimal" maxLength={64} autoComplete="off" value={draft[key]} onChange={(event) => update({ [key]: event.target.value })} /></label>)}
        <label>{t('单次普通输入 Token 阈值')}<input type="text" inputMode="numeric" maxLength={10} autoComplete="off" value={tokenText} onChange={(event) => { setTokenText(event.target.value); update({ input_tokens: /^\d{1,10}$/.test(event.target.value) ? Number(event.target.value) : NaN }); }} /></label>
      </div>
      <p className="spending-muted">{t('金额留空或为 0 时关闭该规则；Token 为 0 时关闭。')}</p>
      <p className="spending-muted">{t('普通输入 Token 不含缓存；缺少可确认的普通输入用量时不触发该预警。')}</p>
      <label className="spending-toggle"><input type="checkbox" checked={draft.telegram} disabled={(!saved.telegram_bound || !saved.telegram_available) && !draft.telegram} onChange={(event) => update({ telegram: event.target.checked })} />{t('Telegram 通知')}</label>
      {(!saved.telegram_bound || !saved.telegram_available) && <Link to="/console/support" target="_blank" rel="noopener noreferrer">{t('前往客服绑定 Telegram')}</Link>}
      {!saved.telegram_available && <p role="status">{t('Telegram 通知暂不可用，站内通知不受影响。')}</p>}
    </fieldset>
    {dirty && <p className="spending-muted" role="status">{t('有未保存的修改')}</p>}
    {validation && <p className="spending-error" role="status">{t(validation)}</p>}
    {status === 'conflict' && <p className="spending-error" role="alert">{t('设置已在其他会话更新。你的修改尚未保存。')}</p>}
    {status === 'error' && <p className="spending-error" role="alert">{t('保存结果未确认，修改仍保留。请重新加载确认后再保存。')}</p>}
    {status === 'telegram_binding_required' && <p className="spending-error" role="alert">{t('Telegram 绑定已失效，修改仍保留。请重新绑定，或关闭 Telegram 通知后保存。')}</p>}
    {status === 'telegram_unavailable' && <p className="spending-error" role="alert">{t('Telegram 绑定状态暂不可用，修改仍保留。请稍后重试，或关闭 Telegram 通知后保存。')}</p>}
    {status === 'saved' && <p role="status">{t('预警设置已保存')}</p>}
    {status === 'session-expired' && showSessionNotice && <SpendingReauthenticate />}
    <div className="spending-actions">
      <button className="console-button" type="submit" aria-label={t('保存预警设置')} disabled={!dirty || !!validation || busy || status === 'conflict' || status === 'error' || status === 'session-expired'}><Save size={16} aria-hidden="true" />{t(busy ? '正在保存...' : '保存预警设置')}</button>
      <button className="console-button console-button--secondary" type="button" disabled={busy} onClick={status === 'conflict' || status === 'error' ? onReload : () => { setDraft(editable(saved)); setTokenText(String(saved.input_tokens)); setStatus((current) => current === 'session-expired' ? current : 'idle'); }}><Undo2 size={16} aria-hidden="true" />{t(status === 'conflict' || status === 'error' ? '放弃修改并重新加载' : '撤销修改')}</button>
    </div>
  </form>;
}
