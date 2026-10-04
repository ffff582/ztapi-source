package gemini

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

// GeminiZTAPIImageHandler publishes only the canonical OpenAI Images-compatible
// representation prepared by the frozen native-response validator.
func GeminiZTAPIImageHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp != nil {
		defer service.CloseResponseBodyGracefully(resp)
	}
	dispatch := info.GetZTAPIManagedImageDispatch()
	if dispatch == nil || dispatch.WireProtocol != types.ZTAPIImageWireProtocolGeminiGenerateContent {
		return nil, types.NewErrorWithStatusCode(errors.New("frozen Gemini image dispatch is required"), types.ErrorCodeBadResponseBody, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
	}
	attemptID := info.CurrentZTAPIImageResponseAttemptID()
	accepted := false
	defer func() {
		if !accepted {
			info.AbortZTAPIImageResponseAttempt(attemptID)
		}
	}()
	if resp == nil || resp.Body == nil || attemptID == 0 {
		return nil, types.NewErrorWithStatusCode(errors.New("Gemini image response was not validated against its frozen contract"), types.ErrorCodeBadResponseBody, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewErrorWithStatusCode(err, types.ErrorCodeReadResponseBodyFailed, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
	}
	if !info.ZTAPIImageResponseMatchesCurrentAttempt(raw) {
		return nil, types.NewErrorWithStatusCode(errors.New("Gemini image response does not match the current validated attempt"), types.ErrorCodeBadResponseBody, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
	}
	candidate := info.CloneCurrentZTAPIImageResponseCandidate(raw)
	if candidate == nil || len(candidate.CanonicalResponse) == 0 {
		return nil, types.NewErrorWithStatusCode(errors.New("Gemini image canonical response is unavailable"), types.ErrorCodeBadResponseBody, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
	}
	evidence, normalizeErr := relaycommon.NormalizeZTAPIImageUsageCandidate(info, candidate, raw)
	if normalizeErr != nil && !errors.Is(normalizeErr, relaycommon.ErrZTAPIMediaUsagePending) {
		return nil, types.NewErrorWithStatusCode(normalizeErr, types.ErrorCodeBadResponseBody, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
	}
	if !info.PromoteZTAPIValidatedImageResponse(raw, evidence) {
		return nil, types.NewErrorWithStatusCode(fmt.Errorf("Gemini image adaptor could not accept the current validated attempt"), types.ErrorCodeBadResponseBody, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
	}
	accepted = true
	c.Writer.Header().Set("Content-Type", "application/json")
	c.Writer.WriteHeader(http.StatusOK)
	_, _ = c.Writer.Write(candidate.CanonicalResponse)
	return &dto.Usage{}, nil
}
