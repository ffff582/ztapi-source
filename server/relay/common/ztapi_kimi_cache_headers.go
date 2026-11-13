package common

import (
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

// ObserveZTAPIOpenAIChatUsage accepts K3 TTL evidence only from upstream Chat
// responses. Native Responses callers must keep using ObserveZTAPIOpenAIUsage.
func ObserveZTAPIOpenAIChatUsage(c *gin.Context, info *RelayInfo, raw []byte, header http.Header) *dto.Usage {
	if ztapiIsKimiK3(info) {
		if evidence := ensureZTAPIMetering(info); evidence != nil {
			five, hour, present, reason := parseZTAPIKimiCacheWriteHeaders(header)
			if present && reason == "" {
				evidence.KimiCacheWriteHeaders = &types.ZTAPIKimiCacheWriteHeaders{Tokens5m: five, Tokens1h: hour}
			}
		}
	}
	return observeZTAPIOpenAIUsage(c, info, raw, header)
}

func ztapiIsKimiK3(info *RelayInfo) bool {
	if info == nil || info.ZTAPIPublicationSnapshot == nil {
		return false
	}
	models := []string{info.OriginModelName, info.ZTAPIPublicationSnapshot.PublicName, info.ZTAPIPublicationSnapshot.SourceModel}
	if info.ChannelMeta != nil {
		models = append(models, info.UpstreamModelName)
	}
	for _, model := range models {
		if model == "kimi-k3" || model == "zt-kimi-k3" {
			return true
		}
	}
	return false
}

func parseZTAPIKimiCacheWriteHeaders(header http.Header) (five, hour int64, present bool, reason string) {
	read := func(name string) (int64, bool, string) {
		var values []string
		matches := 0
		for key, entries := range header {
			if strings.EqualFold(key, name) {
				matches++
				values = append(values, entries...)
			}
		}
		if matches == 0 {
			return 0, false, ""
		}
		if matches != 1 || len(values) != 1 || values[0] == "" {
			return 0, true, "usage_invalid_type"
		}
		for _, digit := range []byte(values[0]) {
			if digit < '0' || digit > '9' {
				return 0, true, "usage_invalid_type"
			}
		}
		value, err := strconv.ParseInt(values[0], 10, strconv.IntSize)
		if err != nil {
			return 0, true, "usage_invalid_quantity"
		}
		return value, true, ""
	}
	five, hasFive, fiveReason := read("Msh-Usage-Cache-Write-Tokens-5m")
	hour, hasHour, hourReason := read("Msh-Usage-Cache-Write-Tokens-1h")
	if fiveReason != "" {
		return 0, 0, true, fiveReason
	}
	if hourReason != "" {
		return 0, 0, true, hourReason
	}
	if hasFive != hasHour {
		return 0, 0, true, "usage_incomplete"
	}
	if five > int64(math.MaxInt)-hour {
		return 0, 0, true, "usage_invalid_quantity"
	}
	return five, hour, hasFive && hasHour, ""
}
