import { CircleDollarSign } from 'lucide-react';
import { useId } from 'react';
import { useLocale } from '../i18n/locale';

export function UsageBillingNote() {
  const headingId = useId();
  const { t } = useLocale();

  return (
    <aside
      aria-labelledby={headingId}
      className="usage-billing-note"
      role="note"
    >
      <CircleDollarSign aria-hidden="true" size={19} />
      <div>
        <strong id={headingId}>{t('用量与计费')}</strong>
        <p>
          {t('计费以上游返回的实际用量为准。部分模型上游会附带额外上下文，这部分同样计入用量。部分模型上游不严格遵守')}{' '}
          <code>max_tokens</code>{t('，实际输出可能超出该值并计入用量。')}
        </p>
        <p className="usage-billing-note__scope">
          {t('适用模型：Claude 全线、GPT 5.4 及以上、GLM / Qwen 推理系。')}
        </p>
      </div>
    </aside>
  );
}
