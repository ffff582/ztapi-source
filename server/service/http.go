package service

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"

	"github.com/gin-gonic/gin"
)

func CloseResponseBodyGracefully(httpResponse *http.Response) {
	if httpResponse == nil || httpResponse.Body == nil {
		return
	}
	err := httpResponse.Body.Close()
	if err != nil {
		common.SysError("failed to close response body: " + err.Error())
	}
}

// ShouldCopyUpstreamHeader allows only validated response protocol metadata.
// Provider IDs stay internal; local billing IDs and CORS policy must not change.
func ShouldCopyUpstreamHeader(c *gin.Context, k string, v []string) bool {
	name := strings.ToLower(k)
	switch name {
	case strings.ToLower(common.RequestIdKey), "x-request-id", "request-id",
		"x-amzn-requestid", "x-amz-request-id", strings.ToLower(common.UpstreamRequestIdKey):
		if c != nil && len(v) > 0 && safeUpstreamRequestID(v[0]) {
			c.Set(common.UpstreamRequestIdKey, v[0])
		}
		return false
	}
	if len(v) == 0 {
		return false
	}
	for _, value := range v {
		if value == "" || common.RedactSecrets(value) != value {
			return false
		}
		for i := 0; i < len(value); i++ {
			if value[i] < ' ' || value[i] == 0x7f {
				return false
			}
		}
		if !safeUpstreamHeaderValue(name, strings.TrimSpace(value)) {
			return false
		}
	}
	return true
}

func safeUpstreamRequestID(value string) bool {
	if len(value) == 0 || len(value) > 256 || common.RedactSecrets(value) != value {
		return false
	}
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' {
			continue
		}
		if i == 0 || !strings.ContainsRune("._:-", rune(ch)) {
			return false
		}
	}
	return true
}

func upstreamHeaderUint(value string) (uint64, bool) {
	if value == "" {
		return 0, false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseUint(value, 10, 64)
	return n, err == nil
}

func safeUpstreamHeaderValue(name, value string) bool {
	switch name {
	case "content-type":
		mediaType, params, err := mime.ParseMediaType(value)
		if err != nil || !strings.Contains(mediaType, "/") {
			return false
		}
		for name, param := range params {
			if common.RedactSecrets(param) != param {
				return false
			}
			switch name {
			case "charset", "boundary", "codecs":
				if param == "" {
					return false
				}
			case "rate", "channels":
				if n, ok := upstreamHeaderUint(param); !ok || n == 0 {
					return false
				}
			default:
				return false
			}
		}
		return true
	case "content-encoding":
		for _, encoding := range strings.Split(value, ",") {
			switch strings.ToLower(strings.TrimSpace(encoding)) {
			case "gzip", "deflate", "br", "zstd", "identity":
			default:
				return false
			}
		}
		return true
	case "cache-control":
		for _, directive := range strings.Split(value, ",") {
			key, arg, hasArg := strings.Cut(strings.ToLower(strings.TrimSpace(directive)), "=")
			switch key {
			case "no-cache", "no-store", "no-transform", "must-revalidate", "proxy-revalidate", "public", "private", "immutable":
				if hasArg {
					return false
				}
			case "max-age", "s-maxage", "stale-while-revalidate", "stale-if-error":
				if _, ok := upstreamHeaderUint(arg); !hasArg || !ok {
					return false
				}
			default:
				return false
			}
		}
		return true
	case "pragma":
		return strings.EqualFold(value, "no-cache")
	case "x-accel-buffering":
		return strings.EqualFold(value, "no") || strings.EqualFold(value, "yes")
	case "retry-after":
		if _, ok := upstreamHeaderUint(value); ok {
			return true
		}
		_, err := http.ParseTime(value)
		return err == nil
	case "retry-after-ms", "x-ms-retry-after-ms",
		"ratelimit-limit", "ratelimit-remaining", "ratelimit-reset",
		"x-ratelimit-limit", "x-ratelimit-remaining", "x-ratelimit-reset",
		"x-ratelimit-limit-requests", "x-ratelimit-limit-tokens",
		"x-ratelimit-remaining-requests", "x-ratelimit-remaining-tokens",
		"anthropic-ratelimit-requests-limit", "anthropic-ratelimit-requests-remaining",
		"anthropic-ratelimit-tokens-limit", "anthropic-ratelimit-tokens-remaining",
		"anthropic-ratelimit-input-tokens-limit", "anthropic-ratelimit-input-tokens-remaining",
		"anthropic-ratelimit-output-tokens-limit", "anthropic-ratelimit-output-tokens-remaining":
		_, ok := upstreamHeaderUint(value)
		return ok
	case "x-ratelimit-reset-requests", "x-ratelimit-reset-tokens":
		if _, ok := upstreamHeaderUint(value); ok {
			return true
		}
		duration, err := time.ParseDuration(value)
		return err == nil && duration >= 0
	case "anthropic-ratelimit-requests-reset", "anthropic-ratelimit-tokens-reset",
		"anthropic-ratelimit-input-tokens-reset", "anthropic-ratelimit-output-tokens-reset":
		_, err := time.Parse(time.RFC3339Nano, value)
		return err == nil
	case "accept-ranges":
		return strings.EqualFold(value, "bytes") || strings.EqualFold(value, "none")
	case "content-range":
		return safeUpstreamContentRange(value)
	default:
		return false
	}
}

func safeUpstreamContentRange(value string) bool {
	unit, value, ok := strings.Cut(value, " ")
	if !ok || !strings.EqualFold(unit, "bytes") {
		return false
	}
	span, total, ok := strings.Cut(value, "/")
	if !ok {
		return false
	}
	size, hasSize := upstreamHeaderUint(total)
	if total != "*" && !hasSize {
		return false
	}
	if span == "*" {
		return hasSize
	}
	first, last, ok := strings.Cut(span, "-")
	start, validStart := upstreamHeaderUint(first)
	end, validEnd := upstreamHeaderUint(last)
	return ok && validStart && validEnd && start <= end && (!hasSize || end < size)
}

func CopyUpstreamResponseHeaders(c *gin.Context, header http.Header) {
	// Connection can nominate otherwise allowlisted fields as hop-by-hop.
	hopHeaders := make(map[string]bool)
	for name, values := range header {
		if strings.EqualFold(name, "Connection") || strings.EqualFold(name, "Proxy-Connection") {
			for _, value := range values {
				for _, field := range strings.Split(value, ",") {
					hopHeaders[strings.ToLower(strings.TrimSpace(field))] = true
				}
			}
		}
	}
	for name, values := range header {
		if ShouldCopyUpstreamHeader(c, name, values) && !hopHeaders[strings.ToLower(name)] {
			value := values[0]
			if strings.EqualFold(name, "Cache-Control") || strings.EqualFold(name, "Content-Encoding") {
				value = strings.Join(values, ", ")
			}
			c.Writer.Header().Set(name, value)
		}
	}
}

// IOCopyBytesGracefully reports local delivery errors, not supplier execution
// failures. Callers must not retry or append an API error after a partial write.
func IOCopyBytesGracefully(c *gin.Context, src *http.Response, data []byte) (err error) {
	defer func() {
		if err != nil {
			logger.LogError(c, "failed to deliver response body: "+err.Error())
		}
	}()
	if c.Writer == nil {
		return fmt.Errorf("response writer unavailable: %w", io.ErrClosedPipe)
	}
	if c.Request != nil && c.Request.Context().Err() != nil {
		return fmt.Errorf("response delivery canceled before write: %w", c.Request.Context().Err())
	}

	// We shouldn't set the header before we parse the response body, because the parse part may fail.
	// And then we will have to send an error response, but in this case, the header has already been set.
	// So the httpClient will be confused by the response.
	// For example, Postman will report error, and we cannot check the response at all.
	if src != nil {
		CopyUpstreamResponseHeaders(c, src.Header)
	}

	// set Content-Length header manually BEFORE calling WriteHeader
	c.Writer.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))

	// Gin commits this status on the first write or flush.
	if src != nil {
		c.Writer.WriteHeader(src.StatusCode)
	} else {
		c.Writer.WriteHeader(http.StatusOK)
	}

	written, err := io.Copy(c.Writer, bytes.NewReader(data))
	if err == nil && written != int64(len(data)) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return fmt.Errorf("response body write failed after %d of %d bytes: %w", written, len(data), err)
	}
	if c.Request != nil && c.Request.Context().Err() != nil {
		return fmt.Errorf("response delivery canceled after %d bytes: %w", written, c.Request.Context().Err())
	}
	if err := flushResponseBody(c.Writer); err != nil {
		return fmt.Errorf("flush response body after %d bytes: %w", written, err)
	}
	return nil
}

func flushResponseBody(writer gin.ResponseWriter) (err error) {
	defer func() {
		if recover() != nil {
			err = fmt.Errorf("response body flush panicked")
		}
	}()
	writer.WriteHeaderNow()
	rawWriter := http.ResponseWriter(writer)
	// Both health monitoring and Gin wrap the network writer. Gin's Flush
	// discards FlushError, so follow transparent facades until an error-aware
	// implementation or the terminal writer is reached.
	for depth := 0; depth < 16; depth++ {
		if _, ok := rawWriter.(interface{ FlushError() error }); ok {
			return http.NewResponseController(rawWriter).Flush()
		}
		if unwrapper, ok := rawWriter.(interface{ Unwrap() http.ResponseWriter }); ok {
			rawWriter = unwrapper.Unwrap()
			continue
		}
		return http.NewResponseController(rawWriter).Flush()
	}
	return fmt.Errorf("response writer wrapper chain exceeds the delivery limit")
}
