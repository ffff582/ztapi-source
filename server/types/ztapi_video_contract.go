package types

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

const (
	ZTAPIVideoProtocolContractVersion   = uint64(1)
	ZTAPIVideoProtocolContractVersionV2 = uint64(2)
	ZTAPIVideoEvidenceVersion           = uint64(1)
)

type ZTAPIVideoProtocolContract struct {
	Version         uint64                           `json:"version"`
	Provider        string                           `json:"provider"`
	ProviderModel   string                           `json:"provider_model"`
	Auth            ZTAPIVideoAuthContract           `json:"auth"`
	Create          ZTAPIVideoEndpointContract       `json:"create"`
	Fetch           ZTAPIVideoEndpointContract       `json:"fetch"`
	Callback        ZTAPIVideoCallbackContract       `json:"callback"`
	Request         ZTAPIVideoRequestContract        `json:"request"`
	Capabilities    ZTAPIVideoCapabilities           `json:"capabilities"`
	States          ZTAPIVideoStateContract          `json:"states"`
	Result          ZTAPIVideoResultContract         `json:"result"`
	Usage           ZTAPIVideoUsageContract          `json:"usage"`
	Reservations    []ZTAPIVideoReservationAuthority `json:"reservations"`
	EvidenceVersion uint64                           `json:"evidence_version"`
	EvidenceHash    string                           `json:"evidence_hash,omitempty"`
}

type ZTAPIVideoAuthContract struct {
	Method string `json:"method"`
	Header string `json:"header"`
	Scheme string `json:"scheme"`
}

type ZTAPIVideoEndpointContract struct {
	Method          string `json:"method"`
	Path            string `json:"path"`
	TaskIDField     string `json:"task_id_field"`
	RequestIDField  string `json:"request_id_field,omitempty"`
	RequestIDSource string `json:"request_id_source,omitempty"`
	RequestIDKey    string `json:"request_id_key,omitempty"`
}

type ZTAPIVideoCallbackContract struct {
	Enabled         bool   `json:"enabled"`
	SignatureHeader string `json:"signature_header,omitempty"`
	SignatureMethod string `json:"signature_method,omitempty"`
}

type ZTAPIVideoRequestContract struct {
	ModelField      string `json:"model_field"`
	PromptField     string `json:"prompt_field"`
	ResolutionField string `json:"resolution_field"`
	DurationField   string `json:"duration_field"`
	VideoInputField string `json:"video_input_field,omitempty"`
}

type ZTAPIVideoCapabilities struct {
	Resolutions        []string `json:"resolutions"`
	DurationSeconds    []int    `json:"duration_seconds"`
	SupportsVideoInput bool     `json:"supports_video_input"`
}

type ZTAPIVideoStateContract struct {
	Field      string   `json:"field"`
	Accepted   []string `json:"accepted"`
	Processing []string `json:"processing"`
	Succeeded  []string `json:"succeeded"`
	Failed     []string `json:"failed"`
}

type ZTAPIVideoResultContract struct {
	URLField           string `json:"url_field"`
	ResolutionField    string `json:"resolution_field"`
	DurationField      string `json:"duration_field"`
	FailureReasonField string `json:"failure_reason_field"`
}

type ZTAPIVideoUsageContract struct {
	Fields map[string]string `json:"fields"`
}

type ZTAPIVideoSelector struct {
	Resolution         string `json:"resolution"`
	DurationSeconds    int    `json:"duration_seconds"`
	ContainsVideoInput bool   `json:"contains_video_input"`
}

type ZTAPIVideoReservationAuthority struct {
	Resolution         string            `json:"resolution"`
	DurationSeconds    int               `json:"duration_seconds"`
	ContainsVideoInput bool              `json:"contains_video_input"`
	MaximumDimensions  map[string]string `json:"maximum_dimensions"`
}

var (
	ztapiVideoModelPattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,254}$`)
	ztapiVideoPathPattern       = regexp.MustCompile(`^/[A-Za-z0-9._~!$&'()*+,;=:@%/-]+$`)
	ztapiVideoFieldPattern      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)
	ztapiVideoStatePattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	ztapiVideoResolutionPattern = regexp.MustCompile(`^([1-9][0-9]{2,3}p|4k)$`)
	ztapiVideoDimensionPattern  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	ztapiVideoHashPattern       = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

func (contract ZTAPIVideoProtocolContract) Clone() ZTAPIVideoProtocolContract {
	contract.Capabilities.Resolutions = append([]string(nil), contract.Capabilities.Resolutions...)
	contract.Capabilities.DurationSeconds = append([]int(nil), contract.Capabilities.DurationSeconds...)
	contract.States.Accepted = append([]string(nil), contract.States.Accepted...)
	contract.States.Processing = append([]string(nil), contract.States.Processing...)
	contract.States.Succeeded = append([]string(nil), contract.States.Succeeded...)
	contract.States.Failed = append([]string(nil), contract.States.Failed...)
	contract.Usage.Fields = cloneZTAPIVideoStringMap(contract.Usage.Fields)
	contract.Reservations = append([]ZTAPIVideoReservationAuthority(nil), contract.Reservations...)
	for index := range contract.Reservations {
		contract.Reservations[index].MaximumDimensions = cloneZTAPIVideoStringMap(contract.Reservations[index].MaximumDimensions)
	}
	return contract
}

func (contract ZTAPIVideoProtocolContract) FindReservationAuthority(selector ZTAPIVideoSelector) (ZTAPIVideoReservationAuthority, bool) {
	for _, authority := range contract.Reservations {
		if authority.Resolution == selector.Resolution && authority.DurationSeconds == selector.DurationSeconds && authority.ContainsVideoInput == selector.ContainsVideoInput {
			authority.MaximumDimensions = cloneZTAPIVideoStringMap(authority.MaximumDimensions)
			return authority, true
		}
	}
	return ZTAPIVideoReservationAuthority{}, false
}

func SealZTAPIVideoProtocolContract(contract ZTAPIVideoProtocolContract) (ZTAPIVideoProtocolContract, string, error) {
	contract = contract.Clone()
	contract.EvidenceHash = ""
	if err := normalizeAndValidateZTAPIVideoProtocolContract(&contract); err != nil {
		return ZTAPIVideoProtocolContract{}, "", err
	}
	payload, err := common.Marshal(contract)
	if err != nil {
		return ZTAPIVideoProtocolContract{}, "", err
	}
	contract.EvidenceHash = fmt.Sprintf("%x", sha256.Sum256(payload))
	canonical, err := common.Marshal(contract)
	if err != nil {
		return ZTAPIVideoProtocolContract{}, "", err
	}
	return contract.Clone(), string(canonical), nil
}

func ParseZTAPIVideoProtocolContract(raw string) (ZTAPIVideoProtocolContract, string, error) {
	var contract ZTAPIVideoProtocolContract
	if strings.TrimSpace(raw) == "" {
		return contract, "", errors.New("video protocol contract is required")
	}
	if err := common.RejectDuplicateJsonObjectMembers(strings.NewReader(raw)); err != nil {
		var duplicate *common.DuplicateJsonObjectMemberError
		if errors.As(err, &duplicate) {
			return contract, "", fmt.Errorf("duplicate video protocol contract field %q", duplicate.Name)
		}
		return contract, "", errors.New("video protocol contract must contain one valid JSON value")
	}
	if err := validateZTAPIVideoResponseIDJSONFields([]byte(raw)); err != nil {
		return contract, "", err
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&contract); err != nil {
		if strings.Contains(err.Error(), "unknown field") {
			return contract, "", fmt.Errorf("unknown video protocol contract field: %w", err)
		}
		return contract, "", errors.New("video protocol contract must be valid JSON")
	}
	if err := ensureZTAPIVideoJSONEOF(decoder); err != nil {
		return contract, "", err
	}
	wantHash := contract.EvidenceHash
	if !ztapiVideoHashPattern.MatchString(wantHash) {
		return contract, "", errors.New("video protocol contract evidence hash is invalid")
	}
	sealed, canonical, err := SealZTAPIVideoProtocolContract(contract)
	if err != nil {
		return contract, "", err
	}
	if sealed.EvidenceHash != wantHash {
		return contract, "", errors.New("video protocol contract evidence hash mismatch")
	}
	return sealed.Clone(), canonical, nil
}

func validateZTAPIVideoResponseIDJSONFields(raw []byte) error {
	var root map[string]json.RawMessage
	if common.Unmarshal(raw, &root) != nil {
		return nil
	}
	var version uint64
	if common.Unmarshal(root["version"], &version) != nil {
		return nil
	}
	for _, endpointName := range []string{"create", "fetch"} {
		var endpoint map[string]json.RawMessage
		if common.Unmarshal(root[endpointName], &endpoint) != nil {
			continue
		}
		switch version {
		case ZTAPIVideoProtocolContractVersion:
			if _, exists := endpoint["request_id_source"]; exists {
				return fmt.Errorf("video %s endpoint response-ID source contains forbidden field %q", endpointName, "request_id_source")
			}
			if _, exists := endpoint["request_id_key"]; exists {
				return fmt.Errorf("video %s endpoint response-ID source contains forbidden field %q", endpointName, "request_id_key")
			}
			if _, exists := endpoint["request_id_field"]; !exists {
				return fmt.Errorf("video %s endpoint response-ID source is missing field %q", endpointName, "request_id_field")
			}
		case ZTAPIVideoProtocolContractVersionV2:
			if _, exists := endpoint["request_id_field"]; exists {
				return fmt.Errorf("video %s endpoint response-ID source contains forbidden field %q", endpointName, "request_id_field")
			}
			if _, exists := endpoint["request_id_source"]; !exists {
				return fmt.Errorf("video %s endpoint response-ID source is missing field %q", endpointName, "request_id_source")
			}
			if _, exists := endpoint["request_id_key"]; !exists {
				return fmt.Errorf("video %s endpoint response-ID source is missing field %q", endpointName, "request_id_key")
			}
		}
	}
	return nil
}

func ensureZTAPIVideoJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("video protocol contract must contain one valid JSON value")
	}
	return nil
}

func normalizeAndValidateZTAPIVideoProtocolContract(contract *ZTAPIVideoProtocolContract) error {
	if contract == nil || (contract.Version != ZTAPIVideoProtocolContractVersion && contract.Version != ZTAPIVideoProtocolContractVersionV2) || contract.EvidenceVersion != ZTAPIVideoEvidenceVersion {
		return errors.New("unsupported video protocol or evidence version")
	}
	if contract.Provider != "aihub" {
		return errors.New("unsupported video protocol provider")
	}
	if contract.ProviderModel != strings.TrimSpace(contract.ProviderModel) || !ztapiVideoModelPattern.MatchString(contract.ProviderModel) || strings.ContainsAny(contract.ProviderModel, "*?") {
		return errors.New("video protocol provider model is invalid")
	}
	if contract.Auth.Method != "header" || contract.Auth.Header != "Authorization" || contract.Auth.Scheme != "Bearer" {
		return errors.New("unsupported video protocol authentication")
	}
	if err := validateZTAPIVideoEndpoint(contract.Create, "POST", false, contract.Version); err != nil {
		return fmt.Errorf("video create endpoint: %w", err)
	}
	if err := validateZTAPIVideoEndpoint(contract.Fetch, "GET", true, contract.Version); err != nil {
		return fmt.Errorf("video fetch endpoint: %w", err)
	}
	if contract.Callback.Enabled || contract.Callback.SignatureHeader != "" || contract.Callback.SignatureMethod != "" {
		return errors.New("video callback contract requires provider evidence")
	}
	if err := validateZTAPIVideoCapabilities(contract); err != nil {
		return err
	}
	if err := validateZTAPIVideoRequest(contract); err != nil {
		return err
	}
	if err := normalizeAndValidateZTAPIVideoStates(&contract.States); err != nil {
		return err
	}
	if !validZTAPIVideoField(contract.Result.URLField) || !validZTAPIVideoField(contract.Result.ResolutionField) || !validZTAPIVideoField(contract.Result.DurationField) || !validZTAPIVideoField(contract.Result.FailureReasonField) {
		return errors.New("video result fields are invalid")
	}
	if err := validateZTAPIVideoUsage(contract.Usage); err != nil {
		return err
	}
	return normalizeAndValidateZTAPIVideoReservations(contract)
}

func validateZTAPIVideoEndpoint(endpoint ZTAPIVideoEndpointContract, method string, fetch bool, version uint64) error {
	if endpoint.Method != method || !ztapiVideoPathPattern.MatchString(strings.ReplaceAll(endpoint.Path, "{task_id}", "task-id")) || strings.ContainsAny(endpoint.Path, "?#") {
		return errors.New("method or path is invalid")
	}
	placeholderCount := strings.Count(endpoint.Path, "{task_id}")
	if (fetch && placeholderCount != 1) || (!fetch && placeholderCount != 0) || strings.ContainsAny(strings.ReplaceAll(endpoint.Path, "{task_id}", ""), "{}") {
		return errors.New("task placeholder is invalid")
	}
	if !validZTAPIVideoField(endpoint.TaskIDField) {
		return errors.New("video task ID field is invalid")
	}
	if version == ZTAPIVideoProtocolContractVersion {
		if !validZTAPIVideoField(endpoint.RequestIDField) || endpoint.TaskIDField == endpoint.RequestIDField || endpoint.RequestIDSource != "" || endpoint.RequestIDKey != "" {
			return errors.New("video response-ID source is invalid")
		}
		return nil
	}
	if endpoint.RequestIDField != "" {
		return errors.New("video response-ID source is invalid")
	}
	switch endpoint.RequestIDSource {
	case ZTAPIResponseIDSourceBodyField:
		if !validZTAPIVideoField(endpoint.RequestIDKey) || endpoint.TaskIDField == endpoint.RequestIDKey {
			return errors.New("video response-ID source is invalid")
		}
	case ZTAPIResponseIDSourceHeader:
		if !validZTAPIResponseIDHeader(endpoint.RequestIDKey) {
			return errors.New("video response-ID source is invalid")
		}
	default:
		return errors.New("video response-ID source is invalid")
	}
	return nil
}

func validateZTAPIVideoCapabilities(contract *ZTAPIVideoProtocolContract) error {
	if len(contract.Capabilities.Resolutions) == 0 || len(contract.Capabilities.DurationSeconds) == 0 {
		return errors.New("video capabilities are incomplete")
	}
	if hasDuplicateZTAPIVideoStrings(contract.Capabilities.Resolutions) {
		return errors.New("video resolutions must be unique")
	}
	for _, resolution := range contract.Capabilities.Resolutions {
		if resolution != strings.TrimSpace(resolution) || !ztapiVideoResolutionPattern.MatchString(resolution) {
			return errors.New("video resolution is invalid")
		}
	}
	if hasDuplicateZTAPIVideoInts(contract.Capabilities.DurationSeconds) {
		return errors.New("video durations must be unique")
	}
	for _, duration := range contract.Capabilities.DurationSeconds {
		if duration < 1 || duration > 600 {
			return errors.New("video duration is invalid")
		}
	}
	sort.Strings(contract.Capabilities.Resolutions)
	sort.Ints(contract.Capabilities.DurationSeconds)
	return nil
}

func validateZTAPIVideoRequest(contract *ZTAPIVideoProtocolContract) error {
	fields := []string{contract.Request.ModelField, contract.Request.PromptField, contract.Request.ResolutionField, contract.Request.DurationField}
	for _, field := range fields {
		if !validZTAPIVideoField(field) {
			return errors.New("video request fields are invalid")
		}
	}
	if hasDuplicateZTAPIVideoStrings(fields) {
		return errors.New("video request fields overlap")
	}
	if contract.Capabilities.SupportsVideoInput {
		if !validZTAPIVideoField(contract.Request.VideoInputField) {
			return errors.New("video input field is required")
		}
	} else if contract.Request.VideoInputField != "" {
		return errors.New("video input field is not admitted")
	}
	return nil
}

func normalizeAndValidateZTAPIVideoStates(states *ZTAPIVideoStateContract) error {
	if states == nil || !validZTAPIVideoField(states.Field) {
		return errors.New("video status field is invalid")
	}
	all := make(map[string]struct{})
	groups := []*[]string{&states.Accepted, &states.Processing, &states.Succeeded, &states.Failed}
	for _, group := range groups {
		if len(*group) == 0 || hasDuplicateZTAPIVideoStrings(*group) {
			return errors.New("video state groups must be non-empty and unique")
		}
		for _, state := range *group {
			if state != strings.TrimSpace(state) || !ztapiVideoStatePattern.MatchString(state) {
				return errors.New("video provider state is invalid")
			}
			if _, exists := all[state]; exists {
				return errors.New("video provider states overlap")
			}
			all[state] = struct{}{}
		}
		sort.Strings(*group)
	}
	return nil
}

func validateZTAPIVideoUsage(usage ZTAPIVideoUsageContract) error {
	if len(usage.Fields) == 0 {
		return errors.New("video usage fields are required")
	}
	seen := make(map[string]struct{}, len(usage.Fields))
	for dimension, field := range usage.Fields {
		if !ztapiVideoDimensionPattern.MatchString(dimension) || !validZTAPIVideoField(field) {
			return errors.New("video usage field binding is invalid")
		}
		if _, exists := seen[field]; exists {
			return errors.New("video usage fields overlap")
		}
		seen[field] = struct{}{}
	}
	return nil
}

func normalizeAndValidateZTAPIVideoReservations(contract *ZTAPIVideoProtocolContract) error {
	inputVariants := 1
	if contract.Capabilities.SupportsVideoInput {
		inputVariants = 2
	}
	want := len(contract.Capabilities.Resolutions) * len(contract.Capabilities.DurationSeconds) * inputVariants
	if len(contract.Reservations) != want {
		return errors.New("video reservation authority must cover every admitted selector exactly once")
	}
	seen := make(map[string]struct{}, want)
	for index := range contract.Reservations {
		authority := &contract.Reservations[index]
		if !containsZTAPIVideoString(contract.Capabilities.Resolutions, authority.Resolution) || !containsZTAPIVideoInt(contract.Capabilities.DurationSeconds, authority.DurationSeconds) || (!contract.Capabilities.SupportsVideoInput && authority.ContainsVideoInput) {
			return errors.New("video reservation authority contains an unadmitted selector")
		}
		key := ztapiVideoSelectorKey(authority.Resolution, authority.DurationSeconds, authority.ContainsVideoInput)
		if _, exists := seen[key]; exists {
			return errors.New("video reservation authority contains a duplicate selector")
		}
		seen[key] = struct{}{}
		if len(authority.MaximumDimensions) != len(contract.Usage.Fields) {
			return errors.New("video reservation dimensions must exactly match usage dimensions")
		}
		positive := false
		for dimension := range contract.Usage.Fields {
			raw, ok := authority.MaximumDimensions[dimension]
			value, err := strconv.ParseInt(raw, 10, 64)
			if !ok || err != nil || value < 0 || strconv.FormatInt(value, 10) != raw {
				return errors.New("video reservation dimension is invalid")
			}
			positive = positive || value > 0
		}
		if !positive {
			return errors.New("video reservation must contain a positive bound")
		}
	}
	sort.Slice(contract.Reservations, func(i, j int) bool {
		left, right := contract.Reservations[i], contract.Reservations[j]
		return ztapiVideoSelectorKey(left.Resolution, left.DurationSeconds, left.ContainsVideoInput) < ztapiVideoSelectorKey(right.Resolution, right.DurationSeconds, right.ContainsVideoInput)
	})
	return nil
}

func validZTAPIVideoField(value string) bool {
	return value == strings.TrimSpace(value) && ztapiVideoFieldPattern.MatchString(value)
}

func ztapiVideoSelectorKey(resolution string, duration int, videoInput bool) string {
	return resolution + "\x00" + strconv.Itoa(duration) + "\x00" + strconv.FormatBool(videoInput)
}

func hasDuplicateZTAPIVideoStrings(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			return true
		}
		seen[value] = struct{}{}
	}
	return false
}

func hasDuplicateZTAPIVideoInts(values []int) bool {
	seen := make(map[int]struct{}, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			return true
		}
		seen[value] = struct{}{}
	}
	return false
}

func containsZTAPIVideoString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func containsZTAPIVideoInt(values []int, target int) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func cloneZTAPIVideoStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}
