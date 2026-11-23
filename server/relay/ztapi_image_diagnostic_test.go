package relay

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/require"
)

func TestRunZTAPIImageDiagnosticIsExplicitRedactedBoundedAndRecordingOnly(t *testing.T) {
	const envName = "ZTAPI_TEST_IMAGE_DIAGNOSTIC_KEY"
	const key = "test-secret-key-material"
	t.Setenv(envName, key)
	manifestPath := "../model/ztapi_quotation_v1.json"
	manifestBefore, err := os.ReadFile(manifestPath)
	require.NoError(t, err)

	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, "Bearer "+key, r.Header.Get("Authorization"))
		body, readErr := io.ReadAll(r.Body)
		require.NoError(t, readErr)
		var request map[string]any
		require.NoError(t, common.Unmarshal(body, &request))
		require.Equal(t, "verified-provider-image", request["model"])
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Api-Key", key)
		w.Header().Set("X-Request-ID", "diag-header-1")
		_, _ = io.WriteString(w, `{"request_id":"diag-req-1","data":[{"url":"https://media.invalid/result.png?token=url-secret","b64_json":"`+strings.Repeat("QUJD", 600)+`"}],"metadata":{"authorization":"Bearer `+key+`"},"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`)
	}))
	t.Cleanup(upstream.Close)

	var evidence bytes.Buffer
	config := ZTAPIImageDiagnosticConfig{
		ExpectedOrigin:   upstream.URL,
		Endpoint:         upstream.URL + "/v1/images/generations",
		KeyEnvironment:   envName,
		Contract:         *syntheticManagedImageContract(t),
		Request:          dto.ImageRequest{Model: "untrusted-alias", Prompt: "neutral diagnostic", N: common.GetPointer(uint(1)), Size: "1024x1024", Quality: "standard", ResponseFormat: "url"},
		Client:           upstream.Client(),
		EvidenceWriter:   &evidence,
		MaxCaptureBytes:  64 << 10,
		MaxMetadataBytes: 700,
	}
	require.Zero(t, calls.Load(), "constructing diagnostics must not execute them")
	record, err := RunZTAPIImageDiagnostic(context.Background(), config)
	require.NoError(t, err)
	require.Equal(t, int32(1), calls.Load())
	require.Equal(t, http.StatusOK, record.StatusCode)
	require.Equal(t, "<omitted sha256=4e271316148f3aa880e89712b3f77e22beb2e9a8bf19d241f604d671af03dee1 bytes=10>", record.UpstreamRequestID)
	require.Equal(t, "<redacted>", record.RequestHeaders["Authorization"])
	require.Equal(t, "<redacted>", record.ResponseHeaders["X-Api-Key"])
	require.Equal(t, "<omitted sha256=37915c7f6e2fc9727c5041b31390a786fc8d0811d6ce0b321dcf6ca8e00baac1 bytes=13>", record.ResponseHeaders["X-Request-Id"])
	require.Len(t, record.RequestBodySHA256, 64)
	require.Len(t, record.ResponseCaptureSHA256, 64)
	require.True(t, record.ResponseBodyComplete)
	require.Equal(t, record.ResponseBytesRead, record.ResponseCapturedBytes)
	require.LessOrEqual(t, len(record.SanitizedMetadata), config.MaxMetadataBytes)

	output := evidence.String()
	require.NotContains(t, output, key)
	require.NotContains(t, output, "url-secret")
	require.NotContains(t, output, "query_secret")
	require.NotContains(t, output, strings.Repeat("QUJD", 20))
	require.Contains(t, output, "omitted")
	require.Less(t, evidence.Len(), 2500)

	manifestAfter, err := os.ReadFile(manifestPath)
	require.NoError(t, err)
	require.Equal(t, manifestBefore, manifestAfter, "diagnostics must not mutate the quotation manifest")
	source, err := os.ReadFile("ztapi_image_diagnostic.go")
	require.NoError(t, err)
	require.NotContains(t, string(source), "new-api/model", "recording-only diagnostics must not import the database model layer")
	require.NotContains(t, string(source), "gorm.io", "recording-only diagnostics must not have a database mutation dependency")
}

func TestRunZTAPIImageDiagnosticRequiresEnvironmentKeyBeforeCallOrWrite(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	t.Cleanup(upstream.Close)
	var evidence bytes.Buffer
	_, err := RunZTAPIImageDiagnostic(context.Background(), ZTAPIImageDiagnosticConfig{
		ExpectedOrigin:   upstream.URL,
		Endpoint:         upstream.URL + "/v1/images/generations",
		KeyEnvironment:   "ZTAPI_TEST_MISSING_IMAGE_KEY",
		Contract:         *syntheticManagedImageContract(t),
		Request:          dto.ImageRequest{Prompt: "neutral", N: common.GetPointer(uint(1)), Size: "1024x1024", Quality: "standard", ResponseFormat: "url"},
		Client:           upstream.Client(),
		EvidenceWriter:   &evidence,
		MaxCaptureBytes:  1024,
		MaxMetadataBytes: 512,
	})
	require.Error(t, err)
	require.Zero(t, calls.Load())
	require.Zero(t, evidence.Len())
}

func TestRunZTAPIImageDiagnosticRejectsCaptureLimitThatCannotUseLimitPlusOne(t *testing.T) {
	const envName = "ZTAPI_TEST_IMAGE_DIAGNOSTIC_OVERFLOW_KEY"
	t.Setenv(envName, "overflow-key")
	var calls atomic.Int32
	client := &http.Client{Transport: ztapiDiagnosticRoundTripper(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("must not execute")
	})}
	_, err := RunZTAPIImageDiagnostic(context.Background(), ZTAPIImageDiagnosticConfig{
		ExpectedOrigin: "http://127.0.0.1", Endpoint: "http://127.0.0.1/v1/images/generations", KeyEnvironment: envName,
		Contract: *syntheticManagedImageContract(t), Request: dto.ImageRequest{Prompt: "neutral", N: common.GetPointer(uint(1)), Size: "1024x1024", Quality: "standard", ResponseFormat: "url"},
		Client: client, EvidenceWriter: io.Discard, MaxCaptureBytes: int64(^uint64(0) >> 1), MaxMetadataBytes: 512,
	})
	require.Error(t, err)
	require.Zero(t, calls.Load())
}

func TestZTAPIImageDiagnosticNeverRecordsShortBase64Media(t *testing.T) {
	value := sanitizeZTAPIDiagnosticValue("YWJj", "b64_json", "")
	require.NotEqual(t, "YWJj", value)
	require.Contains(t, value, "omitted")
}

func TestZTAPIImageDiagnosticSanitizesRequestIDBeforeRecording(t *testing.T) {
	const key = "request-id-secret"
	contract := syntheticManagedImageContract(t)
	raw := []byte(`{"request_id":"https://media.invalid/result?token=` + key + `","data":[],"usage":{}}`)

	_, requestID, _ := sanitizeZTAPIImageDiagnosticMetadata(raw, false, *contract, key, 1024)
	require.NotContains(t, requestID, key)
	require.NotContains(t, requestID, "https://")
	require.Contains(t, requestID, "omitted")
}

func TestSanitizeZTAPIImageDiagnosticHashesRequestIDsConsistently(t *testing.T) {
	const requestID = "foreign-signing-key"
	const expected = "<omitted sha256=ece8bddf1ca281568756566d1a7a2cd04193743d7990cb01f52de76fae261e88 bytes=19>"
	contract := syntheticManagedImageContract(t)
	raw := []byte(`{"request_id":"` + requestID + `","data":[],"usage":{}}`)

	metadata, upstreamRequestID, _ := sanitizeZTAPIImageDiagnosticMetadata(raw, false, *contract, "current-key", 2048)
	headers := sanitizeZTAPIDiagnosticHeaders(http.Header{"X-Request-ID": []string{requestID}})

	require.Equal(t, expected, upstreamRequestID)
	require.Equal(t, expected, headers["X-Request-Id"])
	var sanitized map[string]any
	require.NoError(t, common.Unmarshal([]byte(metadata), &sanitized))
	require.Equal(t, expected, sanitized["request_id"])
	require.NotContains(t, metadata, requestID)
}

func TestSanitizeZTAPIImageDiagnosticOmitsNonStringRequestIDLeaves(t *testing.T) {
	contract := syntheticManagedImageContract(t)
	tests := []struct {
		name string
		raw  string
	}{
		{name: "number", raw: `{"request_id":42,"data":[],"usage":{}}`},
		{name: "bool", raw: `{"request_id":true,"data":[],"usage":{}}`},
		{name: "null", raw: `{"request_id":null,"data":[],"usage":{}}`},
		{name: "array", raw: `{"request_id":["nested-id",7],"data":[],"usage":{}}`},
		{name: "object", raw: `{"request_id":{"nested":"nested-id","value":7},"data":[],"usage":{}}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metadata, upstreamRequestID, _ := sanitizeZTAPIImageDiagnosticMetadata([]byte(test.raw), false, *contract, "current-key", 2048)

			require.Empty(t, upstreamRequestID)
			var sanitized map[string]any
			require.NoError(t, common.Unmarshal([]byte(metadata), &sanitized))
			require.Equal(t, "<omitted request id>", sanitized["request_id"])
			require.NotContains(t, metadata, "nested-id")
		})
	}
}

func TestSanitizeZTAPIImageDiagnosticOnlyRetainsAllowlistedProtocolEnums(t *testing.T) {
	const jwt = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJmb3JlaWduIn0.foreign-signature"
	root := map[string]any{
		"known": map[string]any{
			"status": "completed", "state": "queued", "type": "image_generation",
			"code": "invalid_request_error", "object": "list", "finish_reason": "stop", "role": "assistant",
		},
		"foreign": map[string]any{
			"status": "sk_live_foreign_secret", "state": "foreign-signing-key", "type": jwt,
			"code": "AKIAIOSFODNN7EXAMPLE", "object": "ghp_foreigncredential", "finish_reason": jwt, "role": jwt,
		},
	}

	sanitized := sanitizeZTAPIDiagnosticValue(root, "", "current-key").(map[string]any)
	known := sanitized["known"].(map[string]any)
	foreign := sanitized["foreign"].(map[string]any)
	require.Equal(t, "completed", known["status"])
	require.Equal(t, "queued", known["state"])
	require.Equal(t, "image_generation", known["type"])
	require.Equal(t, "invalid_request_error", known["code"])
	require.Equal(t, "list", known["object"])
	require.Equal(t, "stop", known["finish_reason"])
	require.Equal(t, "assistant", known["role"])
	for field, value := range foreign {
		require.Contains(t, value, "<omitted sha256=", field)
	}
	encoded, err := common.Marshal(sanitized)
	require.NoError(t, err)
	for _, forbidden := range []string{"sk_live_foreign_secret", "foreign-signing-key", jwt, "AKIAIOSFODNN7EXAMPLE", "ghp_foreigncredential"} {
		require.NotContains(t, string(encoded), forbidden)
	}
}

func TestRunZTAPIImageDiagnosticRejectsUnsupportedMediaInputBeforeCall(t *testing.T) {
	const envName = "ZTAPI_TEST_IMAGE_DIAGNOSTIC_UNSUPPORTED_KEY"
	t.Setenv(envName, "test-key")
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	t.Cleanup(upstream.Close)
	request := dto.ImageRequest{Prompt: "neutral", N: common.GetPointer(uint(1)), Size: "1024x1024", Quality: "standard", ResponseFormat: "url"}
	request.Image = []byte(`"embedded-media"`)
	_, err := RunZTAPIImageDiagnostic(context.Background(), ZTAPIImageDiagnosticConfig{
		ExpectedOrigin: upstream.URL, Endpoint: upstream.URL + "/v1/images/generations", KeyEnvironment: envName,
		Contract: *syntheticManagedImageContract(t), Request: request,
		Client: upstream.Client(), EvidenceWriter: io.Discard, MaxCaptureBytes: 1024, MaxMetadataBytes: 512,
	})
	require.Error(t, err)
	require.Zero(t, calls.Load())
}

func TestRunZTAPIImageDiagnosticPinsCredentialOriginAndRejectsEndpointDecorations(t *testing.T) {
	const envName = "ZTAPI_TEST_IMAGE_DIAGNOSTIC_ORIGIN_KEY"
	t.Setenv(envName, "origin-secret")
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	t.Cleanup(upstream.Close)
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	t.Cleanup(other.Close)
	request := dto.ImageRequest{Prompt: "neutral", N: common.GetPointer(uint(1)), Size: "1024x1024", Quality: "standard", ResponseFormat: "url"}

	for _, endpoint := range []string{
		upstream.URL + "/v1/images/generations?debug=true",
		upstream.URL + "/v1/images/generations#fragment",
		strings.Replace(upstream.URL, "://", "://user:password@", 1) + "/v1/images/generations",
		other.URL + "/v1/images/generations",
	} {
		_, err := RunZTAPIImageDiagnostic(context.Background(), ZTAPIImageDiagnosticConfig{
			ExpectedOrigin: upstream.URL, Endpoint: endpoint, KeyEnvironment: envName,
			Contract: *syntheticManagedImageContract(t), Request: request,
			Client: upstream.Client(), EvidenceWriter: io.Discard, MaxCaptureBytes: 1024, MaxMetadataBytes: 512,
		})
		require.Error(t, err)
	}
	require.Zero(t, calls.Load(), "invalid credential targets must be rejected before any network call")
}

func TestZTAPIImageDiagnosticRequiresHTTPSExceptLiteralLoopback(t *testing.T) {
	for _, origin := range []string{
		"https://provider.example",
		"http://localhost:8080",
		"http://127.0.0.1:8080",
		"http://127.200.1.2:8080",
		"http://[::1]:8080",
	} {
		_, err := validateZTAPIImageDiagnosticTarget(origin, origin+"/v1/images/generations", "/v1/images/generations")
		require.NoError(t, err, origin)
	}
	for _, origin := range []string{
		"http://provider.example",
		"http://localhost.evil.example",
		"http://127.0.0.1.evil.example",
		"http://[::ffff:127.0.0.1]:8080",
	} {
		_, err := validateZTAPIImageDiagnosticTarget(origin, origin+"/v1/images/generations", "/v1/images/generations")
		require.Error(t, err, origin)
	}
}

func TestRunZTAPIImageDiagnosticRejectsRedirectWithoutForwardingAuthorization(t *testing.T) {
	const envName = "ZTAPI_TEST_IMAGE_DIAGNOSTIC_REDIRECT_KEY"
	const key = "redirect-secret"
	t.Setenv(envName, key)
	var targetCalls atomic.Int32
	var targetAuthorization atomic.Value
	target := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		targetCalls.Add(1)
		targetAuthorization.Store(r.Header.Get("Authorization"))
	}))
	t.Cleanup(target.Close)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer "+key, r.Header.Get("Authorization"))
		w.Header().Set("Location", target.URL+"/stolen")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	t.Cleanup(source.Close)
	originalRedirectCalls := atomic.Int32{}
	client := source.Client()
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		originalRedirectCalls.Add(1)
		return nil
	}
	request := dto.ImageRequest{Prompt: "neutral", N: common.GetPointer(uint(1)), Size: "1024x1024", Quality: "standard", ResponseFormat: "url"}
	_, err := RunZTAPIImageDiagnostic(context.Background(), ZTAPIImageDiagnosticConfig{
		ExpectedOrigin: source.URL, Endpoint: source.URL + "/v1/images/generations", KeyEnvironment: envName,
		Contract: *syntheticManagedImageContract(t), Request: request,
		Client: client, EvidenceWriter: io.Discard, MaxCaptureBytes: 1024, MaxMetadataBytes: 512,
	})
	require.Error(t, err)
	require.Zero(t, targetCalls.Load())
	require.Zero(t, originalRedirectCalls.Load(), "the supplied client must be cloned and its redirect callback left unused")
	require.Nil(t, targetAuthorization.Load())
}

func TestZTAPIImageDiagnosticRedactsNestedAdversarialMetadata(t *testing.T) {
	const currentKey = "current-key-material"
	foreignBearer := "Bearer FOREIGN-SECRET-TOKEN"
	longOpaque := strings.Repeat("aB9_", 40)
	root := map[string]any{
		"status":              "completed",
		"unknown_short_value": "s3cr3t",
		"nested": map[string]any{
			"client_secret": "one", "refresh-token": "two", "authToken": "three",
			"password": "four", "passwd_hint": "five", "credentialValue": "six",
			"authorization_value": "seven", "session_cookie": "eight", "serviceApiKey": "nine",
			"private_key": "ten", "Signing-Key": "eleven", "ssh.key": "twelve",
			"JWT": "thirteen", "pass_phrase": "fourteen",
			"embedded_url":   "prefix HTTPS://example.invalid/path?token=secret suffix",
			"foreign_bearer": foreignBearer,
			"assignment":     "client_secret=visible-secret",
			"current":        "prefix " + currentKey + " suffix",
			"control":        "line\nsecret",
			"opaque":         longOpaque,
		},
	}
	sanitized := sanitizeZTAPIDiagnosticValue(root, "", currentKey)
	encoded, err := common.Marshal(sanitized)
	require.NoError(t, err)
	output := string(encoded)
	for _, forbidden := range []string{
		"one", "two", "three", "four", "five", "six", "seven", "eight", "nine",
		"ten", "eleven", "twelve", "thirteen", "fourteen", "s3cr3t",
		"example.invalid", "FOREIGN-SECRET-TOKEN", "visible-secret", currentKey, "line\\nsecret", longOpaque,
	} {
		require.NotContains(t, output, forbidden)
	}
	require.Contains(t, output, `"status":"completed"`)
	require.Less(t, len(output), 1800)
}

func TestZTAPIImageDiagnosticReplacesEntireFrozenResultLeafRegardlessOfType(t *testing.T) {
	contract := syntheticManagedImageContract(t)
	raw := []byte(`{"request_id":"safe-id","data":[{"url":{"nested_secret":"visible"},"b64_json":["also-visible"]}],"usage":{"input_tokens":1}}`)
	metadata, _, _ := sanitizeZTAPIImageDiagnosticMetadata(raw, false, *contract, "current-key", 4096)
	require.NotContains(t, metadata, "visible")
	require.Contains(t, metadata, "omitted")
}

type ztapiDiagnosticRoundTripper func(*http.Request) (*http.Response, error)

func (roundTrip ztapiDiagnosticRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

type countingInfiniteZTAPIReader struct {
	read int64
}

func (reader *countingInfiniteZTAPIReader) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = 'x'
	}
	reader.read += int64(len(buffer))
	return len(buffer), nil
}

func (reader *countingInfiniteZTAPIReader) Close() error { return nil }

type shortZTAPIWriter struct{}

func (shortZTAPIWriter) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	return len(data) - 1, nil
}

func TestRunZTAPIImageDiagnosticHardCapsActualResponseReads(t *testing.T) {
	const envName = "ZTAPI_TEST_IMAGE_DIAGNOSTIC_BOUND_KEY"
	t.Setenv(envName, "bound-key")
	reader := &countingInfiniteZTAPIReader{}
	client := &http.Client{Transport: ztapiDiagnosticRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: reader}, nil
	})}
	var evidence bytes.Buffer
	record, err := RunZTAPIImageDiagnostic(context.Background(), ZTAPIImageDiagnosticConfig{
		ExpectedOrigin: "http://127.0.0.1", Endpoint: "http://127.0.0.1/v1/images/generations", KeyEnvironment: envName,
		Contract: *syntheticManagedImageContract(t), Request: dto.ImageRequest{Prompt: "neutral", N: common.GetPointer(uint(1)), Size: "1024x1024", Quality: "standard", ResponseFormat: "url"},
		Client: client, EvidenceWriter: &evidence, MaxCaptureBytes: 32, MaxMetadataBytes: 128,
	})
	require.NoError(t, err)
	require.LessOrEqual(t, reader.read, int64(33), "the response reader must stop after limit+1 bytes")
	require.Equal(t, int64(33), record.ResponseBytesRead)
	require.Equal(t, int64(32), record.ResponseCapturedBytes)
	require.False(t, record.ResponseBodyComplete)
	require.Len(t, record.ResponseCaptureSHA256, 64)
	require.NotContains(t, evidence.String(), "response_body_sha256")
}

func TestRunZTAPIImageDiagnosticRejectsShortEvidenceWrite(t *testing.T) {
	const envName = "ZTAPI_TEST_IMAGE_DIAGNOSTIC_SHORT_WRITE_KEY"
	t.Setenv(envName, "short-write-key")
	body := `{"request_id":"safe-id","data":[{"url":"https://example.invalid/x"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
	client := &http.Client{Transport: ztapiDiagnosticRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	_, err := RunZTAPIImageDiagnostic(context.Background(), ZTAPIImageDiagnosticConfig{
		ExpectedOrigin: "http://127.0.0.1", Endpoint: "http://127.0.0.1/v1/images/generations", KeyEnvironment: envName,
		Contract: *syntheticManagedImageContract(t), Request: dto.ImageRequest{Prompt: "neutral", N: common.GetPointer(uint(1)), Size: "1024x1024", Quality: "standard", ResponseFormat: "url"},
		Client: client, EvidenceWriter: shortZTAPIWriter{}, MaxCaptureBytes: 1024, MaxMetadataBytes: 512,
	})
	require.ErrorIs(t, err, io.ErrShortWrite)
}
