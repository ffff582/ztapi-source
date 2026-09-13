package controller

import "testing"

func TestPublicUserLogErrorCodeOnlyReturnsNormalizedCodes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		other string
		want  string
	}{
		{name: "safe code", other: `{"error_code":"empty_output"}`, want: "empty_output"},
		{name: "safe namespaced code", other: `{"error_code":"upstream.rate-limit"}`, want: "upstream.rate-limit"},
		{name: "raw message rejected", other: `{"error_code":"bad request: secret value"}`},
		{name: "too long rejected", other: `{"error_code":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`},
		{name: "malformed json", other: `{`},
		{name: "missing code", other: `{"finish_reason":"stop"}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := publicUserLogErrorCode(test.other); got != test.want {
				t.Fatalf("publicUserLogErrorCode() = %q, want %q", got, test.want)
			}
		})
	}
}
