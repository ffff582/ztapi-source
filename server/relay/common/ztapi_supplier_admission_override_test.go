package common

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSupplierAdmissionOverrideCannotChangeBilledSKU(t *testing.T) {
	for _, tc := range []struct {
		name      string
		policy    string
		override  map[string]interface{}
		wantError bool
	}{
		{"model_change", "supplier_admission_20261019", map[string]interface{}{"model": "gpt-6-sol-hc"}, true},
		{"model_type", "supplier_admission_20261019", map[string]interface{}{"model": 1}, true},
		{"case_alias_changes_model", "supplier_admission_20261019", map[string]interface{}{"Model": "gpt-6-sol-hc"}, true},
		{"case_alias_even_if_equal", "supplier_admission_20261019", map[string]interface{}{"Model": "gpt-6.1-sol-hc"}, true},
		{"uppercase_alias", "supplier_admission_20261019", map[string]interface{}{"MODEL": "gpt-6-sol-hc"}, true},
		{"exact_model", "supplier_admission_20261019", map[string]interface{}{"model": "gpt-6.1-sol-hc"}, false},
		{"other_parameter", "supplier_admission_20261019", map[string]interface{}{"temperature": 0.5}, false},
		{"xcloud_model_change", "xcloud_verified_20261114", map[string]interface{}{"model": "gpt-6-sol"}, true},
		{"xcloud_exact_model", "xcloud_verified_20261114", map[string]interface{}{"model": "gpt-6.1-sol-hc"}, false},
		{"legacy_unchanged", "", map[string]interface{}{"model": "legacy-private-model"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := &RelayInfo{
				ChannelMeta:              &ChannelMeta{UpstreamModelName: "gpt-6.1-sol-hc", ParamOverride: tc.override},
				ZTAPIPublicationSnapshot: &ZTAPIPublicationSnapshot{PricePolicy: tc.policy},
			}
			_, err := ApplyParamOverrideWithRelayInfo([]byte(`{"model":"gpt-6.1-sol-hc","input":"hello"}`), info)
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestFableOverridePreservesBilledModel(t *testing.T) {
	for _, policy := range []string{"reduction_15_12_20261107", "referral_floor_10_20261107"} {
		t.Run(policy, func(t *testing.T) {
			info := &RelayInfo{
				ChannelMeta:              &ChannelMeta{UpstreamModelName: "claude-fable-5-hc"},
				ZTAPIPublicationSnapshot: &ZTAPIPublicationSnapshot{SourceModel: "claude-fable-5", PricePolicy: policy},
			}
			for _, body := range []string{
				`{"model":"claude-fable-5-hc","messages":[]}`,
				`{"model":"claude-fable-5-hc","input":"hello"}`,
			} {
				info.ParamOverride = map[string]interface{}{"model": "claude-fable-5-1"}
				_, err := ApplyParamOverrideWithRelayInfo([]byte(body), info)
				require.Error(t, err)
				info.ParamOverride = map[string]interface{}{"operations": []map[string]interface{}{{"mode": "delete", "path": "model"}}}
				_, err = ApplyParamOverrideWithRelayInfo([]byte(body), info)
				require.Error(t, err)
				info.ParamOverride = map[string]interface{}{"temperature": 0.5}
				_, err = ApplyParamOverrideWithRelayInfo([]byte(body), info)
				require.NoError(t, err)
			}
		})
	}
}
