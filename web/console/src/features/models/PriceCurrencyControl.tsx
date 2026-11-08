import { useState } from 'react';
import type { DisplayFXReference } from '../../api/contracts';
import { useLocale } from '../../i18n/locale';
import { isValidDisplayRate, type PriceDisplay } from './pricing';
import './price-currency.css';

const storageKey = 'ztapi.priceCurrency';

export function usePriceDisplay(cnyPerUnit?: number | null, reference?: DisplayFXReference) {
  const { locale } = useLocale();
  const [preferred, setPreferred] = useState<PriceDisplay['currency']>(() => {
    try { return localStorage.getItem(storageKey) === 'U' ? 'U' : 'CNY'; } catch { return 'CNY'; }
  });
  const rate = reference ? Number(reference.cny_per_unit) : cnyPerUnit;
  const currency = preferred === 'CNY' && isValidDisplayRate(rate) ? 'CNY' : 'U';
  const display: PriceDisplay = { currency, cnyPerUnit: rate, cnyLabel: locale === 'en' ? 'CNY' : '元', referenceDate: reference?.rate_date, referenceStale: reference?.stale };
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
