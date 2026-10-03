package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRewritePlaygroundMediaPathUsesPublicRelayPaths(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name string
		path string
		want string
	}{
		{name: "image", path: "/pg/images/generations", want: "/v1/images/generations"},
		{name: "video submit", path: "/pg/video/generations", want: "/v1/video/generations"},
		{name: "video fetch", path: "/pg/video/generations/task-1", want: "/v1/video/generations/task-1"},
		{name: "chat unchanged", path: "/pg/chat/completions", want: "/pg/chat/completions"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine := gin.New()
			engine.Use(RewritePlaygroundMediaPath())
			engine.Any("/*path", func(c *gin.Context) {
				if got := c.Request.URL.Path; got != tc.want {
					t.Fatalf("rewritten path = %q, want %q", got, tc.want)
				}
				c.Status(http.StatusNoContent)
			})

			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, tc.path, nil)
			engine.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
			}
		})
	}
}
