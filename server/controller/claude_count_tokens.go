package controller

import (
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// claudeImageTokenEstimate approximates one image the way Anthropic sizes a
// typical screenshot (about 1.15 megapixels at 750 pixels per token).
const claudeImageTokenEstimate = 1600

// CountClaudeMessageTokens answers Anthropic's /v1/messages/count_tokens with a
// local estimate. Claude Code calls it to size its context window; the request
// never reaches a supplier and nothing is billed.
func CountClaudeMessageTokens(c *gin.Context) {
	var request dto.ClaudeRequest
	if err := common.UnmarshalBodyReusable(c, &request); err != nil {
		abortWithClaudeError(c, http.StatusBadRequest, "invalid_request_error", "request body is not a valid Messages request")
		return
	}
	if strings.TrimSpace(request.Model) == "" {
		abortWithClaudeError(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}
	if len(request.Messages) == 0 {
		abortWithClaudeError(c, http.StatusBadRequest, "invalid_request_error", "messages is required")
		return
	}
	meta := request.GetTokenCountMeta()
	tokens := service.CountTextToken(meta.CombineText, request.Model)
	tools := request.GetTools()
	if meta.ToolsCount == 0 && len(tools) > 0 {
		// Decoded tools are plain maps, which the shared meta builder skips;
		// Claude Code sends a dozen large tool schemas on every turn.
		if encoded, err := common.Marshal(tools); err == nil {
			tokens += service.CountTextToken(string(encoded), request.Model)
		}
	}
	// Per-message and per-tool framing that the text alone does not show.
	tokens += 3*meta.MessagesCount + 8*len(tools) + claudeImageTokenEstimate*len(meta.Files)
	c.JSON(http.StatusOK, gin.H{"input_tokens": tokens})
}

func abortWithClaudeError(c *gin.Context, status int, errorType, message string) {
	c.JSON(status, gin.H{"type": "error", "error": gin.H{"type": errorType, "message": message}})
	c.Abort()
}
