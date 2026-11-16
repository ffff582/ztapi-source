package service_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	openai "github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func TestShouldCopyUpstreamHeaderProtocolAllowlist(t *testing.T) {
	for _, name := range []string{
		"Authorization", "authorization", "Proxy-Authorization", "Proxy-Authenticate",
		"X-Api-Key", "Api-Key", "X-Goog-Api-Key", "Cookie", "Set-Cookie", "Set-Cookie2",
		"Location", "Content-Location", "WWW-Authenticate", "Authentication-Info",
		"Connection", "Proxy-Connection", "Keep-Alive", "TE", "Trailer", "Transfer-Encoding", "Upgrade",
		"Access-Control-Allow-Origin", "Access-Control-Allow-Credentials", "Access-Control-Expose-Headers",
		"Access-Control-Allow-Headers", "Access-Control-Allow-Methods", "Access-Control-Max-Age",
		"Content-Length", "Server", "Via", "Forwarded", "X-Forwarded-For", "X-Provider-Debug",
		"X-RateLimit-Api-Key", "X-RateLimit-Reset-Api-Key", "Anthropic-Ratelimit-Api-Key",
		"OpenAI-Organization", "OpenAI-Project", "Content-Disposition", "ETag", "Refresh",
	} {
		t.Run(name, func(t *testing.T) {
			if service.ShouldCopyUpstreamHeader(nil, name, []string{"synthetic-secret"}) {
				t.Errorf("untrusted header %s was allowed", name)
			}
		})
	}
}

func TestCopyUpstreamResponseHeadersRetainsEveryCacheDirective(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	service.CopyUpstreamResponseHeaders(c, http.Header{"Cache-Control": {"private, max-age=0", "no-store"}})
	if got := recorder.Header().Get("Cache-Control"); got != "private, max-age=0, no-store" {
		t.Fatalf("multi-value privacy directives were lost: %q", got)
	}
}

func TestShouldCopyUpstreamHeaderValidatesProtocolValues(t *testing.T) {
	tests := []struct {
		name, value string
		want        bool
	}{
		{"Content-Type", "application/json; charset=utf-8", true},
		{"content-type", "text/event-stream; charset=utf-8", true},
		{"Content-Type", "application/octet-stream", true},
		{"Content-Type", "audio/pcm; rate=24000; channels=1", true},
		{"Content-Type", "image/png", true},
		{"Content-Type", "video/mp4", true},
		{"Content-Type", "multipart/mixed; boundary=provider-boundary", true},
		{"Content-Type", "multipart/mixed; boundary=sk-synthetic-secret", false},
		{"Content-Type", "multipart/mixed; boundary*=UTF-8''%73%6b%2dsynthetic-secret", false},
		{"Content-Type", "not a media type", false},
		{"Content-Type", "application/json; api_key=synthetic-secret", false},
		{"Content-Type", "text/event-stream\r\nAuthorization: synthetic-secret", false},
		{"Content-Encoding", "gzip", true},
		{"Content-Encoding", "br, gzip", true},
		{"Content-Encoding", "synthetic-secret", false},
		{"Cache-Control", "no-cache, no-store, max-age=0", true},
		{"Cache-Control", "private, no-transform", true},
		{"Cache-Control", "private=synthetic-secret", false},
		{"Cache-Control", "max-age=synthetic-secret", false},
		{"Pragma", "no-cache", true},
		{"Pragma", "synthetic-secret", false},
		{"X-Accel-Buffering", "no", true},
		{"X-Accel-Buffering", "synthetic-secret", false},
		{"Retry-After", "0", true},
		{"Retry-After", "120", true},
		{"Retry-After", "Wed, 21 Oct 2015 07:28:00 GMT", true},
		{"Retry-After", "-1", false},
		{"Retry-After", "Bearer synthetic-secret", false},
		{"Retry-After-Ms", "1500", true},
		{"X-Ms-Retry-After-Ms", "500", true},
		{"Retry-After-Ms", "api_key=synthetic-secret", false},
		{"RateLimit-Limit", "120", true},
		{"RateLimit-Remaining", "0", true},
		{"RateLimit-Reset", "60", true},
		{"X-RateLimit-Limit", "120", true},
		{"X-RateLimit-Remaining", "0", true},
		{"X-RateLimit-Reset", "1730000000", true},
		{"X-RateLimit-Limit-Requests", "100", true},
		{"X-RateLimit-Limit-Tokens", "10000", true},
		{"X-RateLimit-Remaining-Requests", "0", true},
		{"X-RateLimit-Remaining-Tokens", "9999", true},
		{"X-RateLimit-Reset-Requests", "6m0s", true},
		{"X-RateLimit-Reset-Tokens", "1.5s", true},
		{"X-RateLimit-Limit-Requests", "sk-synthetic-secret", false},
		{"X-RateLimit-Remaining", "-1", false},
		{"X-RateLimit-Reset-Tokens", "-1s", false},
		{"X-RateLimit-Reset-Tokens", "1s; api_key=synthetic-secret", false},
		{"Anthropic-Ratelimit-Requests-Limit", "50", true},
		{"Anthropic-Ratelimit-Requests-Remaining", "49", true},
		{"Anthropic-Ratelimit-Requests-Reset", "2026-11-02T00:00:00Z", true},
		{"Anthropic-Ratelimit-Tokens-Limit", "10000", true},
		{"Anthropic-Ratelimit-Tokens-Remaining", "9000", true},
		{"Anthropic-Ratelimit-Tokens-Reset", "2026-11-02T00:00:00.123Z", true},
		{"Anthropic-Ratelimit-Input-Tokens-Limit", "10000", true},
		{"Anthropic-Ratelimit-Output-Tokens-Remaining", "0", true},
		{"Anthropic-Ratelimit-Input-Tokens-Reset", "2026-11-02T00:00:00Z", true},
		{"Anthropic-Ratelimit-Requests-Reset", "synthetic-secret", false},
		{"Accept-Ranges", "bytes", true},
		{"Accept-Ranges", "none", true},
		{"Accept-Ranges", "synthetic-secret", false},
		{"Content-Range", "bytes 0-2/3", true},
		{"Content-Range", "bytes */3", true},
		{"Content-Range", "bytes 3-2/3", false},
		{"Content-Range", "bytes 0-3/3", false},
		{"Content-Range", "bytes 0-2/synthetic-secret", false},
	}
	for _, tt := range tests {
		t.Run(tt.name+"/"+tt.value, func(t *testing.T) {
			if got := service.ShouldCopyUpstreamHeader(nil, tt.name, []string{tt.value}); got != tt.want {
				t.Errorf("ShouldCopyUpstreamHeader(%q, %q) = %t, want %t", tt.name, tt.value, got, tt.want)
			}
		})
	}
	for _, values := range [][]string{nil, {}, {""}, {" "}, {"application/json", ""}, {"application/json", "text/plain\x00"}, {"application/json", "text/plain\x7f"}} {
		if service.ShouldCopyUpstreamHeader(nil, "Content-Type", values) {
			t.Errorf("invalid header values %#v were allowed", values)
		}
	}
}

func TestShouldCopyUpstreamHeaderCapturesOnlyInternalRawProviderID(t *testing.T) {
	const rawID = "req_0123456789abcdef0123456789abcdef-1.2:3"
	for _, name := range []string{"X-Request-ID", "x-request-id", common.RequestIdKey, "Request-Id", "X-Amzn-Requestid", "X-Amz-Request-Id", common.UpstreamRequestIdKey} {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Header("X-Request-ID", "local-billing-id")
			ctx.Header(common.RequestIdKey, "local-billing-id")
			if service.ShouldCopyUpstreamHeader(ctx, name, []string{rawID}) {
				t.Fatal("provider ID was allowed into the client response")
			}
			if got := ctx.GetString(common.UpstreamRequestIdKey); got != rawID {
				t.Fatalf("internal raw provider ID = %q, want %q", got, rawID)
			}
			service.ShouldCopyUpstreamHeader(ctx, name, []string{""})
			if got := ctx.GetString(common.UpstreamRequestIdKey); got != rawID {
				t.Fatal("empty provider ID erased reconciliation evidence")
			}
			if service.ShouldCopyUpstreamHeader(nil, name, nil) {
				t.Fatal("empty provider ID was allowed")
			}
		})
	}
}

func TestShouldCopyUpstreamHeaderRejectsUnsafeProviderIDsWithoutMutatingEvidence(t *testing.T) {
	for _, value := range []string{
		"", " ", "provider-id api_key=synthetic-secret", "sk-synthetic-secret",
		"provider\r\nAuthorization: synthetic-secret", "provider\x00id", "provider/id?key=secret",
		strings.Repeat("a", 257),
	} {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		const originalEvidence = "original raw trace api_key=synthetic-secret"
		ctx.Set(common.UpstreamRequestIdKey, originalEvidence)
		if service.ShouldCopyUpstreamHeader(ctx, "X-Request-ID", []string{value}) {
			t.Errorf("unsafe provider ID %q was allowed", value)
		}
		if got := ctx.GetString(common.UpstreamRequestIdKey); got != originalEvidence {
			t.Errorf("original reconciliation evidence mutated for %q: %q", value, got)
		}
	}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	service.ShouldCopyUpstreamHeader(ctx, "X-Request-ID", []string{strings.Repeat("a", 256)})
	if got := ctx.GetString(common.UpstreamRequestIdKey); got != strings.Repeat("a", 256) {
		t.Fatal("bounded machine ID was not preserved exactly")
	}
}

func TestIOCopyBytesGracefullyFiltersHeadersWithoutChangingPayload(t *testing.T) {
	for _, mediaType := range []string{"application/json", "text/event-stream; charset=utf-8", "audio/mpeg", "image/png", "video/mp4", "application/octet-stream"} {
		t.Run(mediaType, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Header("X-Request-ID", "local-billing-id")
			ctx.Header(common.RequestIdKey, "local-billing-id")
			ctx.Header("Access-Control-Allow-Origin", "https://local.invalid")
			body := []byte{0, 1, 2, 0xff, '\n'}
			header := http.Header{
				"Content-Type": {mediaType}, "Content-Length": {"999"},
				"Content-Encoding": {"gzip"}, "Cache-Control": {"no-cache"},
				"X-Accel-Buffering": {"no"}, "Retry-After": {"3"},
				"X-Ratelimit-Remaining-Tokens": {"0"},
				"X-Request-ID":                 {"provider-id"},
				"Authorization":                {"Bearer synthetic-secret"}, "Set-Cookie": {"key=synthetic-secret"},
				"Location":         {"https://provider.invalid/?key=synthetic-secret"},
				"WWW-Authenticate": {"Bearer synthetic-secret"}, "X-Api-Key": {"synthetic-secret"},
				"X-Ratelimit-Api-Key": {"synthetic-secret"}, "X-Provider-Debug": {"synthetic-secret"},
				"Access-Control-Allow-Origin": {"https://provider.invalid"},
				"Connection":                  {"keep-alive"}, "Transfer-Encoding": {"chunked"},
			}
			service.IOCopyBytesGracefully(ctx, &http.Response{StatusCode: http.StatusPartialContent, Header: header}, body)
			result := recorder.Result()
			if result.StatusCode != http.StatusPartialContent || !bytes.Equal(recorder.Body.Bytes(), body) {
				t.Fatal("status or binary payload changed")
			}
			for name, want := range map[string]string{
				"Content-Type": mediaType, "Content-Length": strconv.Itoa(len(body)),
				"Content-Encoding": "gzip", "Cache-Control": "no-cache", "X-Accel-Buffering": "no",
				"Retry-After": "3", "X-Ratelimit-Remaining-Tokens": "0",
				"X-Request-ID": "local-billing-id", common.RequestIdKey: "local-billing-id",
				"Access-Control-Allow-Origin": "https://local.invalid",
			} {
				if got := result.Header.Get(name); got != want {
					t.Errorf("%s = %q, want %q", name, got, want)
				}
			}
			for _, name := range []string{"Authorization", "Set-Cookie", "Location", "WWW-Authenticate", "X-Api-Key", "X-Ratelimit-Api-Key", "X-Provider-Debug", "Connection", "Transfer-Encoding"} {
				if got := result.Header.Get(name); got != "" {
					t.Errorf("blocked header %s leaked %q", name, got)
				}
			}
			if got := ctx.GetString(common.UpstreamRequestIdKey); got != "provider-id" {
				t.Errorf("raw reconciliation ID changed: %q", got)
			}
		})
	}
}

func TestIOCopyBytesGracefullyDropsConnectionNominatedHeaders(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	header := http.Header{
		"Connection":       {"keep-alive, Retry-After", "content-type"},
		"Proxy-Connection": {"x-ratelimit-remaining"},
		"Content-Type":     {"application/json"}, "Retry-After": {"3"},
		"X-Ratelimit-Remaining": {"1"}, "Cache-Control": {"no-store"},
	}
	service.IOCopyBytesGracefully(ctx, &http.Response{StatusCode: http.StatusOK, Header: header}, []byte("ok"))
	for _, name := range []string{"Content-Type", "Retry-After", "X-Ratelimit-Remaining", "Connection", "Proxy-Connection"} {
		if got := recorder.Result().Header.Get(name); got != "" {
			t.Errorf("hop-by-hop header %s leaked %q", name, got)
		}
	}
	if got := recorder.Result().Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("end-to-end header lost: %q", got)
	}
}

func TestIOCopyBytesGracefullySkipsEmptyAndInvalidHeaderValues(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	service.IOCopyBytesGracefully(ctx, &http.Response{StatusCode: http.StatusOK, Header: http.Header{
		"Content-Type": nil, "Retry-After": {}, "X-Ratelimit-Limit-Tokens": {""},
		"Cache-Control": {"no-store", "api_key=synthetic-secret"},
	}}, []byte("ok"))
	for _, name := range []string{"Content-Type", "Retry-After", "X-Ratelimit-Limit-Tokens", "Cache-Control"} {
		if got := recorder.Result().Header.Get(name); got != "" {
			t.Errorf("invalid %s value leaked %q", name, got)
		}
	}
}

func TestUpstreamHeaderPolicyAppliesToResponsesAndAudioCallers(t *testing.T) {
	for _, handler := range []string{"responses", "audio"} {
		t.Run(handler, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/test", nil)
			ctx.Header("X-Request-ID", "local-billing-id")
			ctx.Header(common.RequestIdKey, "local-billing-id")
			payload, contentType := []byte(`{"id":"resp-test","output":[],"usage":{"input_tokens":7,"output_tokens":3,"total_tokens":10}}`), "application/json"
			info := &relaycommon.RelayInfo{}
			if handler == "audio" {
				payload, contentType = []byte{0, 1, 2, 3}, "audio/pcm"
				info.Request = &dto.AudioRequest{ResponseFormat: "pcm"}
			}
			response := &http.Response{StatusCode: http.StatusOK, Header: http.Header{
				"Content-Type": {contentType}, "Authorization": {"Bearer synthetic-secret"},
				"X-Api-Key": {"synthetic-secret"}, "Set-Cookie": {"key=synthetic-secret"},
				"Location":            {"https://provider.invalid/?key=synthetic-secret"},
				"X-Ratelimit-Api-Key": {"synthetic-secret"}, "X-Ratelimit-Remaining-Tokens": {"0"},
				"X-Request-ID": {"provider-id"}, "Retry-After": nil,
			}, Body: io.NopCloser(bytes.NewReader(payload))}
			if handler == "responses" {
				usage, apiErr := openai.OaiResponsesHandler(ctx, info, response)
				if apiErr != nil || usage == nil || usage.PromptTokens != 7 || usage.CompletionTokens != 3 || usage.TotalTokens != 10 {
					t.Fatalf("Responses usage changed: usage=%+v, err=%v", usage, apiErr)
				}
			} else {
				if usage := openai.OpenaiTTSHandler(ctx, response, info); usage == nil {
					t.Fatal("audio handler lost usage")
				}
			}
			if !bytes.Equal(recorder.Body.Bytes(), payload) {
				t.Fatal("caller payload changed")
			}
			for _, name := range []string{"Authorization", "X-Api-Key", "Set-Cookie", "Location", "X-Ratelimit-Api-Key", "Retry-After"} {
				if got := recorder.Result().Header.Get(name); got != "" {
					t.Errorf("caller leaked %s = %q", name, got)
				}
			}
			for name, want := range map[string]string{"Content-Type": contentType, "X-Ratelimit-Remaining-Tokens": "0", "X-Request-ID": "local-billing-id", common.RequestIdKey: "local-billing-id"} {
				if got := recorder.Result().Header.Get(name); got != want {
					t.Errorf("caller %s = %q, want %q", name, got, want)
				}
			}
			if got := ctx.GetString(common.UpstreamRequestIdKey); got != "provider-id" {
				t.Errorf("caller lost provider reconciliation ID: %q", got)
			}
		})
	}
}
