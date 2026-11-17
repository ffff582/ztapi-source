import { GATEWAY_CAPABILITIES } from './homeContent';
import { useLocale } from '../../i18n/locale';

export function GatewayStatusRail() {
  const { t } = useLocale();
  return (
    <section id="capabilities" className="gateway-status" aria-label={t('网关能力')}>
      {GATEWAY_CAPABILITIES.map((capability) => (
        <div className="gateway-status__item" key={capability}>
          <span className="gateway-status__signal" aria-hidden="true" />
          <span>{t(capability)}</span>
        </div>
      ))}
    </section>
  );
}
