package router

import (
	"net/http"
	"testing"
)

// The console wallet reads reserved settlements from /api/user/self/...;
// without this route every visit showed a load failure.
func TestConsoleAccountRoutesAreRegistered(t *testing.T) {
	engine := newZTAPIRealRouter(t, false)
	registered := map[string]bool{}
	for _, route := range engine.Routes() {
		registered[route.Method+" "+route.Path] = true
	}
	for _, route := range []string{
		http.MethodGet + " /api/user/self",
		http.MethodGet + " /api/user/self/pending-settlements",
		http.MethodGet + " /api/user/pending-settlements",
		http.MethodGet + " /api/user/models",
	} {
		if !registered[route] {
			t.Errorf("console route %q is not registered", route)
		}
	}
}
