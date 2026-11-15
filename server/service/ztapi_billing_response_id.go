package service

import "regexp"

// This is the format of the Xcloud charge-detail IDs already matched to upstream requests.
var ztapiXcloudChargeID = regexp.MustCompile(`^[0-9]{27}[A-Za-z]{12}$`)

func ztapiXcloudWorkbenchRequestID(method, scheme, host, path string, values []string) string {
	if method != "POST" || scheme != "https" || host != "www.xcloudtoken.com" ||
		(path != "/v1/chat/completions" && path != "/v1/responses") ||
		len(values) != 1 || !ztapiXcloudChargeID.MatchString(values[0]) {
		return ""
	}
	return values[0]
}
