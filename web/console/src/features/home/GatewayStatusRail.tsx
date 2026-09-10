import { GATEWAY_CAPABILITIES } from './homeContent';

export function GatewayStatusRail() {
  return (
    <section id="capabilities" className="gateway-status" aria-label="网关能力">
      {GATEWAY_CAPABILITIES.map((capability) => (
        <div className="gateway-status__item" key={capability}>
          <span className="gateway-status__signal" aria-hidden="true" />
          <span>{capability}</span>
        </div>
      ))}
    </section>
  );
}
