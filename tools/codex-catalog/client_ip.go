package main

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

func (c *catalog) clientIP(r *http.Request) (string, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return "", false
	}
	peer, ok := parseIP(host)
	if !ok {
		return "", false
	}
	if !c.trusted(peer) {
		return peer.String(), true
	}
	if values := r.Header.Values("X-Forwarded-For"); len(values) != 0 {
		raw := strings.Join(values, ",")
		if len(raw) > 1024 {
			return "", false
		}
		parts := strings.Split(raw, ",")
		if len(parts) > 16 {
			return "", false
		}
		ips := make([]netip.Addr, len(parts))
		for i, part := range parts {
			ip, valid := parseIP(strings.TrimSpace(part))
			if !valid {
				return "", false
			}
			ips[i] = ip
		}
		// Ignore attacker-supplied left-hand addresses once an untrusted hop is found.
		for i := len(ips) - 1; i >= 0; i-- {
			if !c.trusted(ips[i]) || i == 0 {
				return ips[i].String(), true
			}
		}
	}
	if values := r.Header.Values("X-Real-IP"); len(values) != 0 {
		if len(values) != 1 {
			return "", false
		}
		ip, valid := parseIP(strings.TrimSpace(values[0]))
		if !valid {
			return "", false
		}
		return ip.String(), true
	}
	return peer.String(), true
}

func (c *catalog) trusted(ip netip.Addr) bool {
	for _, prefix := range c.trustedProxies {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

func parseIP(raw string) (netip.Addr, bool) {
	ip, err := netip.ParseAddr(raw)
	if err != nil || ip.Zone() != "" || ip.IsUnspecified() || ip.IsMulticast() {
		return netip.Addr{}, false
	}
	ip = ip.Unmap()
	if ip.IsUnspecified() || ip.IsMulticast() {
		return netip.Addr{}, false
	}
	return ip, true
}
