import { Copy, RefreshCw, TriangleAlert } from 'lucide-react';
import { useEffect, useRef, useState, useSyncExternalStore } from 'react';
import { apiClient, getAuthSession, subscribeAuthSession } from '../../api/client';
import { parseUserModelCatalog, type UserModelCatalogItem } from '../../api/contracts';
import { useLocale } from '../../i18n/locale';
import { getClientSetup, guideClients, type GuideClient } from './client-setup';

export function ClientSetupGuide() {
  const { t } = useLocale();
  const user = useSyncExternalStore(subscribeAuthSession, getAuthSession, getAuthSession)?.user;
  const account = user ? `${user.id}:${user.group}` : 'anonymous';
  const [catalog, setCatalog] = useState<{
    account: string;
    status: 'loading' | 'ready' | 'error';
    models: UserModelCatalogItem[];
  }>({ account, status: 'loading', models: [] });
  const [reload, setReload] = useState(0);
  const [selection, setSelection] = useState({ account, modelID: '' });
  const [client, setClient] = useState<GuideClient>('openai');
  const [copyStatus, setCopyStatus] = useState<'idle' | 'success' | 'error'>('idle');
  const copyAttempt = useRef(0);
  const status = catalog.account === account ? catalog.status : 'loading';
  const models = status === 'ready' ? catalog.models : [];
  const model = selection.account === account
    ? models.find((item) => item.model_name === selection.modelID)
    : undefined;
  const setup = getClientSetup(model, client);

  useEffect(() => {
    let active = true;
    setCatalog({ account, status: 'loading', models: [] });
    setSelection({ account, modelID: '' });
    void apiClient.getResponse<unknown>('/user/models')
      .then(parseUserModelCatalog)
      .then(({ catalog: items }) => {
        if (active) setCatalog({ account, status: 'ready', models: items });
      })
      .catch(() => {
        if (active) setCatalog({ account, status: 'error', models: [] });
      });
    return () => { active = false; };
  }, [account, reload]);

  useEffect(() => {
    copyAttempt.current += 1;
    setCopyStatus('idle');
    return () => { copyAttempt.current += 1; };
  }, [account, client, model?.model_name, reload]);

  async function copyConfig() {
    if (!setup.snippet) return;
    const attempt = ++copyAttempt.current;
    setCopyStatus('idle');
    try {
      if (!navigator.clipboard?.writeText) throw new Error('Clipboard unavailable');
      await navigator.clipboard.writeText(setup.snippet);
      if (attempt === copyAttempt.current) setCopyStatus('success');
    } catch {
      if (attempt === copyAttempt.current) setCopyStatus('error');
    }
  }

  return (
    <section className="console-section" aria-labelledby="client-setup-heading">
      <div className="console-section__heading">
        <div>
          <p className="console-eyebrow">{t('客户端接入')}</p>
          <h2 id="client-setup-heading">{t('模型与客户端配置')}</h2>
        </div>
      </div>

      <div className="guide-reference__grid">
        <div className="console-field">
          <label htmlFor="guide-setup-model">{t('接入模型')}</label>
          <select id="guide-setup-model" value={model?.model_name ?? ''}
            disabled={status !== 'ready' || models.length === 0}
            onChange={(event) => setSelection({ account, modelID: event.target.value })}>
            <option value="">{t('选择模型 ID')}</option>
            {models.map((item) => <option key={item.model_name} value={item.model_name}>{item.model_name}</option>)}
          </select>
        </div>
        <div className="console-field">
          <label htmlFor="guide-setup-client">{t('客户端')}</label>
          <select id="guide-setup-client" value={client}
            onChange={(event) => setClient(event.target.value as GuideClient)}>
            {guideClients.map((item) => <option key={item.id} value={item.id}>{t(item.label)}</option>)}
          </select>
        </div>
      </div>

      {status === 'loading' && <div className="console-state" aria-live="polite" aria-busy="true">{t('正在加载模型目录...')}</div>}
      {status === 'error' && (
        <div className="console-state console-state--error" role="alert">
          <TriangleAlert aria-hidden="true" size={19} />
          {t('模型目录加载失败，无法确认客户端配置。')}
          <button className="console-button console-button--secondary" type="button" onClick={() => setReload((value) => value + 1)}>
            <RefreshCw aria-hidden="true" size={16} />{t('重试加载模型')}
          </button>
        </div>
      )}
      {status === 'ready' && models.length === 0 && <div className="console-state">{t('当前账户暂无可调用模型。')}</div>}

      <p role="note" aria-label={t('客户端接入状态')}>{t(setup.message)}</p>
      {model && (
        <dl className="guide-reference__grid">
          <div><dt>{t('模型 ID')}</dt><dd><code aria-label={t('所选模型 ID')}>{model.model_name}</code></dd></div>
          <div><dt>{t('目录声明的端点')}</dt><dd><code>{model.supported_endpoint_types.join(', ')}</code></dd></div>
          <div><dt>{t('所需请求地址')}</dt><dd><code>{`POST ${setup.path}`}</code></dd></div>
        </dl>
      )}

      {setup.snippet && (
        <>
          <dl className="guide-reference__grid">
            <div><dt>Base URL</dt><dd><code>{setup.baseURL}</code></dd></div>
            <div><dt>{t('鉴权方式')}</dt><dd><code>{setup.auth}</code></dd></div>
          </dl>
          <p>{t('配置已使用所选公开模型 ID；将 YOUR_ZTAPI_API_KEY 替换为你的密钥，不要使用供应商模型别名。')}</p>
          {client === 'codex' && <p>{t('将 TOML 配置合并到本机 Codex 配置文件，在启动 Codex 的终端设置 ZTAPI_API_KEY；不要覆盖已有配置或使用 ChatGPT 登录凭据。')}</p>}
          {client === 'claude-code' && <p>{t('Claude Code 使用不带 /v1 的 Base URL，客户端会追加 /v1/messages。示例清除当前终端中冲突的 ANTHROPIC_AUTH_TOKEN，使用 API Key。')}</p>}
          <div className="guide-code-workbench">
            <div className="guide-code-panel">
              <div className="guide-code-toolbar">
                <span>{setup.format}</span>
                <button type="button" onClick={() => void copyConfig()}>
                  <Copy aria-hidden="true" size={15} />{t('复制配置')}
                </button>
              </div>
              <pre data-testid="client-setup-code"><code>{setup.snippet}</code></pre>
              <span className="guide-copy-status" role="status" aria-label={t('配置复制结果')} aria-live="polite">
                {copyStatus === 'success' && t('已复制')}
                {copyStatus === 'error' && t('复制失败，请手动复制')}
              </span>
            </div>
          </div>
        </>
      )}

      <aside className="console-alert" role="note" aria-label={t('能力边界')}>
        <div>
          <strong>{t('端点声明不等于客户端完整验收')}</strong>
          <p>{t('未验证的能力不得从基本文本返回推断：流式输出、工具往返、strict JSON、图像输入和客户端 beta 功能均需按模型、路由和客户端版本单独验收。上方验收状态仅覆盖明确列出的范围。')}</p>
          <p>{t('不支持（仅 Responses → Chat 转换路径）：previous_response_id、服务端 conversation、托管 web_search / file_search 和原生 grammar 强制执行；多轮请求需携带完整历史。此限制不能外推到原生 Responses 路径。')}</p>
          <p>{t('Codex Opus 验收代理冲突已修复，但完整上游客户端验收尚未完成；配置模板不代表已完成验收。Cursor Tab 等封闭产品功能不属于 API 网关提供的能力。')}</p>
        </div>
      </aside>
    </section>
  );
}
