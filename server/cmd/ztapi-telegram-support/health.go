package main

import (
	"encoding/json"
	"net/http"

	"github.com/QuantumNous/new-api/support/telegram"
)

func healthHandler(enabled bool, poll func() telegram.PollHealth) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if !enabled {
			_, _ = w.Write([]byte(`{"ok":true,"enabled":false}`))
			return
		}
		status := poll()
		if !status.OK {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(struct {
			OK      bool                `json:"ok"`
			Enabled bool                `json:"enabled"`
			Poll    telegram.PollHealth `json:"poll"`
		}{status.OK, true, status})
	})
}
