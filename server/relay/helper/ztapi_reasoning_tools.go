package helper

import (
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

// The verified Luna supplier needs Responses for function tools plus reasoning.
// Preserve both controls on the same frozen model/channel instead of dropping one.
func ZTAPIManagedToolsRequireResponses(info *relaycommon.RelayInfo, hasTools bool) bool {
	return hasTools && info != nil && info.ChannelMeta != nil && info.ApiType == constant.APITypeOpenAI &&
		info.ZTAPIPublicationSnapshot != nil && info.ZTAPIPublicationSnapshot.SourceModel == "gpt-5.6-luna"
}
