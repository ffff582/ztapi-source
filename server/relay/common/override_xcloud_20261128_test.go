package common

import (
	"testing"

	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestXcloud20261128OverrideFreezesPhysicalModel(t *testing.T) {
	const source = "gpt-6.2-sol"
	for _, tc := range []struct {
		name, body string
		override   map[string]interface{}
		pass       bool
	}{
		{name: "legacy_change", override: map[string]interface{}{"model": "gpt-6.1-sol"}},
		{name: "legacy_type", override: map[string]interface{}{"model": 1}},
		{name: "legacy_null", override: map[string]interface{}{"model": nil}},
		{name: "case_alias", override: map[string]interface{}{"Model": source}},
		{name: "uppercase_alias", override: map[string]interface{}{"MODEL": source}},
		{name: "operations_set", override: map[string]interface{}{"operations": []map[string]interface{}{{"mode": "set", "path": "model", "value": "gpt-6.1-sol"}}}},
		{name: "operations_delete", override: map[string]interface{}{"operations": []map[string]interface{}{{"mode": "delete", "path": "model"}}}},
		{name: "operations_move", override: map[string]interface{}{"operations": []map[string]interface{}{{"mode": "move", "from": "model", "to": "other_model"}}}},
		{name: "duplicate_model", body: `{"model":"gpt-6.2-sol","model":"gpt-6.2-sol"}`, override: map[string]interface{}{"temperature": 0.5}},
		{name: "body_case_alias", body: `{"model":"gpt-6.2-sol","Model":"gpt-6.2-sol"}`, override: map[string]interface{}{"temperature": 0.5}},
		{name: "missing_model", body: `{"input":"hello"}`, override: map[string]interface{}{"temperature": 0.5}},
		{name: "same_model", override: map[string]interface{}{"model": source}, pass: true},
		{name: "other_parameter", override: map[string]interface{}{"temperature": 0.5}, pass: true},
		{name: "operations_same_model", override: map[string]interface{}{"operations": []map[string]interface{}{{"mode": "set", "path": "model", "value": source}}}, pass: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := tc.body
			if body == "" {
				body = `{"model":"gpt-6.2-sol","input":"hello"}`
			}
			info := &RelayInfo{
				ChannelMeta: &ChannelMeta{UpstreamModelName: source, ParamOverride: tc.override},
				ZTAPIPublicationSnapshot: &ZTAPIPublicationSnapshot{
					SourceModel: source, PricePolicy: types.ZTAPIXcloudPricePolicy20261128, AllowedChannelIDs: []int{7},
				},
			}
			frozen := info.ZTAPIPublicationSnapshot.Clone()
			out, err := ApplyParamOverrideWithRelayInfo([]byte(body), info)
			if tc.pass {
				require.NoError(t, err)
				require.Equal(t, source, gjson.GetBytes(out, "model").String())
				if tc.name == "other_parameter" {
					require.Equal(t, 0.5, gjson.GetBytes(out, "temperature").Float())
				}
			} else {
				require.ErrorContains(t, err, "supplier_admission_model_override_forbidden")
				require.Nil(t, out)
			}
			require.Equal(t, frozen, info.ZTAPIPublicationSnapshot)
		})
	}
}

func TestXcloud20261128OverrideRejectsMissingPhysicalModel(t *testing.T) {
	info := &RelayInfo{
		ChannelMeta: &ChannelMeta{ParamOverride: map[string]interface{}{"temperature": 0.5}},
		ZTAPIPublicationSnapshot: &ZTAPIPublicationSnapshot{
			SourceModel: "glm-5.3-flash", PricePolicy: types.ZTAPIXcloudPricePolicy20261128,
		},
	}
	out, err := ApplyParamOverrideWithRelayInfo([]byte(`{"model":"glm-5.3-flash"}`), info)
	require.ErrorContains(t, err, "supplier_admission_model_override_forbidden")
	require.Nil(t, out)
}

func TestXcloud20261128OverridePreservesLegacyBehavior(t *testing.T) {
	for _, tc := range []struct {
		policy, source string
		locked         bool
	}{
		{types.ZTAPISupplierAdmissionPricePolicy20261019, "gpt-6.1-sol", true},
		{types.ZTAPIXcloudPricePolicy20261114, "gpt-6-sol", true},
		{"reduction_15_12_20261107", "claude-fable-5", true},
		{"referral_floor_10_20261107", "claude-fable-5", true},
		{"enterprise_20_margin", "glm-5.3-flash", false},
		{"", "legacy-model", false},
	} {
		info := &RelayInfo{
			ChannelMeta:              &ChannelMeta{UpstreamModelName: tc.source, ParamOverride: map[string]interface{}{"model": "other-model"}},
			ZTAPIPublicationSnapshot: &ZTAPIPublicationSnapshot{SourceModel: tc.source, PricePolicy: tc.policy},
		}
		out, err := ApplyParamOverrideWithRelayInfo([]byte(`{"model":"`+tc.source+`"}`), info)
		if tc.locked {
			require.ErrorContains(t, err, "supplier_admission_model_override_forbidden")
			require.Nil(t, out)
		} else {
			require.NoError(t, err)
			require.Equal(t, "other-model", gjson.GetBytes(out, "model").String())
		}
	}
}
