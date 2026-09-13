import { CheckCircle2, CircleDollarSign, Copy, Play } from 'lucide-react';
import { FormEvent, useEffect, useMemo, useRef, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { apiClient, getAuthSession } from '../../api/client';
import { parseUserLogPage, parseUserModelCatalog, type UserModelCatalogItem } from '../../api/contracts';
import { PlaygroundClientError, playgroundClient, type PlaygroundChatResult } from '../../api/playground';
import { useLocale } from '../../i18n/locale';
import { markModelSelected } from '../onboarding/onboarding';

const defaultPrompt = '请用一句话介绍你自己。';

function isTestableModel(item: UserModelCatalogItem) {
  return item.modality === 'text' && item.supported_endpoint_types.includes('openai');
}

function testErrorMessage(error: unknown) {
  if (error instanceof PlaygroundClientError) {
    switch (error.kind) {
      case 'insufficient_balance':
        return '余额不足，请先充值后再测试。';
      case 'model_unavailable':
        return '当前模型暂不可用，请更换模型或稍后再试。';
      case 'rate_limited':
        return '请求较多，请稍后再试。';
      case 'unauthorized':
        return '登录状态已失效，请重新登录。';
      case 'invalid_response':
      case 'unknown':
        break;
    }
  }
  return '测试请求失败，请稍后重试。';
}

function codeExample(model: string) {
  return `export ZTAPI_API_KEY="YOUR_ZTAPI_API_KEY"

curl https://ztapi.vip/v1/chat/completions \\
  -H "Authorization: Bearer $ZTAPI_API_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "${model || 'YOUR_MODEL_ID'}",
    "messages": [{"role": "user", "content": "你好"}]
  }'`;
}

export function PlaygroundPage() {
  const { t } = useLocale();
  const [searchParams] = useSearchParams();
  const [models, setModels] = useState<UserModelCatalogItem[]>([]);
  const [catalogStatus, setCatalogStatus] = useState<'loading' | 'ready' | 'error'>('loading');
  const [model, setModel] = useState('');
  const [prompt, setPrompt] = useState(defaultPrompt);
  const [requestStatus, setRequestStatus] = useState<'idle' | 'sending' | 'success' | 'error'>('idle');
  const [result, setResult] = useState<PlaygroundChatResult | null>(null);
  const [billedAmount, setBilledAmount] = useState<number | null>(null);
  const [elapsedMs, setElapsedMs] = useState<number | null>(null);
  const [errorMessage, setErrorMessage] = useState('');
  const [copyState, setCopyState] = useState<'idle' | 'success' | 'error'>('idle');
  const requestSequence = useRef(0);

  useEffect(() => {
    let active = true;
    void apiClient
      .getResponse<unknown>('/user/models')
      .then(parseUserModelCatalog)
      .then((catalog) => catalog.catalog.filter(isTestableModel))
      .then((testableModels) => {
        if (!active) return;
        setModels(testableModels);
        const requested = searchParams.get('model');
        const selected = testableModels.some((item) => item.model_name === requested)
          ? requested ?? ''
          : testableModels[0]?.model_name ?? '';
        setModel(selected);
        const userID = getAuthSession()?.user.id;
        if (selected !== '' && userID !== undefined) markModelSelected(userID);
        setCatalogStatus('ready');
      })
      .catch(() => {
        if (active) setCatalogStatus('error');
      });
    return () => {
      active = false;
    };
  }, [searchParams]);

  const example = useMemo(() => codeExample(model), [model]);

  async function loadBilling(requestID: string, sequence: number) {
    if (requestID === '') return;
    try {
      const query = new URLSearchParams({ p: '1', page_size: '1', request_id: requestID });
      const page = parseUserLogPage(await apiClient.get<unknown>(`/log/self?${query.toString()}`));
      if (sequence !== requestSequence.current) return;
      const log = page.items.find((item) => item.request_id === requestID);
      setBilledAmount(log?.billed_amount ?? null);
    } catch {
      // The model answer remains useful when the asynchronous log is not ready yet.
    }
  }

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const normalizedPrompt = prompt.trim();
    if (requestStatus === 'sending' || model === '' || normalizedPrompt === '') return;

    const sequence = ++requestSequence.current;
    const startedAt = performance.now();
    setRequestStatus('sending');
    setResult(null);
    setBilledAmount(null);
    setElapsedMs(null);
    setErrorMessage('');
    try {
      const value = await playgroundClient.chat({ model, prompt: normalizedPrompt });
      if (sequence !== requestSequence.current) return;
      setElapsedMs(Math.max(0, Math.round(performance.now() - startedAt)));
      setResult(value);
      setRequestStatus('success');
      await loadBilling(value.request_id, sequence);
    } catch (error) {
      if (sequence !== requestSequence.current) return;
      setElapsedMs(Math.max(0, Math.round(performance.now() - startedAt)));
      setErrorMessage(testErrorMessage(error));
      setRequestStatus('error');
    }
  }

  async function copyExample() {
    setCopyState('idle');
    try {
      await navigator.clipboard.writeText(example);
      setCopyState('success');
    } catch {
      setCopyState('error');
    }
  }

  return (
    <div className="console-page playground-page">
      <header className="console-page__header">
        <div>
          <p className="console-eyebrow">{t('接入验证')}</p>
          <h1>{t('在线 API 测试')}</h1>
        </div>
        <p>{t('用当前账号真实调用模型，确认模型、余额、计费和返回结果是否正常。')}</p>
      </header>

      <div className="playground-grid">
        <section className="console-panel" aria-labelledby="playground-request-heading">
          <div className="console-panel__heading">
            <Play aria-hidden="true" size={19} />
            <h2 id="playground-request-heading">{t('发送测试请求')}</h2>
          </div>

          <aside className="playground-billing-note" aria-label={t('计费提醒')} role="note">
            <CircleDollarSign aria-hidden="true" size={18} />
            <div>
              <strong>{t('本次测试会按正常 API 请求扣费')}</strong>
              <p>{t('请求会经过正式模型线路，并在使用日志中留下费用记录。')}</p>
            </div>
          </aside>

          {catalogStatus === 'loading' && <div className="console-state">{t('正在加载可测试模型...')}</div>}
          {catalogStatus === 'error' && (
            <div className="console-alert" role="alert">{t('可测试模型加载失败，请刷新后重试。')}</div>
          )}
          {catalogStatus === 'ready' && models.length === 0 && (
            <div className="console-state">{t('当前账号暂无可在线测试的文本模型。')}</div>
          )}
          {catalogStatus === 'ready' && models.length > 0 && (
            <form className="playground-form" onSubmit={handleSubmit}>
              <div className="console-field">
                <label htmlFor="playground-model">{t('测试模型')}</label>
                <select id="playground-model" value={model} onChange={(event) => {
                  setModel(event.target.value);
                  const userID = getAuthSession()?.user.id;
                  if (userID !== undefined) markModelSelected(userID);
                }}>
                  {models.map((item) => <option key={item.model_name} value={item.model_name}>{item.model_name}</option>)}
                </select>
              </div>
              <div className="console-field">
                <label htmlFor="playground-prompt">{t('测试问题')}</label>
                <textarea
                  id="playground-prompt"
                  maxLength={4_000}
                  rows={7}
                  value={prompt}
                  onChange={(event) => setPrompt(event.target.value)}
                />
                <p className="console-field__help">{t('{{count}} / 4000 字符', { count: prompt.length })}</p>
              </div>
              <button
                className="console-button console-button--primary"
                disabled={requestStatus === 'sending' || prompt.trim() === ''}
                type="submit"
              >
                <Play aria-hidden="true" size={16} />
                {requestStatus === 'sending' ? t('正在调用...') : t('发送测试请求')}
              </button>
            </form>
          )}

          {requestStatus === 'error' && <div className="console-alert playground-result-alert" role="alert">{t(errorMessage)}</div>}
        </section>

        <section className="console-panel playground-result" aria-labelledby="playground-result-heading">
          <div className="console-panel__heading">
            <CheckCircle2 aria-hidden="true" size={19} />
            <h2 id="playground-result-heading">{t('调用结果')}</h2>
          </div>
          {result === null ? (
            <div className="console-state">{t('发送请求后，模型回答和本次费用会显示在这里。')}</div>
          ) : (
            <div className="playground-result__body">
              <div className="playground-answer">{result.text}</div>
              <dl className="playground-metrics">
                <div><dt>{t('Token 用量')}</dt><dd>{result.usage?.total_tokens ?? 0} tokens</dd></div>
                <div><dt>{t('本次费用')}</dt><dd>{billedAmount === null ? t('入账中') : `$${billedAmount.toFixed(6)}`}</dd></div>
                <div><dt>{t('页面耗时')}</dt><dd>{elapsedMs === null ? '—' : `${elapsedMs} ms`}</dd></div>
                <div><dt>{t('结束原因')}</dt><dd>{result.finish_reason || '—'}</dd></div>
              </dl>
              <div className="playground-request-id">
                <span>{t('请求 ID')}</span>
                <code>{result.request_id || '—'}</code>
              </div>
              {result.request_id !== '' && (
                <Link className="console-button console-button--secondary" to={`/console/logs?request_id=${encodeURIComponent(result.request_id)}`}>
                  {t('在使用日志中查看')}
                </Link>
              )}
            </div>
          )}
        </section>
      </div>

      <section className="console-section" aria-labelledby="playground-code-heading">
        <div className="console-section__heading">
          <div>
            <p className="console-eyebrow">{t('接入代码')}</p>
            <h2 id="playground-code-heading">{t('把相同模型接入你的程序')}</h2>
          </div>
          <button className="console-icon-action" type="button" onClick={copyExample}>
            <Copy aria-hidden="true" size={15} />
            {t('复制代码')}
          </button>
        </div>
        <pre className="playground-code" data-testid="playground-code"><code>{example}</code></pre>
        <p className="playground-copy-status" aria-live="polite" role="status">
          {copyState === 'success' && t('已复制')}
          {copyState === 'error' && t('复制失败，请手动复制')}
        </p>
      </section>
    </div>
  );
}
