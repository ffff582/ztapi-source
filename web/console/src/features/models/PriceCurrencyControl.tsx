import { useEffect, useState } from 'react';
import { useLocale } from '../../i18n/locale';
import { isValidDisplayRate, type PriceDisplay } from './pricing';
import './price-currency.css';

const storageKey = 'ztapi.priceCurrency';

export function usePriceDisplay(cnyPerUnit?: number | null) {
  const { locale } = useLocale();
  const [reference, setReference] = useState<{ rate: number; date: string; stale: boolean } | null>(null);
  useEffect(() => {
    let active = true;
    let controller: AbortController | null = null;
    async function refresh() {
      const requestController = new AbortController();
      controller = requestController;
      const timeout = setTimeout(() => requestController.abort(), 8000);
      try {
        const response = await fetch('/api/display-fx', { signal: requestController.signal, credentials: 'omit' });
        if (!response.ok) return;
        const result: unknown = await response.json();
        if (!result || typeof result !== 'object' || !('success' in result) || result.success !== true || !('data' in result)) return;
        const data = result.data;
        if (active && data && typeof data === 'object' && 'available' in data && data.available === false) setReference(null);
        if (!data || typeof data !== 'object' || !('available' in data) || data.available !== true || !('cny_per_unit' in data) || typeof data.cny_per_unit !== 'string' ||
          !('rate_date' in data) || typeof data.rate_date !== 'string' || !/^\d{4}-\d{2}-\d{2}$/.test(data.rate_date) ||
          !('source' in data) || data.source !== 'ecb_usd_cny_reference') return;
        const rate = Number(data.cny_per_unit);
        if (active && Number.isFinite(rate) && rate >= 1 && rate <= 20) {
          setReference({ rate, date: data.rate_date, stale: 'stale' in data && data.stale === true });
        }
      } catch {
        if (active) setReference(value => value ? { ...value, stale: true } : value);
      }
      finally { clearTimeout(timeout); }
    }
    void refresh();
    const interval = setInterval(() => { void refresh(); }, 300_000);
    return () => { active = false; controller?.abort(); clearInterval(interval); };
  }, []);
  const [preferred, setPreferred] = useState<PriceDisplay['currency']>(() => {
    try { return localStorage.getItem(storageKey) === 'U' ? 'U' : 'CNY'; } catch { return 'CNY'; }
  });
  const rate = reference?.rate ?? cnyPerUnit;
  const currency = preferred === 'CNY' && isValidDisplayRate(rate) ? 'CNY' : 'U';
  const display: PriceDisplay = { currency, cnyPerUnit: rate, cnyLabel: locale === 'en' ? 'CNY' : '元', referenceDate: reference?.date, referenceStale: reference?.stale };
  function setCurrency(value: PriceDisplay['currency']) {
    setPreferred(value);
    try { localStorage.setItem(storageKey, value); } catch { /* The current view still works without storage. */ }
  }
  return { display, setCurrency };
}

export function PriceCurrencyControl({ display, onChange }: {
  display: PriceDisplay;
  onChange: (value: PriceDisplay['currency']) => void;
}) {
  const { t } = useLocale();
  const available = isValidDisplayRate(display.cnyPerUnit);
  return (
    <div className="price-currency">
      <div className="price-currency__switch" role="group" aria-label={t('价格币种')}>
        <button type="button" disabled={!available} aria-pressed={display.currency === 'CNY'} onClick={() => onChange('CNY')}>{t('人民币')}</button>
        <button type="button" aria-pressed={display.currency === 'U'} onClick={() => onChange('U')}>U</button>
      </div>
      <p>{available
        ? t('人民币为参考换算，1U = {{rate}} 元；实际仍按 U / USDT 结算。', { rate: display.cnyPerUnit as number })
        : t('人民币参考汇率暂不可用，显示 U 原价。')}</p>
      {available && <p>{display.referenceDate
        ? t('美元参考汇率日期：{{date}}（欧洲央行）；USDT 实际成交价可能不同。', { date: display.referenceDate })
        : t('配置参考汇率，非实时行情。')}
        {display.referenceStale ? ` ${t('使用最近有效缓存，汇率来源暂未刷新。')}` : ''}</p>}
    </div>
  );
}
