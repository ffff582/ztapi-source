package common

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	basecommon "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

type ZTAPIMeteringEvidence = types.ZTAPIMeteringEvidence

func ensureZTAPIMetering(info *RelayInfo) *ZTAPIMeteringEvidence {
	if info == nil || info.ZTAPIPublicationSnapshot == nil ||
		(info.ZTAPIPublicationSnapshot.Modality != "" && info.ZTAPIPublicationSnapshot.Modality != "text") {
		return nil
	}
	if info.ZTAPIMetering == nil {
		info.ZTAPIMetering = &ZTAPIMeteringEvidence{Version: 1}
	}
	return info.ZTAPIMetering
}

func CaptureZTAPIRequestMetering(c *gin.Context, info *RelayInfo, meta *types.TokenCountMeta) {
	evidence := ensureZTAPIMetering(info)
	if evidence == nil {
		return
	}
	evidence.EntryProtocol = string(info.RelayFormat)
	evidence.EstimatedInputTokens = info.GetEstimatePromptTokens()
	if meta != nil {
		evidence.Messages, evidence.Tools = meta.MessagesCount, meta.ToolsCount
		evidence.CombinedTextBytes, evidence.MaxOutputTokens = len(meta.CombineText), meta.MaxTokens
	}
	var tools any
	switch request := info.Request.(type) {
	case *dto.ClaudeRequest:
		evidence.Messages = len(request.Messages)
		list := request.GetTools()
		evidence.Tools, tools = len(list), list
	case *dto.GeneralOpenAIRequest:
		evidence.Messages = len(request.Messages)
		evidence.Tools, tools = len(request.Tools), request.Tools
	case *dto.OpenAIResponsesRequest:
		var declarations []json.RawMessage
		_ = basecommon.Unmarshal(request.Tools, &declarations)
		input := bytes.TrimSpace(request.Input)
		if len(input) > 0 && input[0] == '"' {
			evidence.Messages = 1
		} else {
			var items []struct {
				Type  string          `json:"type"`
				Role  string          `json:"role"`
				Tools json.RawMessage `json:"tools"`
			}
			if basecommon.Unmarshal(input, &items) == nil {
				for _, item := range items {
					if item.Role != "" || item.Type == "message" {
						evidence.Messages++
					}
					if item.Type == "additional_tools" {
						var extra []json.RawMessage
						if basecommon.Unmarshal(item.Tools, &extra) == nil {
							declarations = append(declarations, extra...)
						}
					}
				}
			}
		}
		evidence.Tools, tools = len(declarations), declarations
	}
	if evidence.Tools > 0 {
		if encoded, err := basecommon.Marshal(tools); err == nil {
			evidence.ToolDefinitionBytes = len(encoded)
		}
		evidence.ToolsMissingEstimate = meta != nil && meta.ToolsCount == 0
	}
	if c != nil && c.Request != nil {
		if storage, err := basecommon.GetBodyStorage(c); err == nil {
			size := storage.Size()
			evidence.EntryBodyBytes = &size
		}
	}
}

func CaptureZTAPIOutboundRequestMetering(info *RelayInfo, size int64) {
	evidence := ensureZTAPIMetering(info)
	if evidence == nil {
		return
	}
	evidence.OutboundBodyBytes = nil
	if size >= 0 {
		evidence.OutboundBodyBytes = &size
	}
	evidence.UpstreamProtocol = string(info.GetFinalRequestRelayFormat())
	// A retry's missing usage must not inherit a previous attempt's evidence.
	evidence.RawUsage, evidence.RawUsageSHA256 = nil, ""
	evidence.KimiCacheWriteHeaders = nil
	evidence.UsageSource, evidence.UsageLocation = "", ""
	evidence.UsageObservations = 0
	evidence.UsageIntegrityComplete = false
	evidence.UpstreamTerminal, evidence.DownstreamWriteState = "", ""
	evidence.UpstreamFailure = ""
	info.ZTAPIUsageLastSnapshot = nil
}

func CaptureZTAPIResponseCompletion(info *RelayInfo, terminal, writeState string) {
	if evidence := ensureZTAPIMetering(info); evidence != nil {
		evidence.UpstreamTerminal, evidence.DownstreamWriteState = terminal, writeState
	}
}

func CaptureZTAPIUpstreamUsageEvidence(info *RelayInfo, raw []byte, location string) {
	if info == nil || info.ZTAPIPublicationSnapshot == nil {
		return
	}
	var frame struct {
		Usage    json.RawMessage `json:"usage"`
		Response *struct {
			Usage json.RawMessage `json:"usage"`
		} `json:"response"`
	}
	if basecommon.Unmarshal(raw, &frame) != nil {
		return
	}
	usage := frame.Usage
	if len(usage) == 0 && frame.Response != nil {
		usage = frame.Response.Usage
	}
	if len(usage) == 0 || len(usage) > 65536 {
		return
	}
	filtered := types.FilterZTAPINumericUsage(usage)
	if len(filtered) == 0 {
		return
	}
	safe, err := basecommon.Marshal(filtered)
	if err != nil {
		return
	}
	evidence := ensureZTAPIMetering(info)
	if evidence == nil {
		return
	}
	evidence.UsageSource = "upstream_reported"
	evidence.UpstreamProtocol = string(info.GetFinalRequestRelayFormat())
	evidence.UsageLocation = location
	evidence.UsageObservations++
	evidence.RawUsage = safe
	hash := sha256.Sum256(safe)
	evidence.RawUsageSHA256 = hex.EncodeToString(hash[:])
}
