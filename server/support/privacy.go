// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package support

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)["']?(?:authorization|api[_ -]?key|password|密钥|密码)["']?\s*[:=：]\s*(?:"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|(?:(?:bearer|basic)\s+)?[^\s"',;]+)`),
	regexp.MustCompile(`(?i)\bBearer\s+[a-z0-9._~+/=-]{6,}`),
	regexp.MustCompile(`(?i)\bBasic\s+[a-z0-9+/=]{8,}`),
	regexp.MustCompile(`(?i)\bsk[-_][a-z0-9_-]{8,}`),
	regexp.MustCompile(`\beyJ[a-zA-Z0-9_-]{8,}\.[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+`),
	regexp.MustCompile(`(?i)(?:api[_ -]?key|password|密钥|密码)\s*[:=：]\s*["']?[^\s"',;]{6,}`),
	regexp.MustCompile(`(?i)authorization\s*[:=：]\s*["']?(?:(?:bearer|basic)\s+)?[^\s"',;]{6,}`),
}
var safePlaceholder = regexp.MustCompile(`(?i)^(?:["']?(?:authorization|api[_ -]?key|password|密钥|密码)["']?\s*[:=：]\s*["']?)?(?:(?:bearer|basic)\s+)?(?:YOUR_(?:ZTAPI_)?API_KEY|\$ZTAPI_API_KEY|\$\{ZTAPI_API_KEY\}|<YOUR_API_KEY>|\[已隐藏凭据\])["']?$`)

func Redact(s string) string {
	for _, pattern := range secretPatterns {
		s = pattern.ReplaceAllStringFunc(s, func(match string) string {
			if safePlaceholder.MatchString(match) {
				return match
			}
			return "[已隐藏凭据]"
		})
	}
	return strings.ToValidUTF8(s, "")
}

func boundedText(s string, max int) bool {
	return utf8.ValidString(s) && utf8.RuneCountInString(s) <= max && !strings.ContainsRune(s, '\x00')
}

func shortText(s string, max int) string {
	r := []rune(s)
	if len(r) > max {
		return string(r[:max])
	}
	return s
}
