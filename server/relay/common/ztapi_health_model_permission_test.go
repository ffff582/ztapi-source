package common

import (
	"net/http"
	"testing"
)

func TestZTAPIHealthModelPermissionFaultIsCountedSeparatelyFromKeyFailure(t *testing.T) {
	const wire = `{"error":{"type":"authentication_error","code":"model_not_granted","message":"model not granted for this app","request_id":"req-model-permission"}}`
	for _, source := range []string{"real", "probe"} {
		t.Run(source, func(t *testing.T) {
			out := healthFixture(t, "chat", source, wire, false, http.StatusForbidden)
			if out.Result != "failure" || out.Reason != "upstream_model_permission" {
				t.Fatalf("explicit model permission fault misclassified: %+v", out)
			}
			if out.ProviderErrorCode != "model_not_granted" || out.UpstreamRequestID != "req-model-permission" || out.ChannelID != 7 {
				t.Fatalf("route evidence lost: %+v", out)
			}
		})
	}
}

func TestZTAPIHealthPermissionClassificationDoesNotInventKeyFailures(t *testing.T) {
	cases := []struct {
		name, wire, result, reason string
		status                     int
	}{
		{"unknown403", `{"error":{"code":"custom_forbidden"}}`, "unknown", "ambiguous_upstream_4xx", http.StatusForbidden},
		{"key401", `{"error":{"code":"invalid_api_key"}}`, "failure", "upstream_auth", http.StatusUnauthorized},
		{"safety403", `{"error":{"code":"content_policy_violation"}}`, "excluded", "safety_refusal", http.StatusForbidden},
		{"input400", `{"error":{"code":"invalid_parameter"}}`, "excluded", "invalid_input", http.StatusBadRequest},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			out := healthFixture(t, "chat", "real", tt.wire, false, tt.status)
			if out.Result != tt.result || out.Reason != tt.reason {
				t.Fatalf("got %s/%s, want %s/%s", out.Result, out.Reason, tt.result, tt.reason)
			}
		})
	}
}
