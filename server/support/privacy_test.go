package support

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestRedactionPreservesCopyableSafePlaceholders(t *testing.T) {
	for _, example := range []string{"Authorization: Bearer YOUR_ZTAPI_API_KEY", "Authorization: Bearer $ZTAPI_API_KEY", `api_key="YOUR_API_KEY"`} {
		require.Equal(t, example, Redact(example))
	}
	for _, secret := range []string{"Authorization: Bearer sk-real-secretYOUR_API_KEY", "api_key=real-secret-123456", "Authorization: Basic dXNlcjpwYXNzd29yZA=="} {
		redacted := Redact(secret)
		require.NotEqual(t, secret, redacted)
		require.NotContains(t, redacted, "real-secret")
		require.NotContains(t, redacted, "dXNlcjpw")
	}
}

func TestRedactionCoversQuotedJSONFieldsAndEscapedValues(t *testing.T) {
	for _, raw := range []string{`{"password":"plainPassword123456"}`, `{"api_key":"plainVendorCredential123456"}`, `{"password":"abc\\\"escapedSecret"}`, `{"password":"1234"}`, `{'api_key': 'vendorPrivate12345'}`, `{"Authorization":"Basic dXNlcjpwYXNzd29yZA=="}`} {
		result := Redact(raw)
		for _, value := range []string{"plainPassword", "plainVendorCredential", "escapedSecret", "1234", "vendorPrivate", "dXNlcjpw"} {
			require.NotContains(t, result, value, raw)
		}
		require.Contains(t, result, "[已隐藏凭据]")
	}
	for _, raw := range []string{`{"api_key":"YOUR_ZTAPI_API_KEY"}`, `{"Authorization":"Bearer YOUR_ZTAPI_API_KEY"}`} {
		require.Equal(t, raw, Redact(raw))
	}
}
