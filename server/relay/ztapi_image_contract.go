package relay

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image/png"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const (
	ztapiImageProtocolErrorCode = types.ErrorCode("ztapi_image_protocol_violation")
	// Managed image responses may contain inline base64 output. 64 MiB permits
	// normal multi-image payloads while bounding amplification before parsing.
	ztapiManagedImageResponseLimit = int64(64 << 20)
)

var ErrZTAPIManagedImageResponseTooLarge = errors.New("managed image response exceeds the 64 MiB limit")

func isZTAPIManagedImage(info *relaycommon.RelayInfo) bool {
	return info != nil && info.ZTAPIPublicationSnapshot != nil && info.ZTAPIPublicationSnapshot.Modality == "image"
}

func ztapiImageProtocolError(err error) *types.NewAPIError {
	return types.NewErrorWithStatusCode(err, ztapiImageProtocolErrorCode, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
}

func ztapiImageUpstreamProtocolError(err error) *types.NewAPIError {
	return types.NewErrorWithStatusCode(err, ztapiImageProtocolErrorCode, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
}

func verifiedZTAPIImageContract(info *relaycommon.RelayInfo) (*types.ZTAPIImageProtocolContract, error) {
	if !isZTAPIManagedImage(info) {
		return nil, nil
	}
	snapshot := info.ZTAPIPublicationSnapshot
	if snapshot.ImageProtocolContract == nil {
		return nil, errors.New("verified frozen image protocol contract is required")
	}
	contract := snapshot.ImageProtocolContract.Clone()
	sealed, _, err := types.SealZTAPIImageProtocolContract(contract)
	if err != nil || sealed.EvidenceHash != contract.EvidenceHash {
		return nil, errors.New("verified frozen image protocol contract is invalid")
	}
	if contract.ProviderModel != snapshot.SourceModel {
		return nil, errors.New("image protocol provider model does not match the frozen publication")
	}
	return &contract, nil
}

// WithZTAPIImageAdmission ensures the protected stage is never reached for a
// rejected managed image request. Controller billing uses this before reserve.
func WithZTAPIImageAdmission(c *gin.Context, info *relaycommon.RelayInfo, next func() *types.NewAPIError) *types.NewAPIError {
	if err := AdmitZTAPIImageRequest(c, info); err != nil {
		return err
	}
	if next == nil {
		return nil
	}
	return next()
}

// AdmitZTAPIImageRequest validates only managed image publications. Legacy and
// unmanaged image behavior is intentionally untouched.
func AdmitZTAPIImageRequest(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {
	if info != nil && info.ZTAPIPublicationSnapshot != nil && c != nil && c.Request != nil &&
		strings.HasPrefix(c.Request.URL.Path, "/v1/images/") && info.ZTAPIPublicationSnapshot.Modality != "image" {
		return ztapiImageProtocolError(errors.New("managed non-image publications cannot use image endpoints"))
	}
	if !isZTAPIManagedImage(info) {
		return nil
	}
	contract, err := verifiedZTAPIImageContract(info)
	if err != nil {
		return ztapiImageProtocolError(err)
	}
	if c == nil || c.Request == nil {
		return ztapiImageProtocolError(errors.New("managed image request requires an HTTP request"))
	}
	if contract.Edit != nil && c.Request.URL.Path == contract.Edit.Path {
		return admitZTAPIImageEditRequest(c, info, contract)
	}
	if c.Request.Method != contract.Method || c.Request.URL.Path != contract.Path {
		return ztapiImageProtocolError(errors.New("managed image requests require exact POST /v1/images/generations"))
	}
	request, ok := info.Request.(*dto.ImageRequest)
	if !ok || request == nil {
		return ztapiImageProtocolError(errors.New("managed image request body is invalid"))
	}
	body, err := managedImageRequestObject(c)
	if err != nil {
		return ztapiImageProtocolError(err)
	}
	allowed := map[string]bool{
		"model": true, "prompt": true, "n": true, "size": true,
		"quality": true, "response_format": true, "stream": true,
	}
	for field := range body {
		if !allowed[field] {
			return ztapiImageProtocolError(fmt.Errorf("managed image field %q is not covered by the frozen contract", field))
		}
	}
	for _, field := range []string{"model", "prompt", "n", "size", "quality", "response_format"} {
		if _, present := body[field]; !present {
			return ztapiImageProtocolError(fmt.Errorf("managed image field %q requires an explicit frozen value", field))
		}
	}
	modelName, modelOK := body["model"].(string)
	prompt, promptOK := body["prompt"].(string)
	if !modelOK || modelName != info.OriginModelName || modelName != info.ZTAPIPublicationSnapshot.PublicName {
		return ztapiImageProtocolError(errors.New("managed image model must match the published alias"))
	}
	if !promptOK || strings.TrimSpace(prompt) == "" || prompt != request.Prompt {
		return ztapiImageProtocolError(errors.New("managed image prompt is invalid"))
	}
	size, sizeOK := body["size"].(string)
	quality, qualityOK := body["quality"].(string)
	responseFormat, formatOK := body["response_format"].(string)
	count, countOK := exactNonNegativeInteger(body["n"])
	if !sizeOK || !containsZTAPIImageCapability(contract.Capabilities.Sizes, size) || request.Size != size {
		return ztapiImageProtocolError(errors.New("managed image size is not supported by the frozen contract"))
	}
	if !qualityOK || !contract.Capabilities.SupportsRenderOption(size, quality) || request.Quality != quality {
		return ztapiImageProtocolError(errors.New("managed image quality is not supported by the frozen contract"))
	}
	if !formatOK || !containsZTAPIImageCapability(contract.Capabilities.ResponseFormats, responseFormat) || request.ResponseFormat != responseFormat {
		return ztapiImageProtocolError(errors.New("managed image response format is not supported by the frozen contract"))
	}
	if !countOK || count < contract.Capabilities.MinCount || count > contract.Capabilities.MaxCount || request.N == nil || int(*request.N) != count {
		return ztapiImageProtocolError(errors.New("managed image count is not supported by the frozen contract"))
	}
	if stream, present := body["stream"]; present {
		streamValue, ok := stream.(bool)
		if !ok || streamValue {
			return ztapiImageProtocolError(errors.New("managed image streaming is not covered by the frozen contract"))
		}
	}
	return nil
}

func admitZTAPIImageEditRequest(c *gin.Context, info *relaycommon.RelayInfo, contract *types.ZTAPIImageProtocolContract) *types.NewAPIError {
	edit := contract.Edit
	if edit == nil || c.Request.Method != edit.Method || c.Request.URL.Path != edit.Path {
		return ztapiImageProtocolError(errors.New("managed image edit requests require exact POST /v1/images/edits"))
	}
	contentType, _, err := mime.ParseMediaType(c.Request.Header.Get("Content-Type"))
	if err != nil || contentType != edit.ContentType {
		return ztapiImageProtocolError(errors.New("managed image edits require multipart/form-data"))
	}
	form, err := managedImageEditMultipartForm(c)
	if err != nil {
		return ztapiImageProtocolError(err)
	}
	formData := url.Values(form.Value)
	for field, values := range form.Value {
		if field != "stream" {
			policy, covered := edit.RequestFields[field]
			if !covered || policy == types.ZTAPIImageRequestFieldOmit {
				return ztapiImageProtocolError(fmt.Errorf("managed image edit field %q is not covered by the frozen contract", field))
			}
		}
		if len(values) != 1 {
			return ztapiImageProtocolError(fmt.Errorf("managed image edit field %q must contain exactly one value", field))
		}
	}
	for field, policy := range edit.RequestFields {
		values, present := form.Value[field]
		switch policy {
		case types.ZTAPIImageRequestFieldRequired:
			if !present || len(values) != 1 {
				return ztapiImageProtocolError(fmt.Errorf("managed image edit field %q requires exactly one value", field))
			}
		case types.ZTAPIImageRequestFieldOmit:
			if present {
				return ztapiImageProtocolError(fmt.Errorf("managed image edit field %q must be omitted", field))
			}
		}
	}
	request, ok := info.Request.(*dto.ImageRequest)
	if !ok || request == nil {
		return ztapiImageProtocolError(errors.New("managed image edit request body is invalid"))
	}
	if request.ResponseFormat == "" && len(contract.Capabilities.ResponseFormats) == 1 {
		// Some verified edit protocols, including GPT Image 2, omit
		// response_format on the wire while exposing one fixed result format.
		// Normalize it before billing and response validation, without adding it
		// back to the upstream multipart request.
		request.ResponseFormat = contract.Capabilities.ResponseFormats[0]
	}
	modelName := formData.Get("model")
	prompt := formData.Get("prompt")
	if modelName != info.OriginModelName || modelName != info.ZTAPIPublicationSnapshot.PublicName {
		return ztapiImageProtocolError(errors.New("managed image edit model must match the published alias"))
	}
	if strings.TrimSpace(prompt) == "" || prompt != request.Prompt {
		return ztapiImageProtocolError(errors.New("managed image edit prompt is invalid"))
	}
	count, err := strconv.Atoi(formData.Get("n"))
	if err != nil || count < contract.Capabilities.MinCount || count > contract.Capabilities.MaxCount || request.N == nil || int(*request.N) != count {
		return ztapiImageProtocolError(errors.New("managed image edit count is not supported by the frozen contract"))
	}
	for _, field := range []struct {
		name  string
		value string
		bound []string
	}{
		{name: "size", value: request.Size, bound: contract.Capabilities.Sizes},
		{name: "quality", value: request.Quality, bound: contract.Capabilities.Qualities},
		{name: "response_format", value: request.ResponseFormat, bound: contract.Capabilities.ResponseFormats},
	} {
		formValue := formData.Get(field.name)
		if formValue == "" {
			if _, required := edit.RequestFields[field.name]; required && edit.RequestFields[field.name] == types.ZTAPIImageRequestFieldRequired {
				return ztapiImageProtocolError(fmt.Errorf("managed image edit field %q requires a value", field.name))
			}
			continue
		}
		if !containsZTAPIImageCapability(field.bound, formValue) || field.value != formValue {
			return ztapiImageProtocolError(fmt.Errorf("managed image edit field %q is not supported by the frozen contract", field.name))
		}
	}
	if !contract.Capabilities.SupportsRenderOption(request.Size, request.Quality) {
		return ztapiImageProtocolError(errors.New("managed image edit size and quality combination is not supported by the frozen contract"))
	}
	if streamValue := strings.TrimSpace(formData.Get("stream")); streamValue != "" {
		stream, parseErr := strconv.ParseBool(streamValue)
		if parseErr != nil || stream {
			return ztapiImageProtocolError(errors.New("managed image edit streaming is not covered by the frozen contract"))
		}
	}
	for field := range form.File {
		if field != edit.InputField {
			return ztapiImageProtocolError(fmt.Errorf("managed image edit file field %q is not covered by the frozen contract", field))
		}
	}
	files := form.File[edit.InputField]
	if len(files) < 1 || len(files) > edit.MaxInputFiles {
		return ztapiImageProtocolError(fmt.Errorf("managed image edit accepts between 1 and %d reference images", edit.MaxInputFiles))
	}
	var totalBytes int64
	for _, fileHeader := range files {
		if fileHeader == nil || fileHeader.Size <= 0 || fileHeader.Size > edit.MaxInputBytes || totalBytes > edit.MaxTotalInputBytes-fileHeader.Size {
			return ztapiImageProtocolError(errors.New("managed image edit input exceeds the frozen size limits"))
		}
		totalBytes += fileHeader.Size
		if _, _, fileErr := readManagedImageEditFile(fileHeader, edit); fileErr != nil {
			return ztapiImageProtocolError(fileErr)
		}
	}
	return nil
}

func managedImageEditMultipartForm(c *gin.Context) (*multipart.Form, error) {
	if c.Request.MultipartForm != nil {
		return c.Request.MultipartForm, nil
	}
	form, err := common.ParseMultipartFormReusable(c)
	if err != nil {
		return nil, fmt.Errorf("parse managed image edit multipart form: %w", err)
	}
	c.Request.MultipartForm = form
	c.Request.PostForm = url.Values(form.Value)
	return form, nil
}

func readManagedImageEditFile(fileHeader *multipart.FileHeader, edit *types.ZTAPIImageEditEndpointContract) ([]byte, string, error) {
	file, err := fileHeader.Open()
	if err != nil {
		return nil, "", fmt.Errorf("open managed image edit reference: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, edit.MaxInputBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("read managed image edit reference: %w", err)
	}
	if len(data) == 0 || int64(len(data)) > edit.MaxInputBytes {
		return nil, "", errors.New("managed image edit reference exceeds the per-file size limit")
	}
	detected := http.DetectContentType(data)
	if !containsZTAPIImageCapability(edit.AllowedMimeTypes, detected) {
		return nil, "", fmt.Errorf("managed image edit reference MIME type %q is not allowed", detected)
	}
	return data, detected, nil
}

func rebuildManagedImageEditDispatch(c *gin.Context, contract *types.ZTAPIImageProtocolContract) ([]byte, string, error) {
	form, err := managedImageEditMultipartForm(c)
	if err != nil {
		return nil, "", err
	}
	edit := contract.Edit
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("model", contract.ProviderModel); err != nil {
		return nil, "", err
	}
	for _, field := range []string{"prompt", "n", "size", "quality", "response_format"} {
		policy := edit.RequestFields[field]
		values := form.Value[field]
		if policy == types.ZTAPIImageRequestFieldOmit || len(values) == 0 {
			continue
		}
		if len(values) != 1 {
			return nil, "", fmt.Errorf("managed image edit field %q must contain exactly one value", field)
		}
		if err := writer.WriteField(field, values[0]); err != nil {
			return nil, "", err
		}
	}
	for index, fileHeader := range form.File[edit.InputField] {
		data, detected, err := readManagedImageEditFile(fileHeader, edit)
		if err != nil {
			return nil, "", err
		}
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="reference-%d%s"`, edit.InputField, index+1, imageEditFilenameExtension(detected)))
		header.Set("Content-Type", detected)
		part, err := writer.CreatePart(header)
		if err != nil {
			return nil, "", err
		}
		if _, err := part.Write(data); err != nil {
			return nil, "", err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	return body.Bytes(), writer.FormDataContentType(), nil
}

func imageEditFilenameExtension(contentType string) string {
	switch contentType {
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	default:
		return ".png"
	}
}

func managedImageRequestObject(c *gin.Context) (map[string]any, error) {
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return nil, fmt.Errorf("read managed image request: %w", err)
	}
	raw, err := storage.Bytes()
	if err != nil {
		return nil, fmt.Errorf("read managed image request: %w", err)
	}
	if err := common.RejectDuplicateJsonObjectMembers(bytes.NewReader(raw)); err != nil {
		return nil, fmt.Errorf("managed image request must contain one unambiguous JSON value: %w", err)
	}
	var body map[string]any
	if err := common.Unmarshal(raw, &body); err != nil || body == nil {
		return nil, errors.New("managed image request must be a JSON object")
	}
	return body, nil
}

func exactNonNegativeInteger(value any) (int, bool) {
	number, ok := value.(float64)
	if !ok || math.IsNaN(number) || math.IsInf(number, 0) || number < 0 || math.Trunc(number) != number || number > math.MaxInt {
		return 0, false
	}
	return int(number), true
}

func containsZTAPIImageCapability(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// PrepareZTAPIManagedImageDispatch rebuilds the upstream request solely from
// frozen, admitted fields. Model mapping, parameter overrides and body
// pass-through cannot change this request.
func PrepareZTAPIManagedImageDispatch(c *gin.Context, info *relaycommon.RelayInfo) (*relaycommon.ZTAPIManagedImageDispatch, *types.NewAPIError) {
	if err := AdmitZTAPIImageRequest(c, info); err != nil {
		return nil, err
	}
	if !isZTAPIManagedImage(info) {
		return nil, nil
	}
	contract, err := verifiedZTAPIImageContract(info)
	if err != nil {
		return nil, ztapiImageProtocolError(err)
	}
	incoming := info.Request.(*dto.ImageRequest)
	if contract.Edit != nil && c != nil && c.Request != nil && c.Request.URL.Path == contract.Edit.Path {
		if info.ChannelMeta == nil || info.ChannelType != constant.ChannelTypeOpenAI || info.ApiType != constant.APITypeOpenAI {
			return nil, ztapiImageProtocolError(errors.New("frozen OpenAI image edit binding requires the exact verified OpenAI channel family"))
		}
		body, contentType, buildErr := rebuildManagedImageEditDispatch(c, contract)
		if buildErr != nil {
			return nil, ztapiImageProtocolError(fmt.Errorf("build frozen managed image edit dispatch: %w", buildErr))
		}
		dispatch := &relaycommon.ZTAPIManagedImageDispatch{
			Body: body, ProviderPath: contract.Edit.ProviderPath,
			WireProtocol: contract.Edit.WireProtocol, ContentType: contentType,
		}
		if !info.SetZTAPIManagedImageDispatch(dispatch) {
			return nil, ztapiImageProtocolError(errors.New("frozen managed image edit dispatch is invalid"))
		}
		info.UpstreamModelName = contract.ProviderModel
		info.RequestURLPath = contract.Edit.ProviderPath
		return info.GetZTAPIManagedImageDispatch(), nil
	}
	wireProtocol := contract.WireProtocol
	providerPath := contract.ProviderPath
	var body []byte
	switch wireProtocol {
	case "":
		// V1 and previously sealed V2 OpenAI image contracts retain their exact
		// canonical JSON and resolve to the legacy verified binding at runtime.
		wireProtocol = types.ZTAPIImageWireProtocolOpenAIImages
		providerPath = contract.Path
		fallthrough
	case types.ZTAPIImageWireProtocolOpenAIImages:
		if info.ChannelMeta == nil || info.ChannelType != constant.ChannelTypeOpenAI || info.ApiType != constant.APITypeOpenAI {
			return nil, ztapiImageProtocolError(errors.New("frozen OpenAI Images binding requires the exact verified OpenAI channel family"))
		}
		count := *incoming.N
		upstream := dto.ImageRequest{
			Model: contract.ProviderModel, Prompt: incoming.Prompt, N: &count,
			Size: incoming.Size, Quality: incoming.Quality, ResponseFormat: incoming.ResponseFormat,
		}
		for field, policy := range contract.UpstreamRequestFields {
			if policy != types.ZTAPIImageRequestFieldOmit {
				continue
			}
			switch field {
			case "size":
				upstream.Size = ""
			case "quality":
				upstream.Quality = ""
			case "response_format":
				upstream.ResponseFormat = ""
			}
		}
		body, err = common.Marshal(upstream)
	case types.ZTAPIImageWireProtocolGeminiGenerateContent:
		if info.ChannelMeta == nil || info.ChannelType != constant.ChannelTypeGemini || info.ApiType != constant.APITypeGemini {
			return nil, ztapiImageProtocolError(errors.New("frozen Gemini image binding requires the exact verified Gemini channel family"))
		}
		upstream := dto.GeminiChatRequest{
			Contents:         []dto.GeminiChatContent{{Role: "user", Parts: []dto.GeminiPart{{Text: incoming.Prompt}}}},
			GenerationConfig: dto.GeminiChatGenerationConfig{ResponseModalities: []string{"TEXT", "IMAGE"}},
		}
		body, err = common.Marshal(upstream)
	default:
		return nil, ztapiImageProtocolError(errors.New("unsupported frozen image wire protocol"))
	}
	if err != nil {
		return nil, ztapiImageProtocolError(fmt.Errorf("build frozen managed image dispatch: %w", err))
	}
	dispatch := &relaycommon.ZTAPIManagedImageDispatch{Body: body, ProviderPath: providerPath, WireProtocol: wireProtocol, ContentType: "application/json"}
	if !info.SetZTAPIManagedImageDispatch(dispatch) {
		return nil, ztapiImageProtocolError(errors.New("frozen managed image dispatch is invalid"))
	}
	info.UpstreamModelName = contract.ProviderModel
	info.RequestURLPath = providerPath
	return info.GetZTAPIManagedImageDispatch(), nil
}

func BeginZTAPIManagedImageAttempt(info *relaycommon.RelayInfo) uint64 {
	if !isZTAPIManagedImage(info) {
		return 0
	}
	return info.BeginZTAPIImageResponseAttempt()
}

func AbortZTAPIManagedImageAttempt(info *relaycommon.RelayInfo, attemptID uint64) {
	info.AbortZTAPIImageResponseAttempt(attemptID)
}

// ValidateZTAPIManagedImageResponse reads and restores the exact upstream body,
// validates the frozen structural bindings, and returns an unpublished billing
// candidate. The OpenAI adapter promotes the private candidate before writing.
func ValidateZTAPIManagedImageResponse(info *relaycommon.RelayInfo, resp *http.Response) (*relaycommon.ZTAPIValidatedImageResponse, *types.NewAPIError) {
	if !isZTAPIManagedImage(info) {
		return nil, nil
	}
	attemptID := info.CurrentZTAPIImageResponseAttemptID()
	validated := false
	defer func() {
		if validated {
			return
		}
		info.AbortZTAPIImageResponseAttempt(attemptID)
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
			resp.Body = http.NoBody
		}
	}()
	contract, err := verifiedZTAPIImageContract(info)
	if err != nil {
		return nil, ztapiImageUpstreamProtocolError(err)
	}
	if resp == nil || resp.Body == nil || resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, ztapiImageUpstreamProtocolError(errors.New("managed image response is not a successful HTTP response"))
	}
	if attemptID == 0 {
		return nil, ztapiImageUpstreamProtocolError(errors.New("managed image response has no active attempt"))
	}
	raw, readErr := readAndRestoreZTAPIManagedImageBody(resp, ztapiManagedImageResponseLimit)
	if readErr != nil {
		return nil, ztapiImageUpstreamProtocolError(fmt.Errorf("read managed image response: %w", readErr))
	}
	if !gjson.ValidBytes(raw) {
		return nil, ztapiImageUpstreamProtocolError(errors.New("managed image response is malformed JSON"))
	}
	root := gjson.ParseBytes(raw)
	usagePendingReason := ""
	duplicates, duplicateErr := common.FindDuplicateJsonObjectMembers(bytes.NewReader(raw))
	if duplicateErr != nil {
		return nil, ztapiImageUpstreamProtocolError(fmt.Errorf("managed image response is ambiguous: %w", duplicateErr))
	}
	for index := range duplicates {
		duplicate := duplicates[index]
		if !ztapiImageJSONPathAtOrBelow(duplicate.Path, contract.Usage.UsageField) {
			return nil, ztapiImageUpstreamProtocolError(fmt.Errorf("managed image response is ambiguous: %w", &duplicate))
		}
		if usagePendingReason == "" {
			usagePendingReason = duplicate.Error()
		}
	}
	usageResult := root.Get(contract.Usage.UsageField)
	var rawUsageJSON []byte
	if usageResult.Exists() {
		var ok bool
		rawUsageJSON, ok = exactZTAPIImageJSONToken(raw, usageResult)
		if !ok {
			return nil, ztapiImageUpstreamProtocolError(errors.New("managed image response usage cannot be preserved exactly"))
		}
	}
	if !root.IsObject() {
		return nil, ztapiImageUpstreamProtocolError(errors.New("managed image response must be a JSON object"))
	}
	requestID, idErr := ztapiImageResponseRequestID(contract, root, resp.Header)
	if idErr != nil {
		return nil, ztapiImageUpstreamProtocolError(idErr)
	}
	if contract.Response.Schema == types.ZTAPIImageResponseSchemaGeminiInlineImages {
		resultCount, canonicalResponse, geminiUsagePendingReason, geminiErr := validateZTAPIGeminiImageResponse(raw, info)
		if geminiErr != nil {
			return nil, ztapiImageUpstreamProtocolError(geminiErr)
		}
		if usagePendingReason == "" {
			usagePendingReason = geminiUsagePendingReason
		}
		handoff := &relaycommon.ZTAPIValidatedImageResponse{
			ContractVersion: contract.Version, EvidenceHash: contract.EvidenceHash,
			UpstreamRequestID: requestID, ResultCount: resultCount,
			RawResponse: raw, RawUsageJSON: rawUsageJSON, CanonicalResponse: canonicalResponse,
			UsagePendingReason: usagePendingReason,
		}
		if !info.RecordZTAPIImageResponseValidation(attemptID, handoff) {
			return nil, ztapiImageUpstreamProtocolError(errors.New("managed image response attempt changed during validation"))
		}
		validated = true
		return handoff, nil
	}
	resultsResult := root.Get(contract.Response.ResultsField)
	if !resultsResult.Exists() || !resultsResult.IsArray() {
		return nil, ztapiImageUpstreamProtocolError(errors.New("managed image response results are missing or malformed"))
	}
	results := resultsResult.Array()
	if len(results) == 0 {
		return nil, ztapiImageUpstreamProtocolError(errors.New("managed image response results are missing or malformed"))
	}
	if request, ok := info.Request.(*dto.ImageRequest); ok && request.N != nil && len(results) != int(*request.N) {
		return nil, ztapiImageUpstreamProtocolError(errors.New("managed image response result count does not match the admitted request"))
	}
	request, requestOK := info.Request.(*dto.ImageRequest)
	if !requestOK || request == nil || !containsZTAPIImageCapability(contract.Capabilities.ResponseFormats, request.ResponseFormat) {
		return nil, ztapiImageUpstreamProtocolError(errors.New("managed image admitted response format is unavailable"))
	}
	for _, item := range results {
		if !item.IsObject() {
			return nil, ztapiImageUpstreamProtocolError(errors.New("managed image result must be an object"))
		}
		populated := 0
		for resultType, field := range contract.Response.ResultFields {
			value := item.Get(field)
			if resultType == request.ResponseFormat && value.Exists() && value.Type == gjson.String && validZTAPIImageResult(resultType, value.String()) {
				populated++
			} else if value.Exists() {
				return nil, ztapiImageUpstreamProtocolError(errors.New("managed image result representation does not match the admitted response format"))
			}
		}
		if populated != 1 {
			return nil, ztapiImageUpstreamProtocolError(errors.New("managed image result must contain exactly the admitted frozen result field"))
		}
	}
	handoff := &relaycommon.ZTAPIValidatedImageResponse{
		ContractVersion:    contract.Version,
		EvidenceHash:       contract.EvidenceHash,
		UpstreamRequestID:  requestID,
		ResultCount:        len(results),
		RawResponse:        raw,
		RawUsageJSON:       rawUsageJSON,
		UsagePendingReason: usagePendingReason,
	}
	if !info.RecordZTAPIImageResponseValidation(attemptID, handoff) {
		return nil, ztapiImageUpstreamProtocolError(errors.New("managed image response attempt changed during validation"))
	}
	validated = true
	return handoff, nil
}

func validateZTAPIGeminiImageResponse(raw []byte, info *relaycommon.RelayInfo) (int, []byte, string, error) {
	var response dto.GeminiChatResponse
	if err := common.Unmarshal(raw, &response); err != nil {
		return 0, nil, "", errors.New("Gemini native image response is malformed")
	}
	if response.PromptFeedback != nil && response.PromptFeedback.BlockReason != nil && strings.TrimSpace(*response.PromptFeedback.BlockReason) != "" {
		return 0, nil, "", errors.New("Gemini native image response was blocked by safety policy")
	}
	if len(response.Candidates) == 0 {
		return 0, nil, "", errors.New("Gemini native image response contains no candidates")
	}
	request, ok := info.Request.(*dto.ImageRequest)
	if !ok || request == nil || request.N == nil || request.ResponseFormat != "b64_json" {
		return 0, nil, "", errors.New("Gemini native image admitted request is unavailable")
	}
	if len(response.Candidates) != int(*request.N) {
		return 0, nil, "", errors.New("Gemini native image candidate count does not match the admitted request")
	}
	images := make([]dto.ImageData, 0, int(*request.N))
	for _, candidate := range response.Candidates {
		if candidate.FinishReason == nil || *candidate.FinishReason != "STOP" {
			return 0, nil, "", errors.New("Gemini native image response has an unsafe or unknown finish reason")
		}
		for _, rating := range candidate.SafetyRatings {
			if rating.Blocked {
				return 0, nil, "", errors.New("Gemini native image response was blocked by a safety rating")
			}
		}
		for _, part := range candidate.Content.Parts {
			if part.InlineData == nil {
				if part.Text == "" {
					return 0, nil, "", errors.New("Gemini native image response contains an unsupported result part")
				}
				continue
			}
			if part.Text != "" || part.InlineData.MimeType != "image/png" || strings.TrimSpace(part.InlineData.Data) == "" {
				return 0, nil, "", errors.New("Gemini native image response contains an invalid inline PNG result")
			}
			decoded, err := base64.StdEncoding.DecodeString(part.InlineData.Data)
			if err != nil || len(decoded) == 0 {
				return 0, nil, "", errors.New("Gemini native image response contains invalid base64 image data")
			}
			config, err := png.DecodeConfig(bytes.NewReader(decoded))
			if err != nil || config.Width <= 0 || config.Height <= 0 {
				return 0, nil, "", errors.New("Gemini native image response contains data that is not a valid PNG")
			}
			widthRaw, heightRaw, found := strings.Cut(request.Size, "x")
			expectedWidth, widthErr := strconv.Atoi(widthRaw)
			expectedHeight, heightErr := strconv.Atoi(heightRaw)
			if !found || widthErr != nil || heightErr != nil || expectedWidth <= 0 || expectedHeight <= 0 ||
				config.Width != expectedWidth || config.Height != expectedHeight {
				return 0, nil, "", errors.New("Gemini native image response dimensions do not match the admitted request")
			}
			decodedImage, err := png.Decode(bytes.NewReader(decoded))
			if err != nil || decodedImage.Bounds().Dx() != expectedWidth || decodedImage.Bounds().Dy() != expectedHeight {
				return 0, nil, "", errors.New("Gemini native image response contains a truncated or corrupt PNG")
			}
			images = append(images, dto.ImageData{B64Json: part.InlineData.Data})
		}
	}
	if len(images) != int(*request.N) {
		return 0, nil, "", errors.New("Gemini native image response result count does not match the admitted request")
	}
	canonical, err := common.Marshal(dto.ImageResponse{Created: common.GetTimestamp(), Data: images})
	if err != nil {
		return 0, nil, "", fmt.Errorf("build canonical Gemini image response: %w", err)
	}
	return len(images), canonical, ztapiGeminiImageUsagePendingReason(response.UsageMetadata), nil
}

func ztapiGeminiImageUsagePendingReason(usage dto.GeminiUsageMetadata) string {
	if usage.PromptTokenCount <= 0 || usage.CandidatesTokenCount <= 0 || usage.TotalTokenCount <= 0 ||
		usage.PromptTokenCount > math.MaxInt-usage.CandidatesTokenCount || usage.TotalTokenCount != usage.PromptTokenCount+usage.CandidatesTokenCount {
		return "Gemini native image usage aggregates are missing or inconsistent"
	}
	if usage.ToolUsePromptTokenCount != 0 || usage.ThoughtsTokenCount != 0 || usage.CachedContentTokenCount != 0 {
		return "Gemini native image usage contains unsupported billing dimensions"
	}
	if len(usage.PromptTokensDetails) != 1 || usage.PromptTokensDetails[0].Modality != "TEXT" ||
		usage.PromptTokensDetails[0].TokenCount != usage.PromptTokenCount {
		return "Gemini native image prompt usage detail is missing or inconsistent"
	}
	candidateTokenTotal := 0
	hasImageTokens := false
	for _, detail := range usage.CandidatesTokensDetails {
		if detail.TokenCount <= 0 || (detail.Modality != "IMAGE" && detail.Modality != "TEXT") ||
			candidateTokenTotal > math.MaxInt-detail.TokenCount {
			return "Gemini native image output usage detail is missing or inconsistent"
		}
		candidateTokenTotal += detail.TokenCount
		hasImageTokens = hasImageTokens || detail.Modality == "IMAGE"
	}
	if !hasImageTokens || candidateTokenTotal != usage.CandidatesTokenCount {
		return "Gemini native image output usage detail is missing or inconsistent"
	}
	return ""
}

func ztapiImageResponseRequestID(contract *types.ZTAPIImageProtocolContract, root gjson.Result, headers http.Header) (string, error) {
	field := contract.RequestIDField
	if contract.Version == types.ZTAPIImageProtocolContractVersionV2 || contract.Version == types.ZTAPIImageProtocolContractVersionV3 {
		if contract.RequestIDSource == types.ZTAPIResponseIDSourceHeader {
			var values []string
			for name, entries := range headers {
				if strings.EqualFold(name, contract.RequestIDKey) {
					values = append(values, entries...)
				}
			}
			if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
				return "", errors.New("managed image response request ID header is missing, blank, or ambiguous")
			}
			return values[0], nil
		}
		field = contract.RequestIDKey
	}
	value := root.Get(field)
	if !value.Exists() || value.Type != gjson.String || strings.TrimSpace(value.String()) == "" {
		return "", errors.New("managed image response request ID is missing or malformed")
	}
	return value.String(), nil
}

func readAndRestoreZTAPIManagedImageBody(resp *http.Response, limit int64) ([]byte, error) {
	if resp == nil || resp.Body == nil || limit < 0 {
		return nil, errors.New("managed image response body and non-negative limit are required")
	}
	original := resp.Body
	raw, err := io.ReadAll(io.LimitReader(original, limit+1))
	if err != nil {
		_ = original.Close()
		resp.Body = http.NoBody
		return nil, err
	}
	if int64(len(raw)) > limit {
		_ = original.Close()
		resp.Body = http.NoBody
		return nil, ErrZTAPIManagedImageResponseTooLarge
	}
	_ = original.Close()
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	resp.ContentLength = int64(len(raw))
	return raw, nil
}

func exactZTAPIImageJSONToken(raw []byte, result gjson.Result) ([]byte, bool) {
	if result.Raw == "" || result.Index < 0 || result.Index+len(result.Raw) > len(raw) {
		return nil, false
	}
	token := raw[result.Index : result.Index+len(result.Raw)]
	if !bytes.Equal(token, []byte(result.Raw)) {
		return nil, false
	}
	return token, true
}

func ztapiImageJSONPathAtOrBelow(path []string, field string) bool {
	parts := strings.Split(field, ".")
	if len(path) < len(parts) {
		return false
	}
	for index := range parts {
		if path[index] != parts[index] {
			return false
		}
	}
	return true
}

func validZTAPIImageResult(resultType, value string) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	switch resultType {
	case "url":
		parsed, err := url.Parse(value)
		return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" && parsed.User == nil
	case "b64_json":
		decoder := base64.NewDecoder(base64.StdEncoding, strings.NewReader(value))
		_, err := io.Copy(io.Discard, decoder)
		return err == nil
	default:
		return false
	}
}

func ztapiImageField(root any, path string) (any, bool) {
	current := root
	for _, segment := range strings.Split(path, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[segment]
		if !ok {
			return nil, false
		}
	}
	return current, true
}
