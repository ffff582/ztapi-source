package relay

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/types"
)

type ZTAPIImageDiagnosticConfig struct {
	ExpectedOrigin   string
	Endpoint         string
	KeyEnvironment   string
	Contract         types.ZTAPIImageProtocolContract
	Request          dto.ImageRequest
	Client           *http.Client
	EvidenceWriter   io.Writer
	MaxCaptureBytes  int64
	MaxMetadataBytes int
}

type ZTAPIImageDiagnosticRecord struct {
	RecordedAt            string            `json:"recorded_at"`
	Endpoint              string            `json:"endpoint"`
	StatusCode            int               `json:"status_code"`
	RequestHeaders        map[string]string `json:"request_headers"`
	ResponseHeaders       map[string]string `json:"response_headers"`
	RequestBodySHA256     string            `json:"request_body_sha256"`
	ResponseCaptureSHA256 string            `json:"response_capture_sha256"`
	ResponseBytesRead     int64             `json:"response_bytes_read"`
	ResponseCapturedBytes int64             `json:"response_captured_bytes"`
	ResponseBodyComplete  bool              `json:"response_body_complete"`
	UpstreamRequestID     string            `json:"upstream_request_id,omitempty"`
	SanitizedMetadata     string            `json:"sanitized_metadata"`
	MetadataWasTruncated  bool              `json:"metadata_was_truncated"`
}

// RunZTAPIImageDiagnostic executes only when explicitly called. It has no
// manifest or database dependency and receives credentials only by environment
// variable name.
func RunZTAPIImageDiagnostic(ctx context.Context, config ZTAPIImageDiagnosticConfig) (ZTAPIImageDiagnosticRecord, error) {
	var record ZTAPIImageDiagnosticRecord
	if config.Client == nil || config.EvidenceWriter == nil {
		return record, errors.New("diagnostic client and evidence writer are required")
	}
	if config.MaxCaptureBytes <= 0 || config.MaxCaptureBytes == int64(^uint64(0)>>1) || config.MaxMetadataBytes <= 0 {
		return record, errors.New("valid positive diagnostic capture limits are required")
	}
	sealed, _, err := types.SealZTAPIImageProtocolContract(config.Contract)
	if err != nil || sealed.EvidenceHash != config.Contract.EvidenceHash {
		return record, errors.New("diagnostic image contract is invalid")
	}
	endpoint, err := validateZTAPIImageDiagnosticTarget(config.ExpectedOrigin, config.Endpoint, sealed.Path)
	if err != nil {
		return record, err
	}
	if err := validateZTAPIImageDiagnosticRequest(config.Request, sealed); err != nil {
		return record, err
	}
	key, ok := os.LookupEnv(strings.TrimSpace(config.KeyEnvironment))
	if !ok || strings.TrimSpace(key) == "" {
		return record, fmt.Errorf("diagnostic key environment %q is not set", config.KeyEnvironment)
	}

	request := config.Request
	request.Model = sealed.ProviderModel
	request.Extra = nil
	requestBody, err := common.Marshal(request)
	if err != nil {
		return record, fmt.Errorf("marshal diagnostic request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, sealed.Method, config.Endpoint, bytes.NewReader(requestBody))
	if err != nil {
		return record, fmt.Errorf("create diagnostic request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	client := *config.Client
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("diagnostic redirects are not allowed")
	}
	resp, err := client.Do(req)
	if err != nil {
		return record, fmt.Errorf("execute diagnostic request: %w", err)
	}
	defer resp.Body.Close()

	observed, err := io.ReadAll(io.LimitReader(resp.Body, config.MaxCaptureBytes+1))
	if err != nil {
		return record, fmt.Errorf("read diagnostic response: %w", err)
	}
	bodyComplete := int64(len(observed)) <= config.MaxCaptureBytes
	captured := observed
	if !bodyComplete {
		captured = observed[:config.MaxCaptureBytes]
	}
	metadata, requestID, metadataTruncated := sanitizeZTAPIImageDiagnosticMetadata(captured, !bodyComplete, sealed, key, config.MaxMetadataBytes)
	record = ZTAPIImageDiagnosticRecord{
		RecordedAt:            time.Now().UTC().Format(time.RFC3339Nano),
		Endpoint:              sanitizedZTAPIImageDiagnosticEndpoint(endpoint),
		StatusCode:            resp.StatusCode,
		RequestHeaders:        map[string]string{"Authorization": "<redacted>", "Content-Type": "application/json"},
		ResponseHeaders:       sanitizeZTAPIDiagnosticHeaders(resp.Header),
		RequestBodySHA256:     fmt.Sprintf("%x", sha256.Sum256(requestBody)),
		ResponseCaptureSHA256: fmt.Sprintf("%x", sha256.Sum256(captured)),
		ResponseBytesRead:     int64(len(observed)),
		ResponseCapturedBytes: int64(len(captured)),
		ResponseBodyComplete:  bodyComplete,
		UpstreamRequestID:     requestID,
		SanitizedMetadata:     metadata,
		MetadataWasTruncated:  metadataTruncated,
	}
	encoded, err := common.Marshal(record)
	if err != nil {
		return ZTAPIImageDiagnosticRecord{}, fmt.Errorf("marshal diagnostic evidence: %w", err)
	}
	evidence := append(encoded, '\n')
	written, err := config.EvidenceWriter.Write(evidence)
	if err == nil && written != len(evidence) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return ZTAPIImageDiagnosticRecord{}, fmt.Errorf("write diagnostic evidence: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return record, fmt.Errorf("diagnostic upstream returned HTTP %d", resp.StatusCode)
	}
	return record, nil
}

func validateZTAPIImageDiagnosticTarget(expectedOrigin, endpointValue, frozenPath string) (*url.URL, error) {
	origin, err := url.Parse(expectedOrigin)
	if err != nil || (origin.Scheme != "http" && origin.Scheme != "https") || origin.Host == "" || origin.User != nil ||
		(origin.Path != "" && origin.Path != "/") || origin.RawPath != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return nil, errors.New("diagnostic expected origin must contain only scheme and host")
	}
	if origin.Scheme == "http" && !isLiteralZTAPILoopback(origin.Hostname()) {
		return nil, errors.New("diagnostic remote origins require HTTPS")
	}
	endpoint, err := url.Parse(endpointValue)
	if err != nil || endpoint.Scheme != origin.Scheme || endpoint.Host != origin.Host || endpoint.User != nil || endpoint.Path != frozenPath ||
		endpoint.RawPath != "" || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, errors.New("diagnostic endpoint must match the exact expected origin and frozen image path without decorations")
	}
	return endpoint, nil
}

func isLiteralZTAPILoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.Equal(net.IPv6loopback) {
		return true
	}
	if strings.Contains(host, ":") {
		return false
	}
	ipv4 := ip.To4()
	return ipv4 != nil && ipv4[0] == 127 && strings.Contains(host, ".")
}

func sanitizeZTAPIDiagnosticHeaders(headers http.Header) map[string]string {
	result := make(map[string]string)
	count := 0
	for name, values := range headers {
		if count >= 32 {
			break
		}
		canonicalName := http.CanonicalHeaderKey(name)
		if ztapiSensitiveMetadataKey(canonicalName) {
			result[canonicalName] = "<redacted>"
			count++
			continue
		}
		value := strings.Join(values, ",")
		switch strings.ToLower(canonicalName) {
		case "x-request-id", "request-id":
			result[canonicalName] = sanitizeZTAPIDiagnosticOpaqueValue(value)
		case "content-type":
			result[canonicalName] = sanitizeZTAPIDiagnosticContentType(value)
		default:
			result[canonicalName] = ztapiOmittedValue(value)
		}
		count++
	}
	return result
}

func validateZTAPIImageDiagnosticRequest(request dto.ImageRequest, contract types.ZTAPIImageProtocolContract) error {
	if len(request.Style) > 0 || len(request.User) > 0 || len(request.ExtraFields) > 0 || len(request.Background) > 0 ||
		len(request.Moderation) > 0 || len(request.OutputFormat) > 0 || len(request.OutputCompression) > 0 ||
		len(request.PartialImages) > 0 || len(request.Images) > 0 || len(request.Mask) > 0 || len(request.InputFidelity) > 0 ||
		request.Watermark != nil || len(request.WatermarkEnabled) > 0 || len(request.UserId) > 0 || len(request.Image) > 0 ||
		len(request.Extra) > 0 || request.Stream != nil && *request.Stream {
		return errors.New("diagnostic request contains fields outside the frozen image contract")
	}
	if strings.TrimSpace(request.Prompt) == "" || request.N == nil {
		return errors.New("diagnostic prompt and count are required")
	}
	count := int(*request.N)
	if count < contract.Capabilities.MinCount || count > contract.Capabilities.MaxCount ||
		!containsZTAPIImageCapability(contract.Capabilities.Sizes, request.Size) ||
		!containsZTAPIImageCapability(contract.Capabilities.Qualities, request.Quality) ||
		!containsZTAPIImageCapability(contract.Capabilities.ResponseFormats, request.ResponseFormat) {
		return errors.New("diagnostic request is outside the frozen image capabilities")
	}
	return nil
}

func sanitizeZTAPIImageDiagnosticMetadata(raw []byte, truncated bool, contract types.ZTAPIImageProtocolContract, key string, limit int) (string, string, bool) {
	if truncated {
		return truncateZTAPIDiagnosticMetadata("<response metadata omitted: capture limit exceeded>", limit), "", true
	}
	var root map[string]any
	if common.Unmarshal(raw, &root) != nil || root == nil {
		text := "<response metadata omitted: invalid JSON>"
		return truncateZTAPIDiagnosticMetadata(text, limit), "", len(text) > limit
	}
	requestID := ""
	requestIDReplacement := "<omitted request id>"
	value, requestIDPresent := ztapiImageField(root, contract.RequestIDField)
	if requestIDPresent {
		if rawRequestID, isString := value.(string); isString {
			requestID = sanitizeZTAPIDiagnosticOpaqueValue(rawRequestID)
			requestIDReplacement = requestID
		}
	}
	redactZTAPIImageResultFields(root, contract)
	sanitized := sanitizeZTAPIDiagnosticValue(root, "", key)
	if requestIDPresent {
		redactZTAPIDiagnosticPath(sanitized.(map[string]any), contract.RequestIDField, requestIDReplacement)
	}
	encoded, err := common.Marshal(sanitized)
	if err != nil {
		text := "<response metadata omitted: encoding failure>"
		return truncateZTAPIDiagnosticMetadata(text, limit), requestID, len(text) > limit
	}
	text := strings.ReplaceAll(string(encoded), key, "<redacted>")
	return truncateZTAPIDiagnosticMetadata(text, limit), requestID, len(text) > limit
}

func sanitizeZTAPIDiagnosticOpaqueValue(value string) string {
	return ztapiOmittedValue(value)
}

func redactZTAPIImageResultFields(root map[string]any, contract types.ZTAPIImageProtocolContract) {
	value, ok := ztapiImageField(root, contract.Response.ResultsField)
	results, okArray := value.([]any)
	if !ok || !okArray {
		return
	}
	for _, result := range results {
		object, ok := result.(map[string]any)
		if !ok {
			continue
		}
		for _, path := range contract.Response.ResultFields {
			redactZTAPIDiagnosticPath(object, path, "<omitted result>")
		}
	}
}

func redactZTAPIDiagnosticPath(root map[string]any, path, replacement string) {
	segments := strings.Split(path, ".")
	current := root
	for _, segment := range segments[:len(segments)-1] {
		next, ok := current[segment].(map[string]any)
		if !ok {
			return
		}
		current = next
	}
	leaf := segments[len(segments)-1]
	if _, ok := current[leaf]; ok {
		current[leaf] = replacement
	}
}

func sanitizeZTAPIDiagnosticValue(value any, fieldName, currentKey string) any {
	if ztapiSensitiveMetadataKey(fieldName) {
		return "<redacted>"
	}
	switch typed := value.(type) {
	case map[string]any:
		for childKey, child := range typed {
			typed[childKey] = sanitizeZTAPIDiagnosticValue(child, childKey, currentKey)
		}
		return typed
	case []any:
		for index := range typed {
			typed[index] = sanitizeZTAPIDiagnosticValue(typed[index], fieldName, currentKey)
		}
		return typed
	case string:
		if currentKey != "" && strings.Contains(typed, currentKey) {
			return ztapiOmittedValue(typed)
		}
		if ztapiAllowedDiagnosticProtocolEnum(fieldName, typed) {
			return typed
		}
		return ztapiOmittedValue(typed)
	}
	return value
}

func ztapiSensitiveMetadataKey(key string) bool {
	compact := strings.Map(func(character rune) rune {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			return character
		}
		if character >= 'A' && character <= 'Z' {
			return character + ('a' - 'A')
		}
		return -1
	}, strings.TrimSpace(key))
	if compact == "token" || strings.HasSuffix(compact, "token") || strings.HasSuffix(compact, "key") || compact == "jwt" {
		return true
	}
	for _, marker := range []string{"authorization", "apikey", "secret", "password", "passwd", "passphrase", "credential", "cookie", "privatekey", "signingkey", "sshkey"} {
		if strings.Contains(compact, marker) {
			return true
		}
	}
	return false
}

func ztapiAllowedDiagnosticProtocolEnum(fieldName, value string) bool {
	compact := strings.ToLower(strings.NewReplacer("_", "", "-", "", ".", "", " ", "").Replace(strings.TrimSpace(fieldName)))
	switch compact {
	case "status", "state":
		switch value {
		case "queued", "pending", "in_progress", "processing", "running", "completed", "succeeded", "failed", "cancelled", "canceled":
			return true
		}
	case "type":
		switch value {
		case "image_generation", "image_generation_call", "image_generation.partial_image", "image_generation.completed", "error":
			return true
		}
	case "code":
		switch value {
		case "invalid_request", "invalid_request_error", "rate_limit_exceeded", "server_error", "content_policy_violation":
			return true
		}
	case "object":
		switch value {
		case "list", "image", "image_generation", "error":
			return true
		}
	case "finishreason":
		switch value {
		case "stop", "length", "tool_calls", "content_filter":
			return true
		}
	case "role":
		switch value {
		case "assistant", "user", "system", "developer", "tool":
			return true
		}
	default:
	}
	return false
}

func sanitizeZTAPIDiagnosticContentType(value string) string {
	lower := strings.ToLower(strings.TrimSpace(strings.SplitN(value, ";", 2)[0]))
	switch lower {
	case "application/json", "text/event-stream":
		return lower
	default:
		return ztapiOmittedValue(value)
	}
}

func ztapiOmittedValue(value string) string {
	return fmt.Sprintf("<omitted sha256=%x bytes=%d>", sha256.Sum256([]byte(value)), len(value))
}

func truncateZTAPIDiagnosticMetadata(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	marker := "...<truncated>"
	if limit <= len(marker) {
		return marker[:limit]
	}
	return value[:limit-len(marker)] + marker
}

func sanitizedZTAPIImageDiagnosticEndpoint(endpoint *url.URL) string {
	copy := *endpoint
	copy.User = nil
	copy.RawQuery = ""
	copy.Fragment = ""
	return copy.String()
}
