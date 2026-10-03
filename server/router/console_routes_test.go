package router

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
)

// The console wallet reads reserved settlements from /api/user/self/...;
// without this route every visit showed a load failure.
func TestConsoleAccountRoutesAreRegistered(t *testing.T) {
	engine := newZTAPIRealRouter(t, false)
	registered := map[string]bool{}
	for _, route := range engine.Routes() {
		registered[route.Method+" "+route.Path] = true
	}
	relayEngine := gin.New()
	SetRelayRouter(relayEngine)
	for _, route := range relayEngine.Routes() {
		registered[route.Method+" "+route.Path] = true
	}
	for _, route := range []string{
		http.MethodGet + " /api/user/self",
		http.MethodPost + " /api/user/self/email/verification",
		http.MethodPut + " /api/user/self/email",
		http.MethodGet + " /api/user/self/pending-settlements",
		http.MethodGet + " /api/user/pending-settlements",
		http.MethodGet + " /api/user/models",
		http.MethodPost + " /pg/images/generations",
		http.MethodPost + " /pg/video/generations",
		http.MethodGet + " /pg/video/generations/:task_id",
	} {
		if !registered[route] {
			t.Errorf("console route %q is not registered", route)
		}
	}
}
