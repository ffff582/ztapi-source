package service

import (
	"regexp"
	"strings"
)

// Xcloud charge-detail IDs use a numeric prefix and an ASCII alphanumeric suffix.
var ztapiXcloudChargeID = regexp.MustCompile(`^[0-9]{27}[A-Za-z0-9]{12}$`)

func ztapiXcloudWorkbenchRequestID(method, scheme, host, path string, values []string) string {
	if method != "POST" || scheme != "https" || host != "www.xcloudtoken.com" ||
		(path != "/v1/chat/completions" && path != "/v1/responses") ||
		len(values) != 1 || !ztapiXcloudChargeID.MatchString(values[0]) ||
		!strings.ContainsAny(values[0][27:], "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ") {
		return ""
	}
	return values[0]
}
