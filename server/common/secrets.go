package common

import (
	"regexp"
	"strings"
)

const redactedSecret = "[REDACTED]"

var secretAuthorization = regexp.MustCompile(`(?i)((?:\\?["'])?\b(?:proxy[-_]?)?authorization(?:\\?["'])?\s*[:=]\s*)(?:"(?:\\.|[^"\\])*"|'[^']*'|(?:bearer|basic)\s+[^\s,;}\]]+|[^\s,;}\]]+)`)
var secretCookie = regexp.MustCompile(`(?i)((?:\\?["'])?\b(?:set[-_]?)?cookie(?:\\?["'])?\s*[:=]\s*)(?:"(?:\\.|[^"\\])*"|'[^']*'|[^\r\n}]*)`)
var secretAssignment = regexp.MustCompile(`(?i)((?:\\?["'])?\b(?:[a-z][a-z0-9_.-]*[_-])?(?:api[_-]?key|master[_-]?key|secret(?:[_-]?key)?|access[_-]?token|refresh[_-]?token|auth[_-]?token|password|passwd|pwd|credential|sql[_-]?dsn|redis[_-](?:conn[_-])?string|dsn|crypto[_-]?secret|session(?:[_-]?key)?|key|token)(?:\\?["'])?\s*[:=]\s*)(?:"(?:\\.|[^"\\])*"|'[^']*'|[^\s,;}&]+)`)
var secretField = regexp.MustCompile(`(?i)^(?:(?:[a-z][a-z0-9_.-]*[_-])?(?:api[_-]?key|master[_-]?key|secret(?:[_-]?key)?|access[_-]?token|refresh[_-]?token|auth[_-]?token|password|passwd|pwd|credential|sql[_-]?dsn|redis[_-](?:conn[_-])?string|dsn|crypto[_-]?secret|session(?:[_-]?key)?|key|token)|(?:proxy[-_]?)?authorization|(?:set[-_]?)?cookie)$`)
var secretToken = regexp.MustCompile(`(?i)\b(?:sk-[a-z0-9_-]{8,}|AIza[a-z0-9_-]{20,}|gh[pousr]_[a-z0-9]{20,}|github_pat_[a-z0-9_]{20,}|[0-9]{8,}:[a-z0-9_-]{24,}|eyJ[a-z0-9_-]{8,}\.[a-z0-9_-]{8,}\.[a-z0-9_-]{8,})\b`)
var secretPrivateKey = regexp.MustCompile(`(?s)-----BEGIN (?:[A-Z]+ )?PRIVATE KEY-----.*?(?:-----END (?:[A-Z]+ )?PRIVATE KEY-----|$)`)
var secretURLUser = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^/\s@]+@`)
var secretBearer = regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+[^\s,;}]+`)
var secretSignature = regexp.MustCompile(`(?i)((?:\\?["'])?\b(?:x[-_]amz[-_])?signature(?:\\?["'])?\s*[:=]\s*)(?:"(?:\\.|[^"\\])*"|'[^']*'|[^\s,;}&]+)`)
var secretEnvironmentAssignment = regexp.MustCompile(`(?i)\b(?:environment|environ|env)(?:\\?["'])?\s*[:=]\s*[\[{]`)
var secretPluralContainerAssignment = regexp.MustCompile(`(?i)(?:\\?["'])(?:credentials|secrets)(?:\\?["'])\s*[:=]\s*[\[{]`)

const maxSecretJSONBytes = 1 << 20
const maxSecretJSONDepth = 64

func redactAssignment(pattern *regexp.Regexp, text string) string {
	return pattern.ReplaceAllStringFunc(text, func(match string) string {
		parts := pattern.FindStringSubmatchIndex(match)
		prefix := match[parts[2]:parts[3]]
		value := match[parts[3]:]
		if strings.EqualFold(strings.Trim(prefix, " \t:='\"\\"), "idempotency_key") {
			return match
		}
		if value == "?" || isSQLParameter(value) {
			return match
		}
		switch {
		case strings.HasPrefix(value, `\"`):
			return prefix + `\"` + redactedSecret + `\"`
		case strings.HasPrefix(value, `"`):
			return prefix + `"` + redactedSecret + `"`
		case strings.HasPrefix(value, "'"):
			return prefix + "'" + redactedSecret + "'"
		default:
			return prefix + redactedSecret
		}
	})
}

func isSQLParameter(value string) bool {
	if len(value) < 2 || value[0] != '$' {
		return false
	}
	for _, ch := range value[1:] {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

// RedactSecrets removes credential values, not operational IDs or usage/cost
// numbers. Apply it before truncation, and only at evidence/output boundaries.
func RedactSecrets(text string) string {
	return redactEmbeddedSecretJSON(text, 0)
}

func secretCredentialField(key string) bool {
	if strings.EqualFold(key, "credentials") || strings.EqualFold(key, "secrets") {
		return true
	}
	return secretField.MatchString(key) && !strings.EqualFold(key, "idempotency_key") || strings.EqualFold(key, "signature") || strings.EqualFold(key, "x-amz-signature")
}

func secretEnvironmentField(key string) bool {
	return strings.EqualFold(key, "environment") || strings.EqualFold(key, "environ") || strings.EqualFold(key, "env")
}

func secretStructuredAssignment(text string, start int) bool {
	i := start - 1
	for i >= 0 && strings.ContainsRune(" \t\r\n", rune(text[i])) {
		i--
	}
	if i < 0 || text[i] != ':' && text[i] != '=' {
		return false
	}
	i--
	for i >= 0 && strings.ContainsRune(" \t\r\n", rune(text[i])) {
		i--
	}
	if i >= 0 && (text[i] == '"' || text[i] == '\'') {
		i--
		if i >= 0 && text[i] == '\\' {
			i--
		}
	}
	end := i + 1
	for i >= 0 {
		ch := text[i]
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || strings.ContainsRune("_.-", rune(ch))) {
			break
		}
		i--
	}
	key := text[i+1 : end]
	return secretCredentialField(key) || secretEnvironmentField(key)
}

// Frame disjoint candidates once; never retry a decoder at nested openers.
func secretJSONSpan(text string, start int) (end int, complete, tooDeep bool) {
	quoted, inString := text[start] == '"', text[start] == '"'
	level := 1
	for i := start + 1; i < len(text); i++ {
		ch := text[i]
		if inString {
			if ch == '\\' {
				i++
			} else if ch == '"' {
				inString = false
				if quoted {
					return i + 1, true, false
				}
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{', '[':
			level++
			tooDeep = tooDeep || level > maxSecretJSONDepth
		case '}', ']':
			level--
			if level == 0 {
				return i + 1, true, tooDeep
			}
		}
	}
	return len(text), false, tooDeep
}

func secretJSONCandidate(text string, start int) bool {
	if text[start] == '"' {
		return true
	}
	if text[start] == '[' && start > 0 && text[start-1] == '\x1b' {
		return false
	}
	i := start + 1
	for i < len(text) && strings.ContainsRune(" \t\r\n", rune(text[i])) {
		i++
	}
	if i == len(text) {
		return false
	}
	if text[start] == '{' {
		return text[i] == '"' || text[i] == '}'
	}
	return strings.ContainsRune(`"{[]-0123456789tfn`, rune(text[i]))
}

func redactEmbeddedSecretJSON(text string, depth int) string {
	if depth > maxSecretJSONDepth {
		return redactedSecret
	}
	var output strings.Builder
	plainStart := 0
	for i := 0; i < len(text); {
		if text[i] != '{' && text[i] != '[' && text[i] != '"' {
			i++
			continue
		}
		if strings.HasPrefix(text[i:], redactedSecret) {
			i += len(redactedSecret)
			continue
		}
		sensitiveAssignment := secretStructuredAssignment(text, i)
		if !sensitiveAssignment && !secretJSONCandidate(text, i) {
			i++
			continue
		}
		end, complete, tooDeep := secretJSONSpan(text, i)
		fragment := text[i:end]
		if text[i] == '"' && !strings.ContainsAny(fragment, "{[") {
			i = end
			continue
		}
		var value any
		changed, replacement := false, ""
		if sensitiveAssignment {
			changed, replacement = true, `"`+redactedSecret+`"`
		} else if complete && !tooDeep && len(fragment) <= maxSecretJSONBytes && DecodeJsonStrict(strings.NewReader(fragment), &value) == nil {
			if result, redacted := redactStructuredSecrets(value, depth); redacted {
				if encoded, err := Marshal(result); err == nil {
					changed, replacement = true, string(encoded)
				}
			}
		} else if redactSecretText(fragment) != fragment || secretEnvironmentAssignment.MatchString(fragment) || secretPluralContainerAssignment.MatchString(fragment) {
			// Malformed or over-budget sensitive containers fail closed.
			changed, replacement = true, `"`+redactedSecret+`"`
		}
		if changed {
			output.WriteString(redactSecretText(text[plainStart:i]))
			output.WriteString(replacement)
			plainStart = end
		}
		i = end
	}
	output.WriteString(redactSecretText(text[plainStart:]))
	return output.String()
}

func redactStructuredSecrets(value any, depth int) (any, bool) {
	if depth > maxSecretJSONDepth {
		return redactedSecret, true
	}
	changed := false
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			_, object := item.(map[string]any)
			_, array := item.([]any)
			if secretCredentialField(key) || secretEnvironmentField(key) && (object || array) {
				if item != redactedSecret {
					v[key] = redactedSecret
					changed = true
				}
				continue
			}
			next, itemChanged := redactStructuredSecrets(item, depth+1)
			v[key], changed = next, changed || itemChanged
		}
	case []any:
		for index, item := range v {
			next, itemChanged := redactStructuredSecrets(item, depth+1)
			v[index], changed = next, changed || itemChanged
		}
	case string:
		next := redactEmbeddedSecretJSON(v, depth+1)
		return next, next != v
	}
	return value, changed
}

func redactSecretText(text string) string {
	text = secretPrivateKey.ReplaceAllString(text, redactedSecret)
	text = redactAssignment(secretAuthorization, text)
	text = redactAssignment(secretCookie, text)
	text = redactAssignment(secretAssignment, text)
	text = redactAssignment(secretSignature, text)
	text = secretBearer.ReplaceAllString(text, "Bearer "+redactedSecret)
	text = secretURLUser.ReplaceAllString(text, "${1}"+redactedSecret+"@")
	return secretToken.ReplaceAllString(text, redactedSecret)
}
