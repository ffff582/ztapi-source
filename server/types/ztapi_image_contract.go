package types

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

const (
	ZTAPIImageProtocolContractVersion           = uint64(1)
	ZTAPIImageProtocolContractVersionV2         = uint64(2)
	ZTAPIImageEvidenceVersion                   = uint64(1)
	ZTAPIImageEndpointGeneration                = "images_generation"
	ZTAPIResponseIDSourceBodyField              = "body_field"
	ZTAPIResponseIDSourceHeader                 = "header"
	ZTAPIImageRequestFieldRequired              = "required"
	ZTAPIImageRequestFieldOptional              = "optional"
	ZTAPIImageRequestFieldOmit                  = "omit"
	ZTAPIImageWireProtocolOpenAIImages          = "openai_images"
	ZTAPIImageWireProtocolGeminiGenerateContent = "gemini_generate_content"
	ZTAPIImageResponseSchemaGeminiInlineImages  = "gemini_generate_content_inline_images"
)

type ZTAPIImageProtocolContract struct {
	Version               uint64                           `json:"version"`
	ProviderModel         string                           `json:"provider_model"`
	EndpointType          string                           `json:"endpoint_type"`
	Method                string                           `json:"method"`
	Path                  string                           `json:"path"`
	WireProtocol          string                           `json:"wire_protocol,omitempty"`
	ProviderPath          string                           `json:"provider_path,omitempty"`
	Capabilities          ZTAPIImageCapabilities           `json:"capabilities"`
	Response              ZTAPIImageResponseContract       `json:"response"`
	Usage                 ZTAPIImageUsageContract          `json:"usage"`
	Reservations          []ZTAPIImageReservationAuthority `json:"reservations"`
	RequestIDField        string                           `json:"request_id_field,omitempty"`
	RequestIDSource       string                           `json:"request_id_source,omitempty"`
	RequestIDKey          string                           `json:"request_id_key,omitempty"`
	EvidenceVersion       uint64                           `json:"evidence_version"`
	EvidenceHash          string                           `json:"evidence_hash,omitempty"`
	UpstreamRequestFields map[string]string                `json:"upstream_request_fields,omitempty"`
}

type ZTAPIImageCapabilities struct {
	Sizes           []string `json:"sizes"`
	Qualities       []string `json:"qualities"`
	ResponseFormats []string `json:"response_formats"`
	MinCount        int      `json:"min_count"`
	MaxCount        int      `json:"max_count"`
	SupportsEdits   bool     `json:"supports_edits"`
}

type ZTAPIImageResponseContract struct {
	Schema       string            `json:"schema"`
	ResultsField string            `json:"results_field"`
	ResultFields map[string]string `json:"result_fields"`
}

type ZTAPIImageUsageContract struct {
	UsageField     string            `json:"usage_field"`
	Fields         map[string]string `json:"fields"`
	TotalField     string            `json:"total_field"`
	TotalSemantics string            `json:"total_semantics"`
	CacheSemantics string            `json:"cache_semantics"`
}

type ZTAPIImageSelector struct {
	Size           string `json:"size"`
	Quality        string `json:"quality"`
	ResponseFormat string `json:"response_format"`
	N              int    `json:"n"`
}

type ZTAPIImageReservationAuthority struct {
	Size              string            `json:"size"`
	Quality           string            `json:"quality"`
	ResponseFormat    string            `json:"response_format"`
	N                 int               `json:"n"`
	MaximumDimensions map[string]string `json:"maximum_dimensions"`
}

var (
	ztapiImageProviderModelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`)
	ztapiImageOptionPattern        = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	ztapiImageSizePattern          = regexp.MustCompile(`^(auto|[1-9][0-9]{1,4}x[1-9][0-9]{1,4})$`)
	ztapiImageJSONFieldPattern     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)
	ztapiImageHashPattern          = regexp.MustCompile(`^[a-f0-9]{64}$`)
	ztapiResponseIDHeaderPattern   = regexp.MustCompile("^[!#$%&'*+\\-.^_`|~0-9A-Za-z]+$")
)

func (contract ZTAPIImageProtocolContract) Clone() ZTAPIImageProtocolContract {
	contract.UpstreamRequestFields = cloneZTAPIImageStringMap(contract.UpstreamRequestFields)
	contract.Capabilities.Sizes = append([]string(nil), contract.Capabilities.Sizes...)
	contract.Capabilities.Qualities = append([]string(nil), contract.Capabilities.Qualities...)
	contract.Capabilities.ResponseFormats = append([]string(nil), contract.Capabilities.ResponseFormats...)
	contract.Response.ResultFields = cloneZTAPIImageStringMap(contract.Response.ResultFields)
	contract.Usage.Fields = cloneZTAPIImageStringMap(contract.Usage.Fields)
	contract.Reservations = append([]ZTAPIImageReservationAuthority(nil), contract.Reservations...)
	for index := range contract.Reservations {
		contract.Reservations[index].MaximumDimensions = cloneZTAPIImageStringMap(contract.Reservations[index].MaximumDimensions)
	}
	return contract
}

func (contract ZTAPIImageProtocolContract) FindReservationAuthority(selector ZTAPIImageSelector) (ZTAPIImageReservationAuthority, bool) {
	for _, reservation := range contract.Reservations {
		if reservation.Size == selector.Size && reservation.Quality == selector.Quality &&
			reservation.ResponseFormat == selector.ResponseFormat && reservation.N == selector.N {
			reservation.MaximumDimensions = cloneZTAPIImageStringMap(reservation.MaximumDimensions)
			return reservation, true
		}
	}
	return ZTAPIImageReservationAuthority{}, false
}

func SealZTAPIImageProtocolContract(contract ZTAPIImageProtocolContract) (ZTAPIImageProtocolContract, string, error) {
	contract = contract.Clone()
	contract.EvidenceHash = ""
	if err := normalizeAndValidateZTAPIImageProtocolContract(&contract); err != nil {
		return ZTAPIImageProtocolContract{}, "", err
	}
	payload, err := common.Marshal(contract)
	if err != nil {
		return ZTAPIImageProtocolContract{}, "", err
	}
	contract.EvidenceHash = fmt.Sprintf("%x", sha256.Sum256(payload))
	canonical, err := common.Marshal(contract)
	if err != nil {
		return ZTAPIImageProtocolContract{}, "", err
	}
	return contract.Clone(), string(canonical), nil
}

func ParseZTAPIImageProtocolContract(raw string) (ZTAPIImageProtocolContract, string, error) {
	var contract ZTAPIImageProtocolContract
	if strings.TrimSpace(raw) == "" {
		return contract, "", errors.New("image protocol contract is required")
	}
	if err := common.RejectDuplicateJsonObjectMembers(strings.NewReader(raw)); err != nil {
		var duplicate *common.DuplicateJsonObjectMemberError
		if errors.As(err, &duplicate) {
			return contract, "", fmt.Errorf("duplicate image protocol contract field %q", duplicate.Name)
		}
		return contract, "", errors.New("image protocol contract must contain one valid JSON value")
	}
	if err := validateZTAPIImageJSONFields([]byte(raw)); err != nil {
		return contract, "", err
	}
	if err := common.UnmarshalJsonStr(raw, &contract); err != nil {
		return contract, "", errors.New("image protocol contract must be valid JSON")
	}
	wantHash := contract.EvidenceHash
	if !ztapiImageHashPattern.MatchString(wantHash) {
		return contract, "", errors.New("image protocol contract evidence hash is invalid")
	}
	sealed, canonical, err := SealZTAPIImageProtocolContract(contract)
	if err != nil {
		return contract, "", err
	}
	if sealed.EvidenceHash != wantHash {
		return contract, "", errors.New("image protocol contract evidence hash mismatch")
	}
	return sealed.Clone(), canonical, nil
}

func normalizeAndValidateZTAPIImageProtocolContract(contract *ZTAPIImageProtocolContract) error {
	if contract == nil || (contract.Version != ZTAPIImageProtocolContractVersion && contract.Version != ZTAPIImageProtocolContractVersionV2) || contract.EvidenceVersion != ZTAPIImageEvidenceVersion {
		return errors.New("unsupported image protocol or evidence version")
	}
	if contract.ProviderModel != strings.TrimSpace(contract.ProviderModel) || !ztapiImageProviderModelPattern.MatchString(contract.ProviderModel) || len(contract.ProviderModel) > 255 {
		return errors.New("image protocol provider model is invalid")
	}
	if contract.EndpointType != ZTAPIImageEndpointGeneration || contract.Method != "POST" || contract.Path != "/v1/images/generations" || contract.Capabilities.SupportsEdits {
		return errors.New("unsupported image endpoint, method, path, or edit capability")
	}
	if err := normalizeZTAPIImageCapabilities(&contract.Capabilities); err != nil {
		return err
	}
	if err := validateZTAPIImageRequestFieldPolicy(contract); err != nil {
		return err
	}
	if err := validateZTAPIImageDispatchContract(contract); err != nil {
		return err
	}
	if (contract.Response.Schema != "object_results_array" && contract.Response.Schema != ZTAPIImageResponseSchemaGeminiInlineImages) ||
		!validZTAPIImageField(contract.Response.ResultsField) || len(contract.Response.ResultFields) == 0 {
		return errors.New("image response schema or results field is invalid")
	}
	if err := validateZTAPIImageFieldMap(contract.Response.ResultFields, map[string]bool{"url": true, "b64_json": true}); err != nil {
		return fmt.Errorf("image result fields: %w", err)
	}
	if !ztapiImageResultFormatsMatch(contract.Capabilities.ResponseFormats, contract.Response.ResultFields) {
		return errors.New("image response result fields must exactly match supported response formats")
	}
	if !validZTAPIImageField(contract.Usage.UsageField) || !validZTAPIImageField(contract.Usage.TotalField) || len(contract.Usage.Fields) == 0 {
		return errors.New("image usage fields are invalid")
	}
	usageDimensions := map[string]bool{
		"input_tokens": true, "output_tokens": true, "cached_input_tokens": true,
		"text_input": true, "text_cached_input": true, "image_input": true,
		"image_cached_input": true, "image_output": true,
	}
	if err := validateZTAPIImageFieldMap(contract.Usage.Fields, usageDimensions); err != nil {
		return fmt.Errorf("image usage fields: %w", err)
	}
	if contract.Usage.TotalSemantics != "sum_of_dimensions" {
		return errors.New("unsupported image usage total semantics")
	}
	hasCache, hasInput := false, false
	for dimension := range contract.Usage.Fields {
		hasCache = hasCache || strings.Contains(dimension, "cached")
		hasInput = hasInput || strings.Contains(dimension, "input")
	}
	switch contract.Usage.CacheSemantics {
	case "not_reported":
		if hasCache {
			return errors.New("cache fields overlap not-reported cache semantics")
		}
	case "included_in_input", "separate_dimension":
		if !hasCache || !hasInput {
			return errors.New("cache semantics require exact cache and input fields")
		}
	default:
		return errors.New("unsupported image usage cache semantics")
	}
	if err := normalizeAndValidateZTAPIImageReservations(contract); err != nil {
		return err
	}
	requestIDField, err := validateZTAPIImageResponseIDSource(contract)
	if err != nil {
		return err
	}
	rootFields := []string{contract.Response.ResultsField, contract.Usage.UsageField}
	if requestIDField != "" {
		rootFields = append(rootFields, requestIDField)
	}
	if hasOverlappingZTAPIImageFields(rootFields) || (requestIDField != "" && ztapiImageFieldsOverlap(contract.Usage.TotalField, requestIDField)) {
		return errors.New("image protocol fields are ambiguous or overlapping")
	}
	for _, field := range contract.Usage.Fields {
		if ztapiImageFieldsOverlap(field, contract.Usage.TotalField) {
			return errors.New("image usage total field overlaps a dimension field")
		}
	}
	return nil
}

func validateZTAPIImageResponseIDSource(contract *ZTAPIImageProtocolContract) (string, error) {
	if contract.Version == ZTAPIImageProtocolContractVersion {
		if !validZTAPIImageField(contract.RequestIDField) || contract.RequestIDSource != "" || contract.RequestIDKey != "" {
			return "", errors.New("image response-ID source is invalid")
		}
		return contract.RequestIDField, nil
	}
	if contract.RequestIDField != "" {
		return "", errors.New("image response-ID source is invalid")
	}
	switch contract.RequestIDSource {
	case ZTAPIResponseIDSourceBodyField:
		if !validZTAPIImageField(contract.RequestIDKey) {
			return "", errors.New("image response-ID source is invalid")
		}
		return contract.RequestIDKey, nil
	case ZTAPIResponseIDSourceHeader:
		if !validZTAPIResponseIDHeader(contract.RequestIDKey) {
			return "", errors.New("image response-ID source is invalid")
		}
		return "", nil
	default:
		return "", errors.New("image response-ID source is invalid")
	}
}

func normalizeAndValidateZTAPIImageReservations(contract *ZTAPIImageProtocolContract) error {
	want := len(contract.Capabilities.Sizes) * len(contract.Capabilities.Qualities) *
		len(contract.Capabilities.ResponseFormats) * (contract.Capabilities.MaxCount - contract.Capabilities.MinCount + 1)
	if want <= 0 || len(contract.Reservations) != want {
		return errors.New("image reservation authority must cover every admitted selector exactly once")
	}
	allowedDimensions := make(map[string]struct{}, len(contract.Usage.Fields))
	for dimension := range contract.Usage.Fields {
		allowedDimensions[dimension] = struct{}{}
	}
	seen := make(map[string]struct{}, want)
	for index := range contract.Reservations {
		entry := &contract.Reservations[index]
		if !containsZTAPIImageString(contract.Capabilities.Sizes, entry.Size) ||
			!containsZTAPIImageString(contract.Capabilities.Qualities, entry.Quality) ||
			!containsZTAPIImageString(contract.Capabilities.ResponseFormats, entry.ResponseFormat) ||
			entry.N < contract.Capabilities.MinCount || entry.N > contract.Capabilities.MaxCount {
			return errors.New("image reservation authority contains an unadmitted selector")
		}
		key := ztapiImageSelectorKey(entry.Size, entry.Quality, entry.ResponseFormat, entry.N)
		if _, exists := seen[key]; exists {
			return errors.New("image reservation authority contains a duplicate selector")
		}
		seen[key] = struct{}{}
		if len(entry.MaximumDimensions) != len(allowedDimensions) {
			return errors.New("image reservation maximum dimensions must exactly match usage dimensions")
		}
		positive := false
		for dimension := range allowedDimensions {
			raw, ok := entry.MaximumDimensions[dimension]
			if !ok {
				return errors.New("image reservation maximum dimensions must exactly match usage dimensions")
			}
			value, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || value < 0 || strconv.FormatInt(value, 10) != raw {
				return fmt.Errorf("image reservation maximum dimension %q is invalid", dimension)
			}
			positive = positive || value > 0
		}
		if !positive {
			return errors.New("image reservation maximum dimensions must contain a positive bound")
		}
	}
	sort.Slice(contract.Reservations, func(i, j int) bool {
		left, right := contract.Reservations[i], contract.Reservations[j]
		return ztapiImageSelectorKey(left.Size, left.Quality, left.ResponseFormat, left.N) <
			ztapiImageSelectorKey(right.Size, right.Quality, right.ResponseFormat, right.N)
	})
	return nil
}

func ztapiImageSelectorKey(size, quality, responseFormat string, n int) string {
	return size + "\x00" + quality + "\x00" + responseFormat + "\x00" + strconv.Itoa(n)
}

func containsZTAPIImageString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func ztapiImageResultFormatsMatch(formats []string, fields map[string]string) bool {
	if len(formats) != len(fields) {
		return false
	}
	for _, format := range formats {
		if _, ok := fields[format]; !ok {
			return false
		}
	}
	return true
}

func normalizeZTAPIImageCapabilities(capabilities *ZTAPIImageCapabilities) error {
	if capabilities.MinCount < 1 || capabilities.MaxCount < capabilities.MinCount || capabilities.MaxCount > 10 {
		return errors.New("image count range is invalid")
	}
	validate := func(values []string, allowed func(string) bool) error {
		if len(values) == 0 || hasDuplicateZTAPIImageStrings(values) {
			return errors.New("image capability values must be non-empty and unique")
		}
		for _, value := range values {
			if value != strings.TrimSpace(value) || !allowed(value) {
				return fmt.Errorf("unsupported image capability value %q", value)
			}
		}
		sort.Strings(values)
		return nil
	}
	if err := validate(capabilities.Sizes, ztapiImageSizePattern.MatchString); err != nil {
		return err
	}
	if err := validate(capabilities.Qualities, ztapiImageOptionPattern.MatchString); err != nil {
		return err
	}
	if err := validate(capabilities.ResponseFormats, func(value string) bool { return value == "url" || value == "b64_json" }); err != nil {
		return err
	}
	return nil
}

func validateZTAPIImageFieldMap(fields map[string]string, allowed map[string]bool) error {
	values := make([]string, 0, len(fields))
	for name, field := range fields {
		if !allowed[name] || !validZTAPIImageField(field) {
			return fmt.Errorf("unsupported field binding %q", name)
		}
		values = append(values, field)
	}
	if hasOverlappingZTAPIImageFields(values) {
		return errors.New("field bindings overlap")
	}
	return nil
}

func validateZTAPIImageRequestFieldPolicy(contract *ZTAPIImageProtocolContract) error {
	if contract.UpstreamRequestFields == nil {
		if contract.Version == ZTAPIImageProtocolContractVersionV2 &&
			(contract.ProviderModel == "gpt-image-2" || contract.WireProtocol == ZTAPIImageWireProtocolGeminiGenerateContent) {
			return errors.New("provider-specific upstream image request policy is required")
		}
		return nil
	}
	if contract.Version != ZTAPIImageProtocolContractVersionV2 || len(contract.UpstreamRequestFields) != 6 {
		return errors.New("upstream image request policy requires V2 and all six admitted fields")
	}
	for _, field := range []string{"model", "prompt", "n", "size", "quality", "response_format"} {
		policy := contract.UpstreamRequestFields[field]
		switch policy {
		case ZTAPIImageRequestFieldRequired, ZTAPIImageRequestFieldOptional, ZTAPIImageRequestFieldOmit:
		default:
			return fmt.Errorf("invalid upstream image request policy for %q", field)
		}
		if contract.WireProtocol != ZTAPIImageWireProtocolGeminiGenerateContent &&
			(field == "model" || field == "prompt" || field == "n") && policy != ZTAPIImageRequestFieldRequired {
			return fmt.Errorf("upstream image request field %q must remain required", field)
		}
	}
	if contract.ProviderModel == "gpt-image-2" && contract.UpstreamRequestFields["response_format"] != ZTAPIImageRequestFieldOmit {
		return errors.New("gpt-image-2 upstream response_format policy must be omit")
	}
	if contract.WireProtocol == ZTAPIImageWireProtocolGeminiGenerateContent {
		want := map[string]string{
			"model": ZTAPIImageRequestFieldOmit, "prompt": ZTAPIImageRequestFieldRequired,
			"n": ZTAPIImageRequestFieldOmit, "size": ZTAPIImageRequestFieldOmit,
			"quality": ZTAPIImageRequestFieldOmit, "response_format": ZTAPIImageRequestFieldOmit,
		}
		for field, policy := range want {
			if contract.UpstreamRequestFields[field] != policy {
				return fmt.Errorf("Gemini native image request field %q must be %s", field, policy)
			}
		}
	}
	return nil
}

func validateZTAPIImageDispatchContract(contract *ZTAPIImageProtocolContract) error {
	if contract == nil {
		return errors.New("image dispatch contract is required")
	}
	switch contract.WireProtocol {
	case "":
		if contract.ProviderPath != "" {
			return errors.New("image provider path requires an explicit wire protocol")
		}
		return nil
	case ZTAPIImageWireProtocolOpenAIImages:
		if contract.Version != ZTAPIImageProtocolContractVersionV2 || contract.ProviderPath != contract.Path {
			return errors.New("OpenAI image wire protocol requires the frozen public provider path")
		}
		return nil
	case ZTAPIImageWireProtocolGeminiGenerateContent:
		if contract.Version != ZTAPIImageProtocolContractVersionV2 || contract.ProviderModel != "gemini-2.5-flash-image" ||
			contract.ProviderPath != "/v1beta/models/gemini-2.5-flash-image:generateContent" {
			return errors.New("unsupported Gemini native image binding")
		}
		if contract.Capabilities.MinCount != 1 || contract.Capabilities.MaxCount != 1 ||
			len(contract.Capabilities.ResponseFormats) != 1 || contract.Capabilities.ResponseFormats[0] != "b64_json" {
			return errors.New("Gemini native image binding requires one inline base64 result")
		}
		if contract.Response.Schema != ZTAPIImageResponseSchemaGeminiInlineImages || contract.Response.ResultsField != "candidates" ||
			len(contract.Response.ResultFields) != 1 || contract.Response.ResultFields["b64_json"] != "content.parts.inlineData.data" {
			return errors.New("Gemini native image response binding is invalid")
		}
		if contract.Usage.UsageField != "usageMetadata" || contract.Usage.TotalField != "totalTokenCount" ||
			len(contract.Usage.Fields) != 2 || contract.Usage.Fields["input_tokens"] != "promptTokenCount" ||
			contract.Usage.Fields["output_tokens"] != "candidatesTokenCount" || contract.Usage.CacheSemantics != "not_reported" {
			return errors.New("Gemini native image usage binding is invalid")
		}
		return nil
	default:
		return errors.New("unsupported image wire protocol")
	}
}

func validateZTAPIImageJSONFields(raw []byte) error {
	var root map[string]json.RawMessage
	if common.Unmarshal(raw, &root) != nil {
		return errors.New("image protocol contract must be a JSON object")
	}
	var version uint64
	if common.Unmarshal(root["version"], &version) != nil {
		return errors.New("image protocol contract version is invalid")
	}
	baseFields := []string{"version", "provider_model", "endpoint_type", "method", "path", "capabilities", "response", "usage", "reservations", "evidence_version", "evidence_hash"}
	switch version {
	case ZTAPIImageProtocolContractVersion:
		if err := exactZTAPIImageJSONFields(root, append(baseFields, "request_id_field")...); err != nil {
			return err
		}
	case ZTAPIImageProtocolContractVersionV2:
		if policy, exists := root["upstream_request_fields"]; exists {
			var fields map[string]string
			if common.Unmarshal(policy, &fields) != nil || fields == nil {
				return errors.New("upstream image request policy must be an object")
			}
			baseFields = append(baseFields, "upstream_request_fields")
		}
		if _, exists := root["request_id_field"]; exists {
			return fmt.Errorf("image response-ID source contains forbidden field %q", "request_id_field")
		}
		for _, field := range []string{"request_id_source", "request_id_key"} {
			if _, exists := root[field]; !exists {
				return fmt.Errorf("image response-ID source is missing field %q", field)
			}
		}
		_, hasWireProtocol := root["wire_protocol"]
		_, hasProviderPath := root["provider_path"]
		if hasWireProtocol != hasProviderPath {
			return errors.New("image wire protocol and provider path must be frozen together")
		}
		if hasWireProtocol {
			baseFields = append(baseFields, "wire_protocol", "provider_path")
		}
		if err := exactZTAPIImageJSONFields(root, append(baseFields, "request_id_source", "request_id_key")...); err != nil {
			return err
		}
	default:
		return errors.New("unsupported image protocol or evidence version")
	}
	var capabilities map[string]json.RawMessage
	if common.Unmarshal(root["capabilities"], &capabilities) != nil {
		return errors.New("image capabilities must be an object")
	}
	if err := exactZTAPIImageJSONFields(capabilities, "sizes", "qualities", "response_formats", "min_count", "max_count", "supports_edits"); err != nil {
		return err
	}
	var response map[string]json.RawMessage
	if common.Unmarshal(root["response"], &response) != nil {
		return errors.New("image response contract must be an object")
	}
	if err := exactZTAPIImageJSONFields(response, "schema", "results_field", "result_fields"); err != nil {
		return err
	}
	var usage map[string]json.RawMessage
	if common.Unmarshal(root["usage"], &usage) != nil {
		return errors.New("image usage contract must be an object")
	}
	if err := exactZTAPIImageJSONFields(usage, "usage_field", "fields", "total_field", "total_semantics", "cache_semantics"); err != nil {
		return err
	}
	var reservations []map[string]json.RawMessage
	if common.Unmarshal(root["reservations"], &reservations) != nil || reservations == nil {
		return errors.New("image reservation authority must be an array")
	}
	for _, reservation := range reservations {
		if err := exactZTAPIImageJSONFields(reservation, "size", "quality", "response_format", "n", "maximum_dimensions"); err != nil {
			return err
		}
	}
	return nil
}

func exactZTAPIImageJSONFields(object map[string]json.RawMessage, fields ...string) error {
	allowed := make(map[string]bool, len(fields))
	for _, field := range fields {
		allowed[field] = true
	}
	for field := range object {
		if !allowed[field] {
			return fmt.Errorf("unknown image protocol contract field %q", field)
		}
	}
	for _, field := range fields {
		if _, ok := object[field]; !ok {
			return fmt.Errorf("missing image protocol contract field %q", field)
		}
	}
	return nil
}

func validZTAPIImageField(value string) bool {
	return value == strings.TrimSpace(value) && ztapiImageJSONFieldPattern.MatchString(value)
}

func validZTAPIResponseIDHeader(value string) bool {
	return ztapiResponseIDHeaderPattern.MatchString(value)
}

func hasDuplicateZTAPIImageStrings(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			return true
		}
		seen[value] = struct{}{}
	}
	return false
}

func hasOverlappingZTAPIImageFields(values []string) bool {
	for i := range values {
		for j := i + 1; j < len(values); j++ {
			if ztapiImageFieldsOverlap(values[i], values[j]) {
				return true
			}
		}
	}
	return false
}

func ztapiImageFieldsOverlap(left, right string) bool {
	return left == right || strings.HasPrefix(left, right+".") || strings.HasPrefix(right, left+".")
}

func cloneZTAPIImageStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
