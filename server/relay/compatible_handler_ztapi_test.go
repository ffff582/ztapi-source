package relay

import (
	"testing"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

func TestShouldUseAudioQuotaForZTAPIPublicationSnapshot(t *testing.T) {
	info := &relaycommon.RelayInfo{
		OriginModelName: "zt-realtime",
		ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{
			PublicName: "zt-realtime",
		},
	}
	usage := &dto.Usage{
		PromptTokensDetails: dto.InputTokenDetails{AudioTokens: 10},
	}

	if !shouldUseAudioQuota(info, usage) {
		t.Fatal("ZTAPI audio usage was routed through text quota settlement")
	}
}
