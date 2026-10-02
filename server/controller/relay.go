package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/samber/lo"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func relayHandler(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {
	var err *types.NewAPIError
	switch info.RelayMode {
	case relayconstant.RelayModeImagesGenerations, relayconstant.RelayModeImagesEdits:
		err = relay.ImageHelper(c, info)
	case relayconstant.RelayModeAudioSpeech:
		fallthrough
	case relayconstant.RelayModeAudioTranslation:
		fallthrough
	case relayconstant.RelayModeAudioTranscription:
		err = relay.AudioHelper(c, info)
	case relayconstant.RelayModeRerank:
		err = relay.RerankHelper(c, info)
	case relayconstant.RelayModeEmbeddings:
		err = relay.EmbeddingHelper(c, info)
	case relayconstant.RelayModeResponses, relayconstant.RelayModeResponsesCompact:
		err = relay.ResponsesHelper(c, info)
	default:
		err = relay.TextHelper(c, info)
	}
	return err
}

func geminiRelayHandler(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {
	var err *types.NewAPIError
	if strings.Contains(c.Request.URL.Path, "embed") {
		err = relay.GeminiEmbeddingHandler(c, info)
	} else {
		err = relay.GeminiHelper(c, info)
	}
	return err
}

func validateZTAPIHealthProbeRoute(ctx context.Context, check relaycommon.ZTAPIHealthProbeRouteCheck) error {
	return model.ValidateZTAPIHealthVerificationDispatchPin(ctx, model.ZTAPIHealthVerificationRouteCheck{
		CaseID: check.CaseID, LeaseToken: check.LeaseToken, ProbeRequestID: check.ProbeRequestID,
		ModelID: check.ModelID, ChannelID: check.ChannelID, Protocol: check.Protocol,
		Stream: check.Stream, CredentialVersion: check.CredentialVersion, Generation: check.Generation,
	}, time.Now().UTC())
}

func Relay(c *gin.Context, relayFormat types.RelayFormat) {

	requestId := c.GetString(common.RequestIdKey)
	//group := common.GetContextKeyString(c, constant.ContextKeyUsingGroup)
	//originalModel := common.GetContextKeyString(c, constant.ContextKeyOriginalModel)

	var (
		newAPIError *types.NewAPIError
		ws          *websocket.Conn
	)

	if relayFormat == types.RelayFormatOpenAIRealtime {
		var err error
		ws, err = upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			helper.WssError(c, ws, types.NewError(err, types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry()).ToOpenAIError())
			return
		}
		defer ws.Close()
	}

	defer func() {
		if newAPIError != nil {
			logger.LogError(c, fmt.Sprintf("relay error: %s", common.LocalLogPreview(newAPIError.Error())))
			newAPIError.SetMessage(common.MessageWithRequestId(newAPIError.Error(), requestId))
			if relayFormat != types.RelayFormatOpenAIRealtime && c.Writer.Written() {
				return
			}
			switch relayFormat {
			case types.RelayFormatOpenAIRealtime:
				helper.WssError(c, ws, newAPIError.ToOpenAIError())
			case types.RelayFormatClaude:
				c.JSON(newAPIError.StatusCode, gin.H{
					"type":  "error",
					"error": newAPIError.ToClaudeError(),
				})
			default:
				c.JSON(newAPIError.StatusCode, gin.H{
					"error": newAPIError.ToOpenAIError(),
				})
			}
		}
	}()

	request, err := helper.GetAndValidateRequest(c, relayFormat)
	if err != nil {
		// Map "request body too large" to 413 so clients can handle it correctly
		if common.IsRequestBodyTooLargeError(err) || errors.Is(err, common.ErrRequestBodyTooLarge) {
			newAPIError = types.NewErrorWithStatusCode(err, types.ErrorCodeReadRequestBodyFailed, http.StatusRequestEntityTooLarge, types.ErrOptionWithSkipRetry())
		} else {
			newAPIError = types.NewError(err, types.ErrorCodeInvalidRequest)
		}
		return
	}

	relayInfo, err := relaycommon.GenRelayInfo(c, relayFormat, request, ws)
	if err != nil {
		newAPIError = types.NewError(err, types.ErrorCodeGenRelayInfoFailed)
		return
	}

	healthSession, err := relaycommon.StartZTAPIHealthRequest(c, relayInfo, relaycommon.ZTAPIHealthBackend{
		AdmitRequest:                       model.AdmitZTAPIHealthRequest,
		AdmitMediaRequest:                  model.AdmitZTAPIMediaHealthRequest,
		AdmitRequestWithEntryProtocol:      model.AdmitZTAPIHealthRequestWithEntryProtocol,
		AdmitMediaRequestWithEntryProtocol: model.AdmitZTAPIMediaHealthRequestWithEntryProtocol,
		AdmitAttempt:                       model.AdmitZTAPIHealthAttempt,
		RecordOutcome:                      model.RecordZTAPIHealthOutcome,
		CheckAvailable:                     model.CheckZTAPIHealthModelAvailable,
		ValidateProbeRoute:                 validateZTAPIHealthProbeRoute,
		CircuitOpen:                        model.ErrZTAPIHealthCircuitOpen,
		RouteOpen:                          model.ErrZTAPIHealthRouteOpen,
	})
	if err != nil {
		newAPIError = types.NewError(err, types.ErrorCode("model_temporarily_unavailable"), types.ErrOptionWithStatusCode(http.StatusServiceUnavailable), types.ErrOptionWithSkipRetry())
		return
	}
	defer func() {
		_ = healthSession.Finalize(c.Request.Context(), newAPIError != nil)
	}()

	needSensitiveCheck := setting.ShouldCheckPromptSensitive()
	needCountToken := constant.CountToken
	// Avoid building huge CombineText (strings.Join) when token counting and sensitive check are both disabled.
	var meta *types.TokenCountMeta
	if needSensitiveCheck || needCountToken {
		meta = request.GetTokenCountMeta()
	} else {
		meta = fastTokenCountMetaForPricing(request)
	}

	if needSensitiveCheck && meta != nil {
		contains, words := service.CheckSensitiveText(meta.CombineText)
		if contains {
			logger.LogWarn(c, fmt.Sprintf("user sensitive words detected: %s", strings.Join(words, ", ")))
			newAPIError = types.NewError(err, types.ErrorCodeSensitiveWordsDetected)
			return
		}
	}

	tokens, err := service.EstimateRequestToken(c, meta, relayInfo)
	if err != nil {
		newAPIError = types.NewError(err, types.ErrorCodeCountTokenFailed)
		return
	}

	relayInfo.SetEstimatePromptTokens(tokens)

	priceData, err := helper.ModelPriceHelper(c, relayInfo, tokens, meta)
	if err != nil {
		newAPIError = types.NewError(err, types.ErrorCodeModelPriceError, types.ErrOptionWithStatusCode(http.StatusBadRequest))
		return
	}

	// common.SetContextKey(c, constant.ContextKeyTokenCountMeta, meta)

	newAPIError = preConsumeAfterZTAPIImageAdmission(c, relayInfo, func() *types.NewAPIError {
		if priceData.FreeModel {
			logger.LogInfo(c, fmt.Sprintf("模型 %s 免费，跳过预扣费", relayInfo.OriginModelName))
			return nil
		}
		return service.PreConsumeBilling(c, priceData.QuotaToPreConsume, relayInfo)
	})
	if newAPIError != nil {
		return
	}

	defer func() {
		if finishErr := service.FinishZTAPIBilling(c, relayInfo); finishErr != nil {
			logger.LogError(c, "durable billing finalization requires reconciliation: "+finishErr.Error())
		}
		// Only return quota if downstream failed and quota was actually pre-consumed
		if newAPIError != nil {
			newAPIError = service.NormalizeViolationFeeError(newAPIError)
			if relayInfo.Billing != nil {
				if refundErr := relayInfo.Billing.Refund(c); refundErr != nil {
					logger.LogError(c, "failed to refund billing session: "+refundErr.Error())
				}
			}
			service.ChargeViolationFeeIfNeeded(c, relayInfo, newAPIError)
		}
	}()

	retryParam := newRelayRetryParam(c, relayInfo)
	relayInfo.RetryIndex = 0
	relayInfo.LastError = nil

	for ; retryParam.GetRetry() <= retryParam.RetryLimit(); retryParam.IncreaseRetry() {
		if healthSession != nil && !retryParam.Managed {
			healthSession.PrepareRelayAttempt()
		}
		relayInfo.RetryIndex = retryParam.GetRetry()
		channel, channelErr := getChannel(c, relayInfo, retryParam)
		if channelErr != nil {
			logger.LogError(c, channelErr.Error())
			if !retryParam.Managed || newAPIError == nil {
				newAPIError = channelErr
			}
			break
		}

		addUsedChannel(c, channel.Id)
		bodyStorage, bodyErr := common.GetBodyStorage(c)
		if bodyErr != nil {
			// Ensure consistent 413 for oversized bodies even when error occurs later (e.g., retry path)
			if common.IsRequestBodyTooLargeError(bodyErr) || errors.Is(bodyErr, common.ErrRequestBodyTooLarge) {
				newAPIError = types.NewErrorWithStatusCode(bodyErr, types.ErrorCodeReadRequestBodyFailed, http.StatusRequestEntityTooLarge, types.ErrOptionWithSkipRetry())
			} else {
				newAPIError = types.NewErrorWithStatusCode(bodyErr, types.ErrorCodeReadRequestBodyFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
			}
			break
		}
		c.Request.Body = io.NopCloser(bodyStorage)
		if healthSession != nil && retryParam.Managed {
			// Do not erase the last dispatched outcome if fallback selection failed.
			healthSession.PrepareRelayAttempt()
		}

		switch relayFormat {
		case types.RelayFormatOpenAIRealtime:
			newAPIError = relay.WssHelper(c, relayInfo)
		case types.RelayFormatClaude:
			newAPIError = relay.ClaudeHelper(c, relayInfo)
		case types.RelayFormatGemini:
			newAPIError = geminiRelayHandler(c, relayInfo)
		default:
			newAPIError = relayHandler(c, relayInfo)
		}

		if newAPIError == nil {
			relayInfo.LastError = nil
			return
		}

		newAPIError = service.NormalizeViolationFeeError(newAPIError)
		relayInfo.LastError = newAPIError

		channelError := *types.NewChannelError(channel.Id, channel.Type, channel.Name, channel.ChannelInfo.IsMultiKey, common.GetContextKeyString(c, constant.ContextKeyChannelKey), channel.GetAutoBan())
		processChannelErrorWithAutoBan(c, channelError, newAPIError, ztapiLegacyAutoBanAllowed(healthSession != nil, retryParam.Managed))
		if retryParam.Managed && ztapiRouteUnavailableError(newAPIError) {
			retryParam.ReleaseAttempt(channel.Id)
			retryParam.ResetRetryNextTry()
		}

		if !retryParam.HasMoreAttempts() || !shouldRetry(c, newAPIError, retryParam.RetryLimit()-retryParam.GetRetry()) {
			break
		}
	}

	useChannel := c.GetStringSlice("use_channel")
	if len(useChannel) > 1 {
		retryLogStr := fmt.Sprintf("重试：%s", strings.Trim(strings.Join(strings.Fields(fmt.Sprint(useChannel)), "->"), "[]"))
		logger.LogInfo(c, retryLogStr)
	}
	if newAPIError != nil {
		gopool.Go(func() {
			perfmetrics.RecordRelaySample(relayInfo, false, 0)
		})
	}
}

func preConsumeAfterZTAPIImageAdmission(c *gin.Context, info *relaycommon.RelayInfo, reserve func() *types.NewAPIError) *types.NewAPIError {
	return relay.WithZTAPIImageAdmission(c, info, reserve)
}

var upgrader = websocket.Upgrader{
	Subprotocols: []string{"realtime"}, // WS 握手支持的协议，如果有使用 Sec-WebSocket-Protocol，则必须在此声明对应的 Protocol TODO add other protocol
	CheckOrigin: func(r *http.Request) bool {
		return true // 允许跨域
	},
}

func addUsedChannel(c *gin.Context, channelId int) {
	useChannel := c.GetStringSlice("use_channel")
	useChannel = append(useChannel, fmt.Sprintf("%d", channelId))
	c.Set("use_channel", useChannel)
}

func fastTokenCountMetaForPricing(request dto.Request) *types.TokenCountMeta {
	if request == nil {
		return &types.TokenCountMeta{}
	}
	meta := &types.TokenCountMeta{
		TokenType: types.TokenTypeTokenizer,
	}
	switch r := request.(type) {
	case *dto.GeneralOpenAIRequest:
		maxCompletionTokens := lo.FromPtrOr(r.MaxCompletionTokens, uint(0))
		maxTokens := lo.FromPtrOr(r.MaxTokens, uint(0))
		if maxCompletionTokens > maxTokens {
			meta.MaxTokens = int(maxCompletionTokens)
		} else {
			meta.MaxTokens = int(maxTokens)
		}
	case *dto.OpenAIResponsesRequest:
		meta.MaxTokens = int(lo.FromPtrOr(r.MaxOutputTokens, uint(0)))
	case *dto.ClaudeRequest:
		meta.MaxTokens = int(lo.FromPtr(r.MaxTokens))
	case *dto.ImageRequest:
		// Pricing for image requests depends on ImagePriceRatio; safe to compute even when CountToken is disabled.
		return r.GetTokenCountMeta()
	default:
		// Best-effort: leave CombineText empty to avoid large allocations.
	}
	return meta
}

func getChannel(c *gin.Context, info *relaycommon.RelayInfo, retryParam *service.RetryParam) (*model.Channel, *types.NewAPIError) {
	if info.ChannelMeta == nil {
		autoBan := c.GetBool("auto_ban")
		autoBanInt := 1
		if !autoBan {
			autoBanInt = 0
		}
		channel := &model.Channel{
			Id:      c.GetInt("channel_id"),
			Type:    c.GetInt("channel_type"),
			Name:    c.GetString("channel_name"),
			AutoBan: &autoBanInt,
		}
		if err := retryParam.RecordAttempt(channel.Id); err != nil {
			return nil, types.NewError(err, types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
		}
		return channel, nil
	}
	for {
		channel, selectGroup, err := service.CacheGetRandomSatisfiedChannel(retryParam)

		info.PriceData.GroupRatioInfo = helper.HandleGroupRatio(c, info)

		if err != nil {
			return nil, types.NewError(fmt.Errorf("获取分组 %s 下模型 %s 的可用渠道失败（retry）: %s", selectGroup, info.OriginModelName, err.Error()), types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
		}
		if channel == nil {
			return nil, types.NewError(fmt.Errorf("分组 %s 下模型 %s 的可用渠道不存在（retry）", selectGroup, info.OriginModelName), types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
		}

		newAPIError := middleware.SetupContextForSelectedChannel(c, channel, info.OriginModelName)
		if newAPIError != nil {
			if retryParam.Managed && newAPIError.GetErrorCode() == types.ErrorCodeChannelNoAvailableKey {
				if recordErr := retryParam.RecordAttempt(channel.Id); recordErr != nil {
					return nil, types.NewError(recordErr, types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
				}
				if retryParam.HasMoreAttempts() {
					continue
				}
			}
			return nil, newAPIError
		}
		if err := retryParam.RecordAttempt(channel.Id); err != nil {
			return nil, types.NewError(err, types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
		}
		return channel, nil
	}
}

func newRelayRetryParam(c *gin.Context, info *relaycommon.RelayInfo) *service.RetryParam {
	selectionModel := info.SelectionModelName
	if selectionModel == "" {
		selectionModel = ratio_setting.FormatMatchingModelName(info.OriginModelName)
	}
	if selectionModel == "" {
		selectionModel = info.OriginModelName
	}
	allowedGroups := []string(nil)
	allowedChannelIDs := []int(nil)
	if info.ZTAPIPublicationSnapshot != nil {
		allowedGroups = append(allowedGroups, info.ZTAPIPublicationSnapshot.AllowedGroups...)
		allowedChannelIDs = append(allowedChannelIDs, info.ZTAPIPublicationSnapshot.AllowedChannelIDs...)
	}
	return &service.RetryParam{
		Managed:           info.ZTAPIPublicationSnapshot != nil,
		Ctx:               c,
		TokenGroup:        info.TokenGroup,
		ModelName:         selectionModel,
		AllowedGroups:     allowedGroups,
		AllowedChannelIDs: allowedChannelIDs,
		Retry:             common.GetPointer(0),
	}
}

func shouldRetry(c *gin.Context, openaiErr *types.NewAPIError, retryTimes int) bool {
	if c.Writer.Written() || c.Request.Context().Err() != nil {
		return false
	}
	if openaiErr == nil {
		return false
	}
	managed := relaycommon.GetZTAPIPublicationSnapshot(c) != nil
	if managed {
		if retryTimes <= 0 || types.IsSkipRetryError(openaiErr) || !ztapiRetryableError(openaiErr) {
			return false
		}
		if _, specific := c.Get("specific_channel_id"); specific {
			return false
		}
	}
	if !ztapiRouteUnavailableError(openaiErr) && service.ShouldSkipRetryAfterChannelAffinityFailure(c) {
		return false
	}
	if managed {
		return true
	}
	if types.IsChannelError(openaiErr) {
		return true
	}
	if types.IsSkipRetryError(openaiErr) {
		return false
	}
	if retryTimes <= 0 {
		return false
	}
	if _, ok := c.Get("specific_channel_id"); ok {
		return false
	}
	code := openaiErr.StatusCode
	if code >= 200 && code < 300 {
		return false
	}
	if code < 100 || code > 599 {
		return true
	}
	if operation_setting.IsAlwaysSkipRetryCode(openaiErr.GetErrorCode()) {
		return false
	}
	return operation_setting.ShouldRetryByStatusCode(code)
}

func ztapiRouteUnavailableError(err *types.NewAPIError) bool {
	return err != nil && err.GetErrorCode() == types.ErrorCode("ztapi_route_temporarily_unavailable")
}

func ztapiRetryableError(err *types.NewAPIError) bool {
	if operation_setting.IsAlwaysSkipRetryCode(err.GetErrorCode()) || operation_setting.IsAlwaysSkipRetryStatusCode(err.StatusCode) {
		return false
	}
	wire := err.ToOpenAIError()
	upstreamFault := false
	switch strings.ToLower(strings.TrimSpace(fmt.Sprint(wire.Code))) {
	case "invalid_api_key", "authentication_error", "unauthenticated", "invalid_key", "key_expired", "key_disabled",
		"model_not_granted", "insufficient_quota", "resource_exhausted", "billing_hard_limit_reached",
		"insufficient_balance", "credit_balance_too_low", "quota_exceeded", "rate_limit_exceeded", "rate_limit_error", "too_many_requests":
		upstreamFault = true
	}
	for _, code := range []string{string(err.GetErrorCode()), fmt.Sprint(wire.Code), wire.Type} {
		switch strings.ToLower(strings.TrimSpace(code)) {
		case "invalid_request", "invalid_request_error":
			if !upstreamFault {
				return false
			}
		case "invalid_argument", "invalid_parameter", "invalid_parameters", "context_length_exceeded", "bad_request", "bad_request_body",
			"convert_request_failed", "read_request_body_failed", "channel:param_override_invalid", "channel:header_override_invalid",
			"sensitive_words_detected", "prompt_blocked", "content_filter", "content_policy_violation", "policy_violation",
			"safety", "safety_refusal", "refusal", "violation_fee.grok.csam":
			return false
		}
	}
	if err.StatusCode >= 400 && err.StatusCode < 500 {
		switch err.StatusCode {
		case 401, 402, 429:
			return true
		case 403:
			return fmt.Sprint(wire.Code) == "model_not_granted"
		default:
			return false
		}
	}
	return err.StatusCode >= 500 && err.StatusCode <= 599
}

func processChannelError(c *gin.Context, channelError types.ChannelError, err *types.NewAPIError) {
	processChannelErrorWithAutoBan(c, channelError, err, true)
}

func ztapiLegacyAutoBanAllowed(hasHealthSession, managed bool) bool {
	return !hasHealthSession || !managed
}

func processChannelErrorWithAutoBan(c *gin.Context, channelError types.ChannelError, err *types.NewAPIError, allowAutoBan bool) {
	logger.LogError(c, fmt.Sprintf("channel error (channel #%d, status code: %d): %s", channelError.ChannelId, err.StatusCode, common.LocalLogPreview(err.Error())))
	// 不要使用context获取渠道信息，异步处理时可能会出现渠道信息不一致的情况
	// do not use context to get channel info, there may be inconsistent channel info when processing asynchronously
	if allowAutoBan && service.ShouldDisableChannel(err) && channelError.AutoBan {
		gopool.Go(func() {
			service.DisableChannel(channelError, err.ErrorWithStatusCode())
		})
	}

	if constant.ErrorLogEnabled && types.IsRecordErrorLog(err) {
		// 保存错误日志到mysql中
		userId := c.GetInt("id")
		tokenName := c.GetString("token_name")
		modelName := c.GetString("original_model")
		tokenId := c.GetInt("token_id")
		userGroup := c.GetString("group")
		channelId := c.GetInt("channel_id")
		other := make(map[string]interface{})
		if c.Request != nil && c.Request.URL != nil {
			other["request_path"] = c.Request.URL.Path
		}
		other["error_type"] = err.GetErrorType()
		other["error_code"] = err.GetErrorCode()
		other["status_code"] = err.StatusCode
		other["channel_id"] = channelId
		other["channel_name"] = c.GetString("channel_name")
		other["channel_type"] = c.GetInt("channel_type")
		adminInfo := make(map[string]interface{})
		adminInfo["use_channel"] = c.GetStringSlice("use_channel")
		isMultiKey := common.GetContextKeyBool(c, constant.ContextKeyChannelIsMultiKey)
		if isMultiKey {
			adminInfo["is_multi_key"] = true
			adminInfo["multi_key_index"] = common.GetContextKeyInt(c, constant.ContextKeyChannelMultiKeyIndex)
		}
		service.AppendChannelAffinityAdminInfo(c, adminInfo)
		other["admin_info"] = adminInfo
		startTime := common.GetContextKeyTime(c, constant.ContextKeyRequestStartTime)
		if startTime.IsZero() {
			startTime = time.Now()
		}
		useTimeSeconds := int(time.Since(startTime).Seconds())
		model.RecordErrorLog(c, userId, channelId, modelName, tokenName, err.MaskSensitiveErrorWithStatusCode(), tokenId, useTimeSeconds, common.GetContextKeyBool(c, constant.ContextKeyIsStream), userGroup, other)
	}

}

func RelayMidjourney(c *gin.Context) {
	relayInfo, err := relaycommon.GenRelayInfo(c, types.RelayFormatMjProxy, nil, nil)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"description": fmt.Sprintf("failed to generate relay info: %s", err.Error()),
			"type":        "upstream_error",
			"code":        4,
		})
		return
	}

	var mjErr *dto.MidjourneyResponse
	switch relayInfo.RelayMode {
	case relayconstant.RelayModeMidjourneyNotify:
		mjErr = relay.RelayMidjourneyNotify(c)
	case relayconstant.RelayModeMidjourneyTaskFetch, relayconstant.RelayModeMidjourneyTaskFetchByCondition:
		mjErr = relay.RelayMidjourneyTask(c, relayInfo.RelayMode)
	case relayconstant.RelayModeMidjourneyTaskImageSeed:
		mjErr = relay.RelayMidjourneyTaskImageSeed(c)
	case relayconstant.RelayModeSwapFace:
		mjErr = relay.RelaySwapFace(c, relayInfo)
	default:
		mjErr = relay.RelayMidjourneySubmit(c, relayInfo)
	}
	//err = relayMidjourneySubmit(c, relayMode)
	log.Println(mjErr)
	if mjErr != nil {
		statusCode := http.StatusBadRequest
		if mjErr.Code == 30 {
			mjErr.Result = "当前分组负载已饱和，请稍后再试，或升级账户以提升服务质量。"
			statusCode = http.StatusTooManyRequests
		}
		c.JSON(statusCode, gin.H{
			"description": fmt.Sprintf("%s %s", mjErr.Description, mjErr.Result),
			"type":        "upstream_error",
			"code":        mjErr.Code,
		})
		channelId := c.GetInt("channel_id")
		logger.LogError(c, fmt.Sprintf("relay error (channel #%d, status code %d): %s", channelId, statusCode, fmt.Sprintf("%s %s", mjErr.Description, mjErr.Result)))
	}
}

func RelayNotImplemented(c *gin.Context) {
	err := types.OpenAIError{
		Message: "API not implemented",
		Type:    "new_api_error",
		Param:   "",
		Code:    "api_not_implemented",
	}
	c.JSON(http.StatusNotImplemented, gin.H{
		"error": err,
	})
}

func RelayNotFound(c *gin.Context) {
	err := types.OpenAIError{
		Message: fmt.Sprintf("Invalid URL (%s %s)", c.Request.Method, c.Request.URL.Path),
		Type:    "invalid_request_error",
		Param:   "",
		Code:    "",
	}
	c.JSON(http.StatusNotFound, gin.H{
		"error": err,
	})
}

func RelayTaskFetch(c *gin.Context) {
	relayInfo, err := relaycommon.GenRelayInfo(c, types.RelayFormatTask, nil, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, &dto.TaskError{
			Code:       "gen_relay_info_failed",
			Message:    err.Error(),
			StatusCode: http.StatusInternalServerError,
		})
		return
	}
	if taskErr := relay.RelayTaskFetch(c, relayInfo.RelayMode); taskErr != nil {
		respondTaskError(c, taskErr)
	}
}

func RelayTask(c *gin.Context) {
	relayInfo, err := relaycommon.GenRelayInfo(c, types.RelayFormatTask, nil, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, &dto.TaskError{
			Code:       "gen_relay_info_failed",
			Message:    err.Error(),
			StatusCode: http.StatusInternalServerError,
		})
		return
	}

	if taskErr := relay.ResolveOriginTask(c, relayInfo); taskErr != nil {
		respondTaskError(c, taskErr)
		return
	}
	var taskErr *dto.TaskError
	var healthSession *relaycommon.ZTAPIHealthSession
	if snapshot := relayInfo.ZTAPIPublicationSnapshot; snapshot != nil && snapshot.Modality == model.ZTAPIModalityVideo {
		if relayInfo.OriginModelName == "" {
			relayInfo.OriginModelName = snapshot.PublicName
		}
		healthSession, err = relaycommon.StartZTAPIHealthRequest(c, relayInfo, relaycommon.ZTAPIHealthBackend{
			AdmitRequest:                       model.AdmitZTAPIHealthRequest,
			AdmitMediaRequest:                  model.AdmitZTAPIMediaHealthRequest,
			AdmitRequestWithEntryProtocol:      model.AdmitZTAPIHealthRequestWithEntryProtocol,
			AdmitMediaRequestWithEntryProtocol: model.AdmitZTAPIMediaHealthRequestWithEntryProtocol,
			AdmitAttempt:                       model.AdmitZTAPIHealthAttempt,
			RecordOutcome:                      model.RecordZTAPIHealthOutcome,
			CheckAvailable:                     model.CheckZTAPIHealthModelAvailable,
			ValidateProbeRoute:                 validateZTAPIHealthProbeRoute,
			CircuitOpen:                        model.ErrZTAPIHealthCircuitOpen,
			RouteOpen:                          model.ErrZTAPIHealthRouteOpen,
		})
		if err != nil {
			respondTaskError(c, service.TaskErrorWrapperLocal(err, "model_temporarily_unavailable", http.StatusServiceUnavailable))
			return
		}
		defer func() { _ = healthSession.Finalize(c.Request.Context(), taskErr != nil) }()
	}

	defer func() {
		if taskErr != nil && relayInfo.Billing != nil {
			if refundErr := relayInfo.Billing.Refund(c); refundErr != nil {
				logger.LogError(c, "failed to refund task billing session: "+refundErr.Error())
			}
		}
	}()

	var lockedChannel *model.Channel
	if relayInfo.LockedChannel != nil {
		lockedChannel, _ = relayInfo.LockedChannel.(*model.Channel)
	}
	result, taskErr := executeRelayTaskAttempts(c, relayInfo, lockedChannel, relay.RelayTaskSubmit)

	// -- success: settle, log and persist the public task --
	if taskErr == nil {
		managedMedia := service.IsZTAPIMediaBilling(relayInfo)
		if !managedMedia {
			if settleErr := service.SettleBilling(c, relayInfo, result.Quota); settleErr != nil {
				common.SysError("settle task billing error: " + settleErr.Error())
			}
			service.LogTaskConsumption(c, relayInfo)
		}

		task := model.InitTask(result.Platform, relayInfo)
		task.PrivateData.UpstreamTaskID = result.UpstreamTaskID
		task.PrivateData.BillingSource = relayInfo.BillingSource
		task.PrivateData.SubscriptionId = relayInfo.SubscriptionId
		task.PrivateData.TokenId = relayInfo.TokenId
		task.PrivateData.BillingContext = &model.TaskBillingContext{
			ModelPrice:      relayInfo.PriceData.ModelPrice,
			GroupRatio:      relayInfo.PriceData.GroupRatioInfo.GroupRatio,
			ModelRatio:      relayInfo.PriceData.ModelRatio,
			OtherRatios:     relayInfo.PriceData.OtherRatios,
			OriginModelName: relayInfo.OriginModelName,
			PerCallBilling:  common.StringsContains(constant.TaskPricePatches, relayInfo.OriginModelName) || relayInfo.PriceData.UsePrice,
		}
		task.Quota = result.Quota
		task.Data = persistedTaskData(managedMedia, result.TaskData)
		task.Action = relayInfo.Action
		if managedMedia {
			if tagErr := service.TagZTAPIMediaLegacyTask(task, relayInfo); tagErr != nil {
				common.SysError("tag durable media task error: " + tagErr.Error())
				return
			}
		}
		if insertErr := task.Insert(); insertErr != nil {
			common.SysError("insert task error: " + insertErr.Error())
		}
	}

	if taskErr != nil {
		respondTaskError(c, taskErr)
	}
}

type relayTaskSubmitFunc func(*gin.Context, *relaycommon.RelayInfo) (*relay.TaskSubmitResult, *dto.TaskError)

func executeRelayTaskAttempts(c *gin.Context, relayInfo *relaycommon.RelayInfo, lockedChannel *model.Channel, submit relayTaskSubmitFunc) (*relay.TaskSubmitResult, *dto.TaskError) {
	retryParam := newRelayRetryParam(c, relayInfo)
	var result *relay.TaskSubmitResult
	var taskErr *dto.TaskError
	for ; retryParam.GetRetry() <= retryParam.RetryLimit(); retryParam.IncreaseRetry() {
		var channel *model.Channel

		if lockedChannel != nil {
			channel = lockedChannel
			if retryParam.GetRetry() > 0 {
				if setupErr := middleware.SetupContextForSelectedChannel(c, channel, relayInfo.OriginModelName); setupErr != nil {
					taskErr = service.TaskErrorWrapperLocal(setupErr.Err, "setup_locked_channel_failed", http.StatusInternalServerError)
					break
				}
			}
		} else {
			var channelErr *types.NewAPIError
			channel, channelErr = getChannel(c, relayInfo, retryParam)
			if channelErr != nil {
				logger.LogError(c, channelErr.Error())
				taskErr = service.TaskErrorWrapperLocal(channelErr.Err, "get_channel_failed", http.StatusInternalServerError)
				break
			}
		}

		addUsedChannel(c, channel.Id)
		bodyStorage, bodyErr := common.GetBodyStorage(c)
		if bodyErr != nil {
			if common.IsRequestBodyTooLargeError(bodyErr) || errors.Is(bodyErr, common.ErrRequestBodyTooLarge) {
				taskErr = service.TaskErrorWrapperLocal(bodyErr, "read_request_body_failed", http.StatusRequestEntityTooLarge)
			} else {
				taskErr = service.TaskErrorWrapperLocal(bodyErr, "read_request_body_failed", http.StatusBadRequest)
			}
			break
		}
		c.Request.Body = io.NopCloser(bodyStorage)

		result, taskErr = submit(c, relayInfo)
		if taskErr == nil {
			break
		}

		if !taskErr.LocalError {
			processChannelError(c,
				*types.NewChannelError(channel.Id, channel.Type, channel.Name, channel.ChannelInfo.IsMultiKey,
					common.GetContextKeyString(c, constant.ContextKeyChannelKey), channel.GetAutoBan()),
				types.NewOpenAIError(taskErr.Error, types.ErrorCodeBadResponseStatusCode, taskErr.StatusCode))
		}

		if prepareZTAPITaskRouteRetry(c, retryParam, channel.Id, taskErr) {
			continue
		}
		if !shouldRetryTaskRelay(c, channel.Id, taskErr, retryParam.RetryLimit()-retryParam.GetRetry()) {
			break
		}
	}

	useChannel := c.GetStringSlice("use_channel")
	if len(useChannel) > 1 {
		retryLogStr := fmt.Sprintf("重试：%s", strings.Trim(strings.Join(strings.Fields(fmt.Sprint(useChannel)), "->"), "[]"))
		logger.LogInfo(c, retryLogStr)
	}
	return result, taskErr
}

func persistedTaskData(managedMedia bool, upstreamData []byte) []byte {
	if managedMedia {
		return []byte("{}")
	}
	return upstreamData
}

// respondTaskError 统一输出 Task 错误响应（含 429 限流提示改写）
func respondTaskError(c *gin.Context, taskErr *dto.TaskError) {
	if taskErr.StatusCode == http.StatusTooManyRequests {
		taskErr.Message = "当前分组上游负载已饱和，请稍后再试"
	}
	c.JSON(taskErr.StatusCode, taskErr)
}

func shouldRetryTaskRelay(c *gin.Context, channelId int, taskErr *dto.TaskError, retryTimes int) bool {
	if taskErr == nil {
		return false
	}
	if relaycommon.ZTAPITaskProviderAccepted(c) {
		return false
	}
	routeUnavailable := taskErr.Code == "ztapi_route_temporarily_unavailable"
	if !routeUnavailable && service.ShouldSkipRetryAfterChannelAffinityFailure(c) {
		return false
	}
	if retryTimes <= 0 {
		return false
	}
	if _, ok := c.Get("specific_channel_id"); ok {
		return false
	}
	if taskErr.LocalError {
		return routeUnavailable
	}
	if taskErr.StatusCode == http.StatusTooManyRequests {
		return true
	}
	if taskErr.StatusCode == 307 {
		return true
	}
	if taskErr.StatusCode/100 == 5 {
		// 超时不重试
		if operation_setting.IsAlwaysSkipRetryStatusCode(taskErr.StatusCode) {
			return false
		}
		return true
	}
	if taskErr.StatusCode == http.StatusBadRequest {
		return false
	}
	if taskErr.StatusCode == 408 {
		// azure处理超时不重试
		return false
	}
	if taskErr.StatusCode/100 == 2 {
		return false
	}
	return true
}

func prepareZTAPITaskRouteRetry(c *gin.Context, retryParam *service.RetryParam, channelID int, taskErr *dto.TaskError) bool {
	if retryParam == nil || !retryParam.Managed || taskErr == nil || taskErr.Code != "ztapi_route_temporarily_unavailable" {
		return false
	}
	if !shouldRetryTaskRelay(c, channelID, taskErr, retryParam.RetryLimit()-retryParam.GetRetry()) {
		return false
	}
	retryParam.ReleaseAttempt(channelID)
	retryParam.ResetRetryNextTry()
	return true
}
