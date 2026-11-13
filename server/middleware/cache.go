package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// apiPrefixes are answered by the application, never by a cache: a 404 from a
// route that did not exist yet would otherwise keep a corrected client from
// ever asking again for as long as the response stayed cached.
var apiPrefixes = []string{"/api", "/v1", "/v1beta", "/pg", "/mj", "/.well-known"}

func Cache() func(c *gin.Context) {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		switch {
		case path == "/":
			c.Header("Cache-Control", "no-cache")
		case isAPIPath(path):
			c.Header("Cache-Control", "no-store")
		default:
			c.Header("Cache-Control", "max-age=604800") // one week
		}
		c.Header("Cache-Version", "b688f2fb5be447c25e5aa3bd063087a83db32a288bf6a4f35f2d8db310e40b14")
		c.Next()
	}
}

func isAPIPath(path string) bool {
	for _, prefix := range apiPrefixes {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}
