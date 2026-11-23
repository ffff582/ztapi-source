const providers = [
  { name: 'OpenAI', route: 'route 01', delay: '-0.2s' },
  { name: 'Claude', route: 'route 02', delay: '-1.8s' },
  { name: 'Gemini', route: 'route 03', delay: '-3.4s' },
] as const;

export function GatewayMotionVisual() {
  return (
    <div className="gateway-motion" data-testid="gateway-motion-visual">
      <div className="gateway-motion__frame" />
      <div className="gateway-motion__header">
        <span>REQUEST FLOW</span>
        <span className="gateway-motion__live">
          <i />
          ROUTING ACTIVE
        </span>
      </div>

      <div className="gateway-motion__stage">
        <div className="gateway-motion__request">
          <span>POST</span>
          <code>/v1/chat/completions</code>
          <small>encrypted request</small>
        </div>

        <div className="gateway-motion__ingress">
          <span className="gateway-motion__packet gateway-motion__packet--ingress" />
        </div>

        <div className="gateway-motion__gateway">
          <span className="gateway-motion__pulse gateway-motion__pulse--outer" />
          <span className="gateway-motion__pulse gateway-motion__pulse--inner" />
          <strong>ZT</strong>
          <span>ZTAPI GATEWAY</span>
        </div>

        <div className="gateway-motion__routes">
          {providers.map((provider, index) => (
            <div
              className="gateway-motion__route"
              data-route-provider={provider.name.toLowerCase()}
              key={provider.name}
            >
              <span className="gateway-motion__route-line">
                <span
                  className="gateway-motion__packet gateway-motion__packet--route"
                  style={{ animationDelay: provider.delay }}
                />
              </span>
              <span className="gateway-motion__provider-index">0{index + 1}</span>
              <span className="gateway-motion__provider-name">{provider.name}</span>
              <span className="gateway-motion__provider-state">
                <i />
                READY
              </span>
              <small>{provider.route}</small>
            </div>
          ))}
        </div>
      </div>

      <div className="gateway-motion__telemetry">
        <span><b>AUTH</b><i>verified</i></span>
        <span><b>ROUTE</b><i>selected</i></span>
        <span><b>METER</b><i>recording</i></span>
      </div>
    </div>
  );
}
