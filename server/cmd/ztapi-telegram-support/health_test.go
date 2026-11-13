package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/support/telegram"
)

func TestRuntimeHealthReflectsPollStatus(t *testing.T) {
	for _, tc := range []struct {
		name        string
		enabled, ok bool
		code        int
	}{
		{"disabled", false, false, 200}, {"starting", true, false, 503}, {"degraded", true, false, 503}, {"polling", true, true, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := healthHandler(tc.enabled, func() telegram.PollHealth { return telegram.PollHealth{OK: tc.ok, State: tc.name} })
			res := httptest.NewRecorder()
			h.ServeHTTP(res, httptest.NewRequest("GET", "/healthz", nil))
			if res.Code != tc.code || res.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("bad health response %d", res.Code)
			}
			var body struct {
				OK, Enabled bool
				Poll        *telegram.PollHealth
			}
			if json.Unmarshal(res.Body.Bytes(), &body) != nil || body.Enabled != tc.enabled || body.OK != (tc.ok || !tc.enabled) {
				t.Fatal("health payload contradicts status")
			}
			if tc.enabled && (body.Poll == nil || body.Poll.State != tc.name) {
				t.Fatal("missing actual poll status")
			}
		})
	}
}
