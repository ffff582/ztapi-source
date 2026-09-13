package common

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"sort"
	"strings"
)

// ZTAPIHealthCredentialMaterial returns the exact effective outbound
// credential material. Callers must hash it before durable storage.
func ZTAPIHealthCredentialMaterial(info *RelayInfo, header http.Header) string {
	parts := make([]string, 0, 2)
	credentialHeaders := map[string]bool{
		"authorization":          true,
		"api-key":                true,
		"x-api-key":              true,
		"x-goog-api-key":         true,
		"sec-websocket-protocol": true,
	}
	if info != nil {
		for _, name := range info.ChannelSetting.ZTAPIHealthCredentialHeaders {
			if canonical := strings.ToLower(strings.TrimSpace(name)); canonical != "" {
				credentialHeaders[canonical] = true
			}
		}
	}
	for canonical := range credentialHeaders {
		name := http.CanonicalHeaderKey(canonical)
		if canonical == "sec-websocket-protocol" {
			for _, token := range strings.Split(header.Get(name), ",") {
				token = strings.TrimSpace(token)
				if strings.HasPrefix(token, "openai-insecure-api-key.") {
					parts = append(parts, canonical+"\x00"+token)
					break
				}
			}
			continue
		}
		for _, value := range header.Values(name) {
			value = strings.TrimSpace(value)
			if value != "" {
				parts = append(parts, canonical+"\x00"+value)
			}
		}
	}
	if len(parts) == 0 {
		if info == nil {
			return ""
		}
		return info.ApiKey
	}
	sort.Strings(parts)
	return strings.Join(parts, "\x00")
}

func ZTAPIHealthCredentialVersion(material string) (string, error) {
	if material == "" {
		return "", errors.New("ZTAPI health credential is unavailable")
	}
	digest := sha256.Sum256([]byte(material))
	return hex.EncodeToString(digest[:]), nil
}
