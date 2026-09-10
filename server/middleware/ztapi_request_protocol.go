package middleware

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

func validateZTAPIRequestProtocol(c *gin.Context, channel *model.Channel, requestedModel string) *types.NewAPIError {
	publication := relaycommon.GetZTAPIPublicationSnapshot(c)
	if publication != nil {
		if publication.Modality == "image" {
			contract := publication.ImageProtocolContract
			if contract == nil || contract.ProviderModel != publication.SourceModel ||
				c.Request.Method != contract.Method || c.Request.URL.Path != contract.Path {
				return types.NewErrorWithStatusCode(errors.New("The managed image route does not match its frozen protocol contract."),
					"unsupported_model_endpoint", http.StatusBadRequest, types.ErrOptionWithSkipRetry())
			}
			if channel == nil || channel.Type != constant.ChannelTypeOpenAI {
				return types.NewErrorWithStatusCode(errors.New("The managed image publication requires its verified OpenAI Images provider family."),
					"unsupported_model_endpoint", http.StatusBadRequest, types.ErrOptionWithSkipRetry())
			}
			return nil
		}
		embedding := model.ZTAPIModelModality(publication.SourceModel) == model.ZTAPIModalityEmbedding
		if (embedding && c.Request.URL.Path != "/v1/embeddings") || (!embedding && c.Request.URL.Path == "/v1/embeddings") {
			return types.NewErrorWithStatusCode(errors.New("The requested endpoint does not match the published model modality; embedding models require /v1/embeddings."), "unsupported_model_endpoint", http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
		if embedding {
			var request struct {
				Input          any    `json:"input"`
				Stream         *bool  `json:"stream"`
				EncodingFormat string `json:"encoding_format"`
				Dimensions     *int   `json:"dimensions"`
			}
			err := common.UnmarshalBodyReusable(c, &request)
			if err == nil {
				_, err = common.ZTAPIEmbeddingInputCount(request.Input)
			}
			if err != nil || request.Stream != nil && *request.Stream || request.EncodingFormat != "" && request.EncodingFormat != "float" || request.Dimensions != nil && *request.Dimensions != 1536 {
				return types.NewErrorWithStatusCode(errors.New("This embedding route requires valid input, non-streaming float encoding and 1536 dimensions."), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
			}
		}
	}
	if publication == nil || !common.IsOpenAIResponseOnlyModel(publication.SourceModel) {
		return nil
	}
	if c.Request.URL.Path == "/v1/responses" {
		return nil
	}
	// Match the relay's actual conversion conditions, including the original
	// client alias. A source-only regex or body passthrough cannot enable it.
	switch c.Request.URL.Path {
	case "/v1/chat/completions", "/pg/chat/completions", "/v1/messages":
		settings := model_setting.GetGlobalSettings()
		if !settings.PassThroughRequestEnabled && !channel.GetSetting().PassThroughBodyEnabled &&
			service.ShouldChatCompletionsUseResponsesGlobal(channel.Id, channel.Type, requestedModel) {
			return nil
		}
	}
	return types.NewErrorWithStatusCode(errors.New("This model requires /v1/responses; the requested endpoint is not supported without an enabled Responses conversion."),
		"unsupported_model_endpoint", http.StatusBadRequest, types.ErrOptionWithSkipRetry())
}
