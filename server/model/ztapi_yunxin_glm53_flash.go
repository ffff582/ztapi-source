package model

import (
	"errors"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const ZTAPIGLM53FlashSource = "glm-5.3-flash"

func ValidateZTAPIEnterpriseGLM53FlashRoute(channelID int, baseURL, physicalModel string) error {
	if channelID != 1 || strings.TrimRight(baseURL, "/") != "https://ai.yunxinapi.com/hub" || physicalModel != ZTAPIGLM53FlashSource {
		return errors.New("yunxin_enterprise_glm53_flash_route_mismatch")
	}
	return nil
}

func RecordZTAPIEnterpriseGLM53FlashDiscovery(visible []string, operatorID int, fetchedAt time.Time) (*ZTAPIDiscoveryResult, error) {
	return recordZTAPIExactModelDiscovery(1, ZTAPIGLM53FlashSource, visible, operatorID, fetchedAt, func(channel *Channel) error {
		raw := strings.TrimSpace(channel.GetModelMapping())
		if raw != "" {
			var mapping map[string]string
			if err := common.UnmarshalJsonStr(raw, &mapping); err != nil {
				return err
			}
			if mapped := mapping[ZTAPIGLM53FlashSource]; mapped != "" && mapped != ZTAPIGLM53FlashSource {
				return errors.New("glm-5.3-flash discovery requires an exact physical model")
			}
		}
		return ValidateZTAPIEnterpriseGLM53FlashRoute(channel.Id, channel.GetBaseURL(), ZTAPIGLM53FlashSource)
	})
}
