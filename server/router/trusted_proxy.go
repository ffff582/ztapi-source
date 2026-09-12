package router

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/gin-gonic/gin"
)

var allowedTrustedProxyNetworks = []netip.Prefix{
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("fc00::/7"),
}

func ParseTrustedProxyCIDRs(raw string, production bool) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if production {
			return nil, fmt.Errorf("ZTAPI_TRUSTED_PROXY_CIDRS is required in release mode")
		}
		return nil, nil
	}

	entries := strings.Split(raw, ",")
	result := make([]string, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		prefix, err := netip.ParsePrefix(entry)
		if err != nil {
			return nil, fmt.Errorf("invalid trusted proxy CIDR %q: %w", entry, err)
		}
		if prefix.Addr().Is4In6() {
			return nil, fmt.Errorf("IPv4-mapped IPv6 trusted proxy CIDR %q is not supported", entry)
		}
		prefix = prefix.Masked()
		if !isAllowedTrustedProxyNetwork(prefix) {
			return nil, fmt.Errorf("trusted proxy CIDR %q is not a private proxy network", entry)
		}
		normalized := prefix.String()
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	return result, nil
}

func ConfigureTrustedProxies(engine *gin.Engine, raw string, production bool) error {
	trustedProxies, err := ParseTrustedProxyCIDRs(raw, production)
	if err != nil {
		return err
	}
	engine.ForwardedByClientIP = true
	engine.RemoteIPHeaders = []string{"X-Forwarded-For", "X-Real-IP"}
	return engine.SetTrustedProxies(trustedProxies)
}

func isAllowedTrustedProxyNetwork(candidate netip.Prefix) bool {
	for _, allowed := range allowedTrustedProxyNetworks {
		if candidate.Addr().BitLen() != allowed.Addr().BitLen() {
			continue
		}
		if candidate.Bits() >= allowed.Bits() && allowed.Contains(candidate.Addr()) {
			return true
		}
	}
	return false
}
