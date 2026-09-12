package relay

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

func ImageHelper(c *gin.Context, info *relaycommon.RelayInfo) (newAPIError *types.NewAPIError) {
	info.InitChannelMeta(c)
	managedImage := isZTAPIManagedImage(info)
	attemptID := BeginZTAPIManagedImageAttempt(info)
	committedManagedResponse := false
	if managedImage {
		defer func() {
			if !committedManagedResponse {
				AbortZTAPIManagedImageAttempt(info, attemptID)
			}
		}()
	}

	imageReq, ok := info.Request.(*dto.ImageRequest)
	if !ok {
		return types.NewErrorWithStatusCode(fmt.Errorf("invalid request type, expected dto.ImageRequest, got %T", info.Request), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}

	request, err := common.DeepCopy(imageReq)
	if err != nil {
		return types.NewError(fmt.Errorf("failed to copy request to ImageRequest: %w", err), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}

	var managedDispatch *relaycommon.ZTAPIManagedImageDispatch
	if managedImage {
		managedDispatch, newAPIError = PrepareZTAPIManagedImageDispatch(c, info)
		if newAPIError != nil {
			return newAPIError
		}
	} else {
		err = helper.ModelMappedHelper(c, info, request)
		if err != nil {
			return types.NewError(err, types.ErrorCodeChannelModelMappedError, types.ErrOptionWithSkipRetry())
		}
	}

	adaptor := GetAdaptor(info.ApiType)
	if adaptor == nil {
		return types.NewError(fmt.Errorf("invalid api type: %d", info.ApiType), types.ErrorCodeInvalidApiType, types.ErrOptionWithSkipRetry())
	}
	adaptor.Init(info)

	var requestBody io.Reader

	if managedImage {
		body, size, closer, err := relaycommon.NewOutboundJSONBody(managedDispatch.Body)
		if err != nil {
			return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
		}
		defer closer.Close()
		info.UpstreamRequestBodySize = size
		requestBody = body
	} else if model_setting.GetGlobalSettings().PassThroughRequestEnabled || info.ChannelSetting.PassThroughBodyEnabled {
		storage, err := common.GetBodyStorage(c)
		if err != nil {
			return types.NewErrorWithStatusCode(err, types.ErrorCodeReadRequestBodyFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
		requestBody = common.ReaderOnly(storage)
	} else {
		convertedRequest, err := adaptor.ConvertImageRequest(c, info, *request)
		if err != nil {
			return types.NewError(err, types.ErrorCodeConvertRequestFailed)
		}
		relaycommon.AppendRequestConversionFromRequest(info, convertedRequest)

		switch convertedRequest.(type) {
		case *bytes.Buffer:
			requestBody = convertedRequest.(io.Reader)
		default:
			jsonData, err := common.Marshal(convertedRequest)
			if err != nil {
				return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
			}

			// apply param override
			if len(info.ParamOverride) > 0 {
				jsonData, err = relaycommon.ApplyParamOverrideWithRelayInfo(jsonData, info)
				if err != nil {
					return newAPIErrorFromParamOverride(err)
				}
			}

			logger.LogDebug(c, "image request body: %s", jsonData)
			body, size, closer, err := relaycommon.NewOutboundJSONBody(jsonData)
			if err != nil {
				return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
			}
			defer closer.Close()
			jsonData = nil
			info.UpstreamRequestBodySize = size
			requestBody = body
		}
	}

	statusCodeMappingStr := c.GetString("status_code_mapping")

	resp, err := adaptor.DoRequest(c, info, requestBody)
	if err != nil {
		return types.NewOpenAIError(err, types.ErrorCodeDoRequestFailed, http.StatusInternalServerError)
	}
	var httpResp *http.Response
	if resp != nil {
		httpResp = resp.(*http.Response)
		upstreamStream := strings.HasPrefix(strings.ToLower(httpResp.Header.Get("Content-Type")), "text/event-stream")
		if managedImage && upstreamStream {
			if httpResp.Body != nil {
				_ = httpResp.Body.Close()
			}
			return ztapiImageUpstreamProtocolError(fmt.Errorf("managed image upstream returned an unauthorized stream"))
		}
		info.IsStream = info.IsStream || upstreamStream
		if httpResp.StatusCode != http.StatusOK {
			if httpResp.StatusCode == http.StatusCreated && info.ApiType == constant.APITypeReplicate {
				// replicate channel returns 201 Created when using Prefer: wait, treat it as success.
				httpResp.StatusCode = http.StatusOK
			} else {
				newAPIError = service.RelayErrorHandler(c.Request.Context(), httpResp, false)
				// reset status code 重置状态码
				service.ResetStatusCode(newAPIError, statusCodeMappingStr)
				return newAPIError
			}
		}
	}
	if managedImage {
		if _, newAPIError = ValidateZTAPIManagedImageResponse(info, httpResp); newAPIError != nil {
			return newAPIError
		}
	}

	usage, newAPIError := adaptor.DoResponse(c, httpResp, info)
	if newAPIError != nil {
		// reset status code 重置状态码
		service.ResetStatusCode(newAPIError, statusCodeMappingStr)
		return newAPIError
	}
	if managedImage {
		committedManagedResponse = true
		if service.IsZTAPIDurableBilling(info) {
			validated, evidence := info.GetZTAPIImageSettlementEvidence()
			switch {
			case validated == nil || evidence == nil:
				if err := service.PendZTAPIImageBilling(info, relaycommon.ZTAPIMediaUsageEvidence{}, "image_settlement_evidence_missing"); err != nil {
					logger.LogError(c, "managed image billing evidence handoff failed: "+err.Error())
				}
			case validated.UpstreamRequestID != evidence.UpstreamRequestID || validated.ResultCount != evidence.ResultCount || !evidence.ResultAvailable:
				if err := service.PendZTAPIImageBilling(info, *evidence, "image_settlement_evidence_conflict"); err != nil {
					logger.LogError(c, "managed image billing evidence conflict handoff failed: "+err.Error())
				}
			case evidence.Pending:
				if err := service.PendZTAPIImageBilling(info, *evidence, "image_usage_untrusted"); err != nil {
					logger.LogError(c, "managed image pending settlement handoff failed: "+err.Error())
				}
			default:
				if err := service.FinalizeZTAPIImageBilling(info, *evidence); err != nil {
					logger.LogError(c, "managed image settlement failed after delivery: "+err.Error())
					if pendingErr := service.PendZTAPIImageBilling(info, *evidence, "settlement_retry_required"); pendingErr != nil {
						logger.LogError(c, "managed image settlement recovery handoff failed: "+pendingErr.Error())
					}
				}
			}
		}
		return nil
	}

	imageN := uint(1)
	if request.N != nil {
		imageN = *request.N
	}

	// n is handled via OtherRatio so it is applied exactly once in quota
	// calculation (both price-based and ratio-based paths).
	// Adaptors may have already set a more accurate count from the
	// upstream response; only set the default when they haven't.
	if info.PriceData.UsePrice { // only price model use N ratio
		if _, hasN := info.PriceData.OtherRatios["n"]; !hasN {
			info.PriceData.AddOtherRatio("n", float64(imageN))
		}
	}

	if usage.(*dto.Usage).TotalTokens == 0 {
		usage.(*dto.Usage).TotalTokens = 1
	}
	if usage.(*dto.Usage).PromptTokens == 0 {
		usage.(*dto.Usage).PromptTokens = 1
	}

	quality := request.Quality
	if quality == "" {
		quality = "standard"
	}

	var logContent []string

	if len(request.Size) > 0 {
		logContent = append(logContent, fmt.Sprintf("大小 %s", request.Size))
	}
	if len(quality) > 0 {
		logContent = append(logContent, fmt.Sprintf("品质 %s", quality))
	}
	if imageN > 0 {
		logContent = append(logContent, fmt.Sprintf("生成数量 %d", imageN))
	}

	service.PostTextConsumeQuota(c, info, usage.(*dto.Usage), logContent)
	return nil
}
