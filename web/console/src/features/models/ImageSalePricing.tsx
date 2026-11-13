import type { ImageSale } from '../../api/contracts';
import { useLocale } from '../../i18n/locale';
import { formatImageUnitPrice, imageSalePriceDetails, type PriceDisplay } from './pricing';
import './image-sale.css';

export function ImageSalePricing({ name, sale, display }: { name: string; sale: ImageSale; display: PriceDisplay }) {
  const { t } = useLocale();
  const referencePrice = formatImageUnitPrice(sale.reference_unit_price, display, t('每张输出图'));
  const promptPrice = formatImageUnitPrice(sale.prompt_block_price, display, t('每张输出图'));
  return (
    <div className="model-image-sale">
      <ul className="model-image-sale__prices" aria-label={t('{{name}} 按张售价', { name })}>
        {imageSalePriceDetails(sale, display).map((price) => (
          <li key={price.key}><span>{price.label}</span><strong>{price.price}</strong></li>
        ))}
      </ul>
      <div className="model-image-sale__terms">
        {sale.version === 2 ? <>
          <p>{sale.allowed_requests?.map((r) => `${r.image_operation === 'edit' ? '编辑' : '生成'}：${r.size} ${r.quality}`).join('；')}。每次 1 张，0.30 U / 张。</p>
          <p>编辑限 1 张参考图，最多 4194304 像素；提示词最多 2000 个本地估算 Token（非精确分词计数）。</p>
          <p>其他尺寸、多张输出及额外档位尚未开放，待验收。</p>
        </> : <>
        <p>{t('基础包含 {{tokens}} 个本地估算 Token 和 {{references}} 份标准参考图档。', {
          tokens: sale.included_prompt_tokens, references: sale.included_reference_units,
        })}</p>
        <p>{t('额外参考图档 +{{price}}。', { price: referencePrice })}</p>
        <p>{t('提示词每多不足或满 {{tokens}} Token，+{{price}}。', { tokens: sale.prompt_block_tokens, price: promptPrice })}</p>
        <p>{t('标准参考图档按 {{side}}x{{side}} 像素面积（{{pixels}} 像素）计，超出按面积向上取整分档。', {
          side: Math.sqrt(sale.reference_unit_pixels), pixels: sale.reference_unit_pixels,
        })}</p>
        <p>{t('实际费用在派发前确定。')}</p>
        </>}
      </div>
    </div>
  );
}
