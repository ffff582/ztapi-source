import { ExternalLink } from 'lucide-react';
import type { OfficialPriceReference as Reference } from '../../api/contracts';
import { useLocale } from '../../i18n/locale';
import type { ModelPriceDetail } from './pricing';
import './official-price-reference.css';

export function OfficialPriceReference({ reference }: { reference?: Reference }) {
  const { t } = useLocale();
  if (!reference) return null;
  const quoted = reference.kind === 'supplier_quoted_official';
  return (
    <div className="official-price-reference">
      <a href={reference.url} target="_blank" rel="noopener noreferrer">
        {t(quoted ? '报价表官网基准' : '官网价格参考')}<ExternalLink aria-hidden="true" size={13} />
      </a>
      <span>{t(quoted ? '报价基准记录日期' : '官网参考核验日期')}：<time dateTime={reference.checked_at}>{reference.checked_at.slice(0, 10)}</time></span>
      {quoted && <span>{t('供应商报价基准，不代表当前官网价格已独立核验。')}</span>}
      {reference.note && <p>{reference.note}</p>}
    </div>
  );
}

export function OfficialPriceComparison({ price, quoted = false }: { price: ModelPriceDetail; quoted?: boolean }) {
  const { t } = useLocale();
  const difference = price.officialDifferencePercent;
  if (difference === undefined) return null;
  const absolute = Math.abs(difference);
  const percent = absolute > 0 && absolute < 0.01 ? '<0.01' : String(Number(absolute.toFixed(2)));
  const label = difference === 0
    ? quoted ? '与报价基准一致' : '与官网参考一致'
    : difference > 0
      ? quoted ? '高于报价基准 {{percent}}%' : '高于官网 {{percent}}%'
      : quoted ? '低于报价基准 {{percent}}%' : '低于官网 {{percent}}%';
  return <span className="official-price-comparison">{t(label, { percent })}</span>;
}
