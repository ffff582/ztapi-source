package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// A cached answer about an API route outlives the release that changes the
// route. A 404 cached for a week kept the console from ever asking again for
// an endpoint that had since been added.
func TestCacheNeverStoresAnswersAboutAPIRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(Cache())
	engine.NoRoute(func(c *gin.Context) { c.Status(http.StatusNotFound) })
	engine.GET("/assets/app.js", func(c *gin.Context) { c.Status(http.StatusOK) })
	engine.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })

	for _, test := range []struct {
		path string
		want string
	}{
		{"/api/user/self/pending-settlements?after_id=0", "no-store"},
		{"/api/does-not-exist", "no-store"},
		{"/v1/models", "no-store"},
		{"/v1beta/models", "no-store"},
		{"/.well-known/source", "no-store"},
		{"/", "no-cache"},
		{"/assets/app.js", "max-age=604800"},
		{"/apifoo", "max-age=604800"},
	} {
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, test.path, nil))
		if got := recorder.Header().Get("Cache-Control"); got != test.want {
			t.Errorf("%s Cache-Control = %q, want %q", test.path, got, test.want)
		}
	}
}
