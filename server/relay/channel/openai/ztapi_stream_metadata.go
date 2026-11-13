package openai

import (
	"encoding/json"
	"time"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"
)

type ztapiStreamMetadata struct {
	id      string
	model   string
	created int64
}

func newZTAPIStreamMetadata(info *relaycommon.RelayInfo) *ztapiStreamMetadata {
	if info == nil || info.ZTAPIPublicationSnapshot == nil ||
		info.RelayMode != relayconstant.RelayModeChatCompletions || info.RelayFormat != types.RelayFormatOpenAI {
		return nil
	}
	created := info.StartTime.Unix()
	if info.StartTime.IsZero() || created <= 0 {
		created = time.Now().Unix()
	}
	return &ztapiStreamMetadata{
		id: "chatcmpl-ztapi-" + common.GetUUID(), model: info.OriginModelName, created: created,
	}
}

func (metadata *ztapiStreamMetadata) normalize(data string) (string, error) {
	if metadata == nil {
		return data, nil
	}
	var fields map[string]json.RawMessage
	if err := common.UnmarshalJsonStr(data, &fields); err != nil {
		return data, err
	}
	// Preserve error/non-chat frames; never manufacture a successful chunk.
	if common.GetJsonType(fields["choices"]) != "array" {
		return data, nil
	}
	changed := false
	for _, entry := range []struct {
		key   string
		value *string
	}{{"id", &metadata.id}, {"model", &metadata.model}} {
		var value string
		if err := common.Unmarshal(fields[entry.key], &value); err == nil && value != "" {
			*entry.value = value
			continue
		}
		encoded, err := common.Marshal(*entry.value)
		if err != nil {
			return data, err
		}
		fields[entry.key] = encoded
		changed = true
	}
	var created int64
	if err := common.Unmarshal(fields["created"], &created); err == nil && created > 0 {
		metadata.created = created
	} else {
		encoded, err := common.Marshal(metadata.created)
		if err != nil {
			return data, err
		}
		fields["created"] = encoded
		changed = true
	}
	if !changed {
		return data, nil
	}
	// RawMessage keeps provider extensions and large integers lossless.
	encoded, err := common.Marshal(fields)
	return string(encoded), err
}
