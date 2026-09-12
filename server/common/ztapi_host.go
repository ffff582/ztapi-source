package common

import (
	"net"
	"strings"
)

const ZTAPIAdminHostname = "admin.ztapi.vip"

func IsZTAPIAdminHost(host string) bool {
	host = strings.TrimSpace(host)
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}
	host = strings.TrimSuffix(host, ".")
	return strings.EqualFold(host, ZTAPIAdminHostname)
}
