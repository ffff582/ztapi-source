import { ExternalLink, RotateCw } from 'lucide-react';
import { useEffect, useState } from 'react';
import { useLocale } from '../../i18n/locale';

const sourceRepository = 'https://github.com/ffff582/ztapi-source';

type SourceState =
  | { status: 'loading' | 'error' }
  | { status: 'ready'; commit: string; url: string };

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function readSourceOffer(value: unknown) {
  if (!isRecord(value) || value.success !== true || !isRecord(value.data)) {
    throw new Error('Invalid source offer');
  }
  const { license, source_commit: commit, source_url: url, source_repository: repository } = value.data;
  if (license !== 'AGPL-3.0-or-later' || typeof commit !== 'string' || !/^[a-f0-9]{40}$/.test(commit) ||
    url !== `${sourceRepository}/tree/${commit}` || (repository !== undefined && repository !== sourceRepository)) {
    throw new Error('Invalid source offer');
  }
  return { commit, url };
}

export function SourceOffer() {
  const { t } = useLocale();
  const [attempt, setAttempt] = useState(0);
  const [state, setState] = useState<SourceState>({ status: 'loading' });

  useEffect(() => {
    let current = true;
    const controller = new AbortController();
    setState({ status: 'loading' });
    const timeout = setTimeout(() => {
      controller.abort();
      if (current) setState({ status: 'error' });
    }, 10_000);

    void fetch('/.well-known/source', {
      method: 'GET',
      credentials: 'omit',
      redirect: 'error',
      cache: 'no-store',
      headers: { Accept: 'application/json' },
      signal: controller.signal,
    })
      .then((response) => {
        if (!response.ok) throw new Error('Source offer unavailable');
        return response.json() as Promise<unknown>;
      })
      .then(readSourceOffer)
      .then((offer) => {
        if (current && !controller.signal.aborted) setState({ status: 'ready', ...offer });
      })
      .catch(() => {
        if (current) setState({ status: 'error' });
      })
      .finally(() => { clearTimeout(timeout); });

    return () => {
      current = false;
      clearTimeout(timeout);
      controller.abort();
    };
  }, [attempt]);

  return (
    <div className="docs-source-offer">
      {state.status === 'loading' && <p role="status">{t('正在加载对应源码...')}</p>}
      {state.status === 'error' && (
        <div role="alert">
          <p>{t('当前版本对应源码暂不可用，请重试；不会改用旧版本链接。')}</p>
          <button type="button" onClick={() => setAttempt((value) => value + 1)}>
            <RotateCw aria-hidden="true" size={16} />
            <span>{t('重新加载对应源码')}</span>
          </button>
        </div>
      )}
      {state.status === 'ready' && (
        <>
          <a href={state.url} target="_blank" rel="noopener noreferrer">
            {t('获取当前版本对应源码')}<ExternalLink aria-hidden="true" size={16} />
          </a>
          <dl><div><dt>{t('源码提交')}</dt><dd><code>{state.commit}</code></dd></div></dl>
        </>
      )}
    </div>
  );
}
