package service

import (
	"strings"
	"testing"
)

func TestZTAPIWorkbenchXcloudBillingResponseID(t *testing.T) {
	// Previously matched Xcloud bills have 27 decimal digits and 12 letters.
	xcID := strings.Repeat("7", 27) + "aBcDeFgHiJkL"
	mixedID := "202611180423471234567890123" + "a9d6c4mQceWJ"
	const origin = "www.xcloudtoken.com"
	const chat = "/v1/chat/completions"
	for _, tc := range []struct {
		name, method, scheme, host, path, want string
		alias                                  []string
	}{
		{"xcloud alias", "POST", "https", origin, chat, xcID, []string{xcID}},
		{"alphanumeric Xcloud suffix", "POST", "https", origin, chat, mixedID, []string{mixedID}},
		{"no letter in Xcloud suffix", "POST", "https", origin, chat, "", []string{strings.Repeat("7", 39)}},
		{"short Xcloud suffix", "POST", "https", origin, chat, "", []string{mixedID[:len(mixedID)-1]}},
		{"Xcloud alias with non-ASCII letter", "POST", "https", origin, chat, "", []string{mixedID[:len(mixedID)-1] + "\u00c9"}},
		{"responses alias", "POST", "https", origin, "/v1/responses", xcID, []string{xcID}},
		{"duplicate header", "POST", "https", origin, chat, "", []string{xcID, xcID}},
		{"secret-like value", "POST", "https", origin, chat, "", []string{"sk-zt-synthetic-only"}},
		{"multiple comma values", "POST", "https", origin, chat, "", []string{xcID + ", " + xcID}},
		{"missing header", "POST", "https", origin, chat, "", nil},
		{"differing known path", "POST", "https", origin, "/v1/messages", "", []string{xcID}},
		{"encoded path", "POST", "https", origin, "/v1/%63hat/completions", "", []string{xcID}},
		{"differing host", "POST", "https", origin + ".fixture.invalid", chat, "", []string{xcID}},
		{"untrusted protocol", "POST", "http", origin, chat, "", []string{xcID}},
		{"untrusted host port", "POST", "https", origin + ":8443", chat, "", []string{xcID}},
		{"incorrect method", "GET", "https", origin, chat, "", []string{xcID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ztapiXcloudWorkbenchRequestID(tc.method, tc.scheme, tc.host, tc.path, tc.alias)
			if got != tc.want {
				t.Errorf("receipt selection = %q; want %q", got, tc.want)
			}
		})
	}
}
