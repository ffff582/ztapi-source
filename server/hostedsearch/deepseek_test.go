package hostedsearch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
)

type deepSeekTestTransport func(*http.Request) (*http.Response, error)

func (f deepSeekTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func deepSeekTestProvider(t *testing.T, transport http.RoundTripper) Provider {
	t.Helper()
	p, err := NewDeepSeekProvider("unit-test-key-not-real", transport)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func deepSeekTestResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"X-Request-Id": {"req_123-abc.1"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func deepSeekTestHash(body string) string {
	hash := sha256.Sum256([]byte(body))
	return hex.EncodeToString(hash[:])
}

func deepSeekTestJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := common.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func deepSeekTestWitness(t *testing.T, result Result, status int, body string) {
	t.Helper()
	if result.HTTPStatus != status || result.UpstreamRequestID != "req_123-abc.1" || result.ResponseSHA256 != deepSeekTestHash(body) {
		t.Fatalf("lost response witness: %+v", result)
	}
}

func TestDeepSeekConstructorRejectsUnsafeKeys(t *testing.T) {
	for _, key := range []string{"", " ", " key", "key ", "key\r\nsecret", "key\x00secret", "key\x7fsecret", "key secret"} {
		t.Run(strings.ReplaceAll(key, "\x00", "NUL"), func(t *testing.T) {
			p, err := NewDeepSeekProvider(key, deepSeekTestTransport(func(*http.Request) (*http.Response, error) {
				t.Fatal("constructor must not perform I/O")
				return nil, nil
			}))
			if err == nil || p != nil || strings.Contains(err.Error(), "secret") {
				t.Fatalf("unsafe key accepted or disclosed: provider=%v, err=%v", p, err)
			}
		})
	}
}

func TestDeepSeekDefaultTransportIsPrivateAndSecure(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://proxy.invalid:9999")
	previous := http.DefaultTransport
	http.DefaultTransport = deepSeekTestTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("must not inherit global transport")
		return nil, nil
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
	p := deepSeekTestProvider(t, nil).(*deepSeekProvider)
	if p.client.Timeout != 20*time.Second || p.client.CheckRedirect == nil {
		t.Fatal("missing bounded client or redirect guard")
	}
	transport, ok := p.client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil || transport.DialContext == nil || transport.TLSHandshakeTimeout <= 0 || !transport.DisableCompression {
		t.Fatal("default transport is not isolated from environment proxies")
	}
	if transport.TLSClientConfig != nil && transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("TLS certificate verification disabled")
	}
	req, _ := http.NewRequest(http.MethodGet, "https://attacker.invalid/", nil)
	if err := p.client.CheckRedirect(req, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("redirect not blocked: %v", err)
	}
}

func TestDeepSeekSearchUsesOnlyOfficialEndpointAndSummaries(t *testing.T) {
	for _, count := range []int{1, 10} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			query := strings.Repeat("\u641c", 128)
			body := `{"results":[{"title":"Example","url":"https://example.com/a","summary":"summary","content":"DO-NOT-RETURN-FULL-TEXT","score":0.9}],"metadata":{"ok":true}}`
			calls := 0
			p := deepSeekTestProvider(t, deepSeekTestTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != http.MethodPost || req.URL.String() != "https://api.deepseek.com/v1/tools/search" || req.Host != "api.deepseek.com" {
					t.Fatalf("unexpected outbound target: %s %s", req.Method, req.URL)
				}
				if req.Header.Get("Authorization") != "Bearer unit-test-key-not-real" || req.Header.Get("Content-Type") != "application/json" {
					t.Fatal("missing request credentials or JSON content type")
				}
				deadline, ok := req.Context().Deadline()
				if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 20*time.Second {
					t.Fatal("request deadline is not bounded to 20 seconds")
				}
				var payload map[string]any
				if err := common.DecodeJson(req.Body, &payload); err != nil {
					t.Fatal(err)
				}
				if len(payload) != 3 || payload["query"] != query || payload["count"] != float64(count) || payload["need_content"] != false {
					t.Fatalf("unexpected search payload: %#v", payload)
				}
				return deepSeekTestResponse(200, body), nil
			}))
			result, err := p.Search(context.Background(), query, count)
			if err != nil || result.Outcome != OutcomeSucceeded || calls != 1 || len(result.Sources) != 1 {
				t.Fatalf("search failed: %+v, %v, calls=%d", result, err, calls)
			}
			if result.Sources[0] != (Source{Title: "Example", URL: "https://example.com/a", Summary: "summary"}) || result.Content != "" {
				t.Fatalf("unexpected source or full text leak: %+v", result)
			}
			deepSeekTestWitness(t, result, 200, body)
		})
	}
}

func TestDeepSeekFetchSendsURLAsJSONToOfficialEndpoint(t *testing.T) {
	body := `{"title":"Example","content":"full page","extra":{"ok":true}}`
	p := deepSeekTestProvider(t, deepSeekTestTransport(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.String() != "https://api.deepseek.com/v1/tools/fetch" || req.Header.Get("Authorization") != "Bearer unit-test-key-not-real" {
			t.Fatal("fetch target or credentials differ from official API")
		}
		var payload map[string]any
		if err := common.DecodeJson(req.Body, &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload) != 1 || payload["url"] != "https://example.com/page?q=a&b=c" {
			t.Fatalf("unexpected fetch payload: %#v", payload)
		}
		return deepSeekTestResponse(200, body), nil
	}))
	result, err := p.Fetch(context.Background(), "https://example.com/page?q=a&b=c")
	if err != nil || result.Outcome != OutcomeSucceeded || result.Title != "Example" || result.Content != "full page" || len(result.Sources) != 0 {
		t.Fatalf("unexpected fetch result: %+v, %v", result, err)
	}
	deepSeekTestWitness(t, result, 200, body)
}

func TestDeepSeekSearchRejectsInvalidInputWithoutIO(t *testing.T) {
	for _, tc := range []struct {
		query string
		count int
	}{
		{"", 1}, {" \t\n", 1}, {strings.Repeat("x", 129), 1}, {strings.Repeat("\u641c", 129), 1},
		{string([]byte{0xff}), 1}, {"query", 0}, {"query", -1}, {"query", 11},
	} {
		p := deepSeekTestProvider(t, deepSeekTestTransport(func(*http.Request) (*http.Response, error) {
			t.Fatal("invalid input reached transport")
			return nil, nil
		}))
		result, err := p.Search(context.Background(), tc.query, tc.count)
		if err == nil || result.Outcome != OutcomeRejected || result.HTTPStatus != 0 || result.ResponseSHA256 != "" {
			t.Fatalf("invalid search accepted: %+v, %v", result, err)
		}
	}
}

func TestDeepSeekHTTPClassificationAndWitness(t *testing.T) {
	for _, operation := range []string{"search", "fetch"} {
		for _, tc := range []struct {
			status  int
			outcome Outcome
		}{
			{400, OutcomeRejected}, {401, OutcomeRejected}, {403, OutcomeRejected}, {404, OutcomeRejected},
			{408, OutcomeUnknown}, {422, OutcomeRejected}, {429, OutcomeRejected},
			{500, OutcomeUnknown}, {502, OutcomeUnknown}, {503, OutcomeUnknown},
			{201, OutcomeUnknown}, {204, OutcomeUnknown}, {302, OutcomeUnknown},
		} {
			t.Run(operation+"/"+http.StatusText(tc.status), func(t *testing.T) {
				body := `{"error":{"message":"PRIVATE-UPSTREAM-DETAILS"}}`
				calls := 0
				p := deepSeekTestProvider(t, deepSeekTestTransport(func(*http.Request) (*http.Response, error) {
					calls++
					return deepSeekTestResponse(tc.status, body), nil
				}))
				var result Result
				var err error
				if operation == "search" {
					result, err = p.Search(context.Background(), "query", 1)
				} else {
					result, err = p.Fetch(context.Background(), "https://example.com/")
				}
				if err == nil || result.Outcome != tc.outcome || calls != 1 || strings.Contains(err.Error(), "PRIVATE") || strings.Contains(err.Error(), "unit-test-key") {
					t.Fatalf("wrong status classification or leaked error: %+v, %v, calls=%d", result, err, calls)
				}
				deepSeekTestWitness(t, result, tc.status, body)
			})
		}
	}
}

func TestDeepSeekEmptyResultsAreNotSuccess(t *testing.T) {
	for _, tc := range []struct {
		operation string
		body      string
	}{
		{"search", `{"results":[]}`},
		{"fetch", `{"title":"Empty","content":""}`},
		{"fetch", `{"title":"Empty","content":" \n\t"}`},
	} {
		p := deepSeekTestProvider(t, deepSeekTestTransport(func(*http.Request) (*http.Response, error) {
			return deepSeekTestResponse(200, tc.body), nil
		}))
		var result Result
		var err error
		if tc.operation == "search" {
			result, err = p.Search(context.Background(), "query", 1)
		} else {
			result, err = p.Fetch(context.Background(), "https://example.com/")
		}
		if err != nil || result.Outcome != OutcomeEmpty {
			t.Fatalf("empty result misclassified: %+v, %v", result, err)
		}
		deepSeekTestWitness(t, result, 200, tc.body)
	}
}

func TestDeepSeekRejectsMalformedAndDuplicateResponseSchemas(t *testing.T) {
	for _, tc := range []struct {
		operation string
		body      string
	}{
		{"search", ""}, {"search", `null`}, {"search", `[]`}, {"search", `{}`},
		{"search", `{"results":null}`}, {"search", `{"results":{}}`}, {"search", `{"results":[null]}`},
		{"search", `{"results":[{"title":"a","url":"https://example.com","summary":12}]}`},
		{"search", `{"results":[{"title":"a","url":"https://example.com"}]}`},
		{"search", `{"results":[{"title":null,"url":"https://example.com","summary":"a"}]}`},
		{"search", `{"results":[{"title":"a","url":"","summary":"a"}]}`},
		{"search", `{"results":[{"title":"a","url":"http://127.0.0.1/","summary":"a"}]}`},
		{"search", `{"results":[{"title":"a","url":"https://metadata.google.internal/","summary":"a"}]}`},
		{"search", `{"results":[{"title":"a","url":"https://user:password@example.com/","summary":"a"}]}`},
		{"search", `{"results":[{"title":"a","url":"https://example.com","summary":"a","content":{}}]}`},
		{"search", `{"Results":[]}`}, {"search", `{"results":[],"results":[]}`},
		{"search", `{"results":[],"re\u0073ults":[]}`},
		{"search", `{"results":[{"title":"a","url":"https://example.com","summary":"a","summ\u0061ry":"b"}]}`},
		{"search", `{"results":[],"extra":{"a":1,"a":2}}`},
		{"search", `{"results":[]} {"results":[]}`},
		{"fetch", `null`}, {"fetch", `{}`}, {"fetch", `{"title":"x"}`},
		{"fetch", `{"title":"x","content":null}`}, {"fetch", `{"title":"x","content":true}`},
		{"fetch", `{"title":12,"content":"a"}`}, {"fetch", `{"title":"x","Content":"a"}`},
		{"fetch", `{"title":"x","content":"a","content":"b"}`},
		{"fetch", `{"title":"x","content":"a","cont\u0065nt":"b"}`},
		{"fetch", `{"title":"x","content":"a","extra":[{"x":1,"x":2}]}`},
		{"fetch", `{"title":"x","content":"a"} trailing`},
		{"fetch", `{"title":"x","content":"` + string([]byte{0xff}) + `"}`},
	} {
		t.Run(tc.operation+"/"+tc.body, func(t *testing.T) {
			p := deepSeekTestProvider(t, deepSeekTestTransport(func(*http.Request) (*http.Response, error) {
				return deepSeekTestResponse(200, tc.body), nil
			}))
			var result Result
			var err error
			if tc.operation == "search" {
				result, err = p.Search(context.Background(), "query", 1)
			} else {
				result, err = p.Fetch(context.Background(), "https://example.com/")
			}
			if err == nil || result.Outcome != OutcomeUnknown || len(result.Sources) != 0 || result.Content != "" || strings.Contains(err.Error(), tc.body) && tc.body != "" {
				t.Fatalf("bad schema accepted or disclosed: %+v, %v", result, err)
			}
			deepSeekTestWitness(t, result, 200, tc.body)
		})
	}
}

func TestDeepSeekOutputLimitsUseUnicodeAndDoNotTruncate(t *testing.T) {
	for _, tc := range []struct {
		operation string
		length    int
		outcome   Outcome
	}{
		{"search", 4096, OutcomeSucceeded}, {"search", 4097, OutcomeUnknown},
		{"fetch", 64000, OutcomeSucceeded}, {"fetch", 64001, OutcomeUnknown},
	} {
		value := strings.Repeat("\u754c", tc.length)
		var body string
		if tc.operation == "search" {
			body = deepSeekTestJSON(t, map[string]any{"results": []Source{{Title: "x", URL: "https://example.com", Summary: value}}})
		} else {
			body = deepSeekTestJSON(t, map[string]string{"title": "x", "content": value})
		}
		p := deepSeekTestProvider(t, deepSeekTestTransport(func(*http.Request) (*http.Response, error) {
			return deepSeekTestResponse(200, body), nil
		}))
		var result Result
		var err error
		if tc.operation == "search" {
			result, err = p.Search(context.Background(), "query", 10)
		} else {
			result, err = p.Fetch(context.Background(), "https://example.com/")
		}
		if result.Outcome != tc.outcome || (err != nil) != (tc.outcome == OutcomeUnknown) {
			t.Fatalf("wrong Unicode limit outcome: %+v, %v", result, err)
		}
		if tc.outcome == OutcomeUnknown && !strings.Contains(err.Error(), "limit exceeded") {
			t.Fatalf("oversized text did not produce an explicit limit error: %v", err)
		}
		if tc.outcome == OutcomeSucceeded {
			if tc.operation == "search" && result.Sources[0].Summary != value || tc.operation == "fetch" && result.Content != value {
				t.Fatal("valid Unicode text was truncated")
			}
		} else if len(result.Sources) != 0 || result.Content != "" || result.Title != "" {
			t.Fatal("oversized output was silently truncated or partially returned")
		}
		deepSeekTestWitness(t, result, 200, body)
	}
	for _, size := range []int{10, 11} {
		sources := make([]Source, size)
		for i := range sources {
			sources[i] = Source{Title: "x", URL: "https://example.com", Summary: "summary"}
		}
		body := deepSeekTestJSON(t, map[string]any{"results": sources})
		p := deepSeekTestProvider(t, deepSeekTestTransport(func(*http.Request) (*http.Response, error) {
			return deepSeekTestResponse(200, body), nil
		}))
		result, err := p.Search(context.Background(), "query", 10)
		if size == 10 && (err != nil || result.Outcome != OutcomeSucceeded || len(result.Sources) != 10) {
			t.Fatal("ten sources must be preserved")
		}
		if size == 11 && (err == nil || result.Outcome != OutcomeUnknown || len(result.Sources) != 0) {
			t.Fatal("eleven sources must fail, not silently truncate")
		}
		deepSeekTestWitness(t, result, 200, body)
	}
}

func TestDeepSeekFiltersRequestIDHeaders(t *testing.T) {
	for _, tc := range []struct {
		headers http.Header
		want    string
	}{
		{http.Header{"X-Request-Id": {"abc_123-XYZ.9"}}, "abc_123-XYZ.9"},
		{http.Header{"X-Ds-Request-Id": {"ds-123"}}, "ds-123"},
		{http.Header{"X-Request-Id": {strings.Repeat("a", 128)}}, strings.Repeat("a", 128)},
		{http.Header{"X-Request-Id": {strings.Repeat("a", 129)}}, ""},
		{http.Header{"X-Request-Id": {"abc\r\nsecret"}}, ""},
		{http.Header{"X-Request-Id": {"abc secret"}}, ""},
		{http.Header{"X-Request-Id": {"\u79d8\u5bc6"}}, ""},
		{http.Header{"X-Request-Id": {"<script>"}}, ""},
		{http.Header{"X-Request-Id": {"a", "b"}}, ""},
		{http.Header{"X-Request-Id": {"a"}, "X-Ds-Request-Id": {"b"}}, ""},
		{http.Header{"Untrusted-Header": {"secret"}}, ""},
	} {
		p := deepSeekTestProvider(t, deepSeekTestTransport(func(*http.Request) (*http.Response, error) {
			response := deepSeekTestResponse(200, `{"results":[]}`)
			response.Header = tc.headers
			return response, nil
		}))
		result, err := p.Search(context.Background(), "query", 1)
		if err != nil || result.UpstreamRequestID != tc.want {
			t.Fatalf("request ID not safely filtered: %q, %v", result.UpstreamRequestID, err)
		}
	}
}

func TestDeepSeekNeverFollowsRedirectOrRetries(t *testing.T) {
	calls := 0
	body := "redirect body"
	p := deepSeekTestProvider(t, deepSeekTestTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Host != "api.deepseek.com" {
			t.Fatal("API key sent outside official host")
		}
		response := deepSeekTestResponse(307, body)
		response.Header.Set("Location", "https://attacker.invalid/steal")
		return response, nil
	}))
	result, err := p.Search(context.Background(), "query", 1)
	if err == nil || calls != 1 || result.Outcome != OutcomeUnknown {
		t.Fatalf("redirect followed or retried: calls=%d, %+v, %v", calls, result, err)
	}
	deepSeekTestWitness(t, result, 307, body)
}

func TestDeepSeekTransportErrorsAreUnknownAndSanitized(t *testing.T) {
	var fixed string
	for _, upstreamErr := range []error{context.DeadlineExceeded, context.Canceled, io.ErrUnexpectedEOF, errors.New("unit-test-key-not-real PRIVATE-NETWORK-ERROR https://secret.invalid")} {
		calls := 0
		p := deepSeekTestProvider(t, deepSeekTestTransport(func(*http.Request) (*http.Response, error) {
			calls++
			return nil, upstreamErr
		}))
		result, err := p.Search(context.Background(), "query", 1)
		if err == nil || result.Outcome != OutcomeUnknown || calls != 1 || result.HTTPStatus != 0 || result.ResponseSHA256 != "" {
			t.Fatalf("transport failure misclassified: %+v, %v", result, err)
		}
		if fixed == "" {
			fixed = err.Error()
		}
		if err.Error() != fixed || strings.Contains(err.Error(), "unit-test-key") || strings.Contains(err.Error(), "PRIVATE") || strings.Contains(err.Error(), "secret.invalid") {
			t.Fatalf("raw transport error disclosed: %v", err)
		}
	}
}

func TestDeepSeekCanceledContextDoesNotDispatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := deepSeekTestProvider(t, deepSeekTestTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("canceled operation dispatched")
		return nil, nil
	}))
	for _, operation := range []func() (Result, error){
		func() (Result, error) { return p.Search(ctx, "query", 1) },
		func() (Result, error) { return p.Fetch(ctx, "https://example.com/") },
	} {
		result, err := operation()
		if err == nil || result.Outcome != OutcomeUnknown {
			t.Fatalf("cancellation misclassified: %+v, %v", result, err)
		}
	}
}

type deepSeekTestBody struct {
	reader io.Reader
	read   int
	closed bool
	err    error
}

func (b *deepSeekTestBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	b.read += n
	if err == io.EOF && b.err != nil {
		return n, b.err
	}
	return n, err
}

func (b *deepSeekTestBody) Close() error {
	b.closed = true
	return nil
}

func TestDeepSeekResponseByteLimitAndPartialReadWitness(t *testing.T) {
	const limit = 1 << 20
	base := `{"title":"x","content":"ok","padding":""}`
	for _, size := range []int{limit, limit + 1, limit + 100} {
		body := strings.Replace(base, `"padding":""`, `"padding":"`+strings.Repeat("p", size-len(base))+`"`, 1)
		stream := &deepSeekTestBody{reader: strings.NewReader(body)}
		p := deepSeekTestProvider(t, deepSeekTestTransport(func(*http.Request) (*http.Response, error) {
			response := deepSeekTestResponse(200, "")
			response.Body = stream
			return response, nil
		}))
		result, err := p.Fetch(context.Background(), "https://example.com/")
		if !stream.closed || stream.read > limit+1 {
			t.Fatalf("response read is unbounded or not closed: %+v", stream)
		}
		if size == limit && (err != nil || result.Outcome != OutcomeSucceeded || result.Content != "ok") {
			t.Fatalf("exact byte limit rejected: %+v, %v", result, err)
		}
		if size > limit && (err == nil || result.Outcome != OutcomeUnknown || result.Content != "") {
			t.Fatalf("oversized body accepted: %+v, %v", result, err)
		}
		deepSeekTestWitness(t, result, 200, body[:stream.read])
	}
	body := `{"title":"private partial body"`
	stream := &deepSeekTestBody{reader: bytes.NewBufferString(body), err: errors.New("PRIVATE-READ-ERROR")}
	p := deepSeekTestProvider(t, deepSeekTestTransport(func(*http.Request) (*http.Response, error) {
		response := deepSeekTestResponse(200, "")
		response.Body = stream
		return response, nil
	}))
	result, err := p.Fetch(context.Background(), "https://example.com/")
	if err == nil || result.Outcome != OutcomeUnknown || !stream.closed || strings.Contains(err.Error(), "PRIVATE") || strings.Contains(err.Error(), "private partial") {
		t.Fatalf("read failure misclassified or disclosed: %+v, %v", result, err)
	}
	deepSeekTestWitness(t, result, 200, body)
}

func TestDeepSeekFetchRejectsUnsafeURLsWithoutIO(t *testing.T) {
	for _, target := range []string{
		"", "not a URL", "//example.com/path", "file:///etc/passwd", "ftp://example.com", "javascript:alert(1)",
		"https://user:password@example.com/", "https://user@example.com/", "https:///path", "https://example.com:bad/",
		"https://localhost/", "http://LOCALHOST./", "https://sub.localhost/", "https://internal/", "https://a.internal/", "https://a.local/",
		"https://a.local./", "https://metadata/", "https://metadata.google.internal/", "https://instance-data/", "https://instance-data.ec2.internal/",
		"https://127.0.0.1/", "https://127.22.0.1/", "https://0.0.0.0/", "https://10.1.2.3/", "https://172.16.0.1/",
		"https://172.31.255.255/", "https://192.168.1.1/", "https://169.254.169.254/", "https://100.100.100.200/",
		"https://100.64.0.1/", "https://192.0.0.192/", "https://198.18.0.1/", "https://224.0.0.1/", "https://255.255.255.255/",
		"https://[::1]/", "https://[::]/", "https://[fc00::1]/", "https://[fd00:ec2::254]/", "https://[fe80::1]/",
		"https://[ff02::1]/", "https://[::ffff:127.0.0.1]/", "https://[::ffff:7f00:1]/", "https://[::ffff:10.1.2.3]/",
		"https://[::ffff:a9fe:a9fe]/", "https://[fe80::1%25eth0]/", "https://2130706433/", "https://127.1/",
		"https://0177.0.0.1/", "https://0x7f000001/", "https://0x7f.0.0.1/", "https://127.0.0.1./",
		"https://example.com\\@127.0.0.1/", "https://%6cocalhost/", "https://a..example.com/",
		"https://example.com:0/", "https://example.com:65536/", "https://example.com:/", "https://-bad.example/", "https://bad-.example/",
		"https://a.lan/", "https://a.home/", "https://a.home.arpa/", "https://a.localdomain/", "https://metadata.example.com/",
		"https://168.63.129.16/", "https://192.0.2.1/", "https://198.51.100.1/", "https://203.0.113.1/", "https://240.0.0.1/",
		"https://[2001:db8::1]/", "https://[2002:7f00:1::]/", "https://[64:ff9b::7f00:1]/", "https://[::127.0.0.1]/",
		"https://[::ffff:192.168.0.1]/", "https://[::ffff:169.254.169.254]/", "https://[::ffff:0:7f00:1]/",
		"https://" + strings.Repeat("x", 64) + ".example/", "https://example.com..", "https://example.123/",
		"https://localHost\u3002/", "https://\uff4c\uff4f\uff43\uff41\uff4c\uff48\uff4f\uff53\uff54/", "https://example.com/\x00secret",
		"https://[example.com]/", "https://[8.8.8.8]/", "https://2606:4700:4700::1111/",
	} {
		t.Run(target, func(t *testing.T) {
			if err := ValidatePublicURL(target); err == nil {
				t.Fatal("unsafe URL passed pre-reservation validation")
			}
			p := deepSeekTestProvider(t, deepSeekTestTransport(func(*http.Request) (*http.Response, error) {
				t.Fatal("unsafe URL reached official fetch API")
				return nil, nil
			}))
			result, err := p.Fetch(context.Background(), target)
			if err == nil || result.Outcome != OutcomeRejected || result.HTTPStatus != 0 || result.ResponseSHA256 != "" || strings.Contains(err.Error(), "password") {
				t.Fatalf("unsafe URL accepted or leaked: %+v, %v", result, err)
			}
		})
	}
}

func TestDeepSeekFetchAcceptsPublicURLsWithoutResolvingDNS(t *testing.T) {
	for _, target := range []string{
		"https://example.com/", "http://www.example.com:8080/a?q=x", "https://example.com./", "https://unresolvable.example.test/",
		"https://8.8.8.8/", "http://1.1.1.1/", "https://[2606:4700:4700::1111]/", "https://[::ffff:8.8.8.8]/",
		"https://EXAMPLE.COM/a%20b", "https://example.com:65535/", "https://xn--bcher-kva.example/",
	} {
		t.Run(target, func(t *testing.T) {
			if err := ValidatePublicURL(target); err != nil {
				t.Fatalf("public URL failed pre-reservation validation: %v", err)
			}
			calls := 0
			p := deepSeekTestProvider(t, deepSeekTestTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.Host != "api.deepseek.com" || req.URL.Scheme != "https" || req.URL.User != nil {
					t.Fatal("fetched target directly instead of official API")
				}
				return deepSeekTestResponse(200, `{"title":"x","content":"public"}`), nil
			}))
			result, err := p.Fetch(context.Background(), target)
			if err != nil || result.Outcome != OutcomeSucceeded || calls != 1 {
				t.Fatalf("public URL rejected: %+v, %v", result, err)
			}
		})
	}
}

func TestDeepSeekHonorsEarlierDeadlineAndReturnsSanitizedError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	p := deepSeekTestProvider(t, deepSeekTestTransport(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, &url.Error{Op: "POST", URL: "https://secret.invalid/unit-test-key-not-real", Err: req.Context().Err()}
	}))
	result, err := p.Fetch(ctx, "https://example.com/")
	if err == nil || result.Outcome != OutcomeUnknown || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "unit-test-key") {
		t.Fatalf("deadline outcome or privacy violated: %+v, %v", result, err)
	}
}

func TestDeepSeekIncompleteRejectionIsUnknown(t *testing.T) {
	for _, status := range []int{400, 408, 429, 503} {
		body := "partial private error response"
		stream := &deepSeekTestBody{reader: strings.NewReader(body), err: io.ErrUnexpectedEOF}
		p := deepSeekTestProvider(t, deepSeekTestTransport(func(*http.Request) (*http.Response, error) {
			response := deepSeekTestResponse(status, "")
			response.Body = stream
			return response, nil
		}))
		result, err := p.Search(context.Background(), "x", 1)
		if err == nil || result.Outcome != OutcomeUnknown || !stream.closed {
			t.Fatalf("incomplete receipt incorrectly treated as confirmed rejection: %+v, %v", result, err)
		}
		deepSeekTestWitness(t, result, status, body)
	}
}
