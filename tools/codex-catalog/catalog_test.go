package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func upstreamResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func testCatalog(t *testing.T, trust string, transport roundTripFunc) *catalog {
	t.Helper()
	cfg, err := readConfig(testEnv(map[string]string{"TRUSTED_PROXY_CIDRS": trust}))
	if err != nil {
		t.Fatal(err)
	}
	h := newCatalog(cfg, []byte(modelFixture))
	h.client.Transport = transport
	return h
}

func catalogRequest() *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/codex/models?client_version=0.165.0", nil)
	r.RemoteAddr = "198.51.100.8:40000"
	r.Header.Set("Authorization", "Bearer sk-TEST-ONLY")
	return r
}

func serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func assertSafeResponse(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Type") != "application/json" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("missing response safety headers: %v", w.Header())
	}
	if !json.Valid(w.Body.Bytes()) {
		t.Errorf("response is not JSON: %q", w.Body.String())
	}
	for _, forbidden := range []string{"sk-TEST-ONLY", "SECRET", "Set-Cookie", "attacker.invalid"} {
		if strings.Contains(w.Body.String()+w.Header().Get("Location")+w.Header().Get("WWW-Authenticate"), forbidden) {
			t.Errorf("response leaked upstream or request information: %q", forbidden)
		}
	}
}

func TestAuthenticatedReadUsesOnlyFixedModelsEndpointAndCanonicalIP(t *testing.T) {
	var calls int
	h := testCatalog(t, "", func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "GET" || r.URL.String() != "http://main-api:3000/v1/models" || r.Body != nil || r.URL.RawQuery != "" {
			t.Errorf("request escaped the fixed read-only endpoint: %v", r)
		}
		if r.Header.Get("Authorization") != "Bearer sk-TEST-ONLY" || r.Header.Get("Accept") != "application/json" {
			t.Error("API key or JSON accept header not forwarded")
		}
		if r.Header.Get("X-Forwarded-For") != "198.51.100.8" || r.Header.Get("X-Real-IP") != "198.51.100.8" {
			t.Error("canonical peer IP was not forwarded")
		}
		for _, name := range []string{"Cookie", "Proxy-Authorization", "Forwarded", "X-Api-Key", "X-Original-URL", "X-Forwarded-Host"} {
			if r.Header.Get(name) != "" {
				t.Errorf("untrusted header forwarded: %s", name)
			}
		}
		deadline, ok := r.Context().Deadline()
		if !ok || time.Until(deadline) > 5*time.Second || time.Until(deadline) <= 0 {
			t.Error("upstream lacks a bounded five-second deadline")
		}
		res := upstreamResponse(200, `{"object":"list","data":[{"id":"other"},{"id":"zt-claude-opus-5","object":"model"}]}`)
		res.Header.Set("Set-Cookie", "SECRET")
		return res, nil
	})
	r := catalogRequest()
	for _, name := range []string{"Cookie", "Proxy-Authorization", "Forwarded", "X-Api-Key", "X-Original-URL", "X-Forwarded-Host"} {
		r.Header.Set(name, "SECRET")
	}
	w := serve(h, r)
	if w.Code != 200 || calls != 1 || strings.TrimSpace(w.Body.String()) != `{"models":[`+modelFixture+`]}` {
		t.Fatalf("unexpected catalog response: status=%d calls=%d body=%s", w.Code, calls, w.Body.String())
	}
	if w.Header().Get("Set-Cookie") != "" {
		t.Fatal("upstream headers leaked")
	}
	assertSafeResponse(t, w)
}

func TestTokenRestrictedOrUnlistedModelReturnsEmptyCatalog(t *testing.T) {
	for _, body := range []string{
		`{"data":[]}`, `{"data":[{"id":"other"}]}`,
		`{"data":[{"id":"zt-claude-opus-5-other"},{"id":"claude-opus-5"}]}`,
	} {
		h := testCatalog(t, "", func(*http.Request) (*http.Response, error) { return upstreamResponse(200, body), nil })
		w := serve(h, catalogRequest())
		if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"models":[]}` {
			t.Errorf("model visibility was broadened: %d %s", w.Code, w.Body.String())
		}
		assertSafeResponse(t, w)
	}
}

func TestEachRequestRechecksTokenVisibilityWithoutCaching(t *testing.T) {
	var calls int
	h := testCatalog(t, "", func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("Authorization") == "Bearer allowed" {
			return upstreamResponse(200, `{"data":[{"id":"zt-claude-opus-5"}]}`), nil
		}
		return upstreamResponse(200, `{"data":[]}`), nil
	})
	for _, token := range []string{"allowed", "restricted", "allowed", "restricted"} {
		r := catalogRequest()
		r.Header.Set("Authorization", "Bearer "+token)
		w := serve(h, r)
		if (strings.TrimSpace(w.Body.String()) == `{"models":[]}`) != (token == "restricted") || w.Code != 200 {
			t.Error("authorization visibility was cached across tokens")
		}
	}
	if calls != 4 {
		t.Fatal("every catalog request must reauthenticate through the existing API")
	}
}

func TestMissingMalformedOrMultipleBearerNeverReachesUpstream(t *testing.T) {
	for _, auth := range [][]string{
		nil, {""}, {"Basic SECRET"}, {"Bearer"}, {"Bearer "}, {"Bearer a b"},
		{"Bearer SECRET, Bearer other"}, {"Bearer a\tb"}, {"Bearer a\r\nb"},
		{"Bearer a", "Bearer b"}, {"Bearer " + strings.Repeat("x", 8193)},
	} {
		h := testCatalog(t, "", func(*http.Request) (*http.Response, error) {
			t.Error("invalid bearer reached upstream")
			return upstreamResponse(200, `{"data":[]}`), nil
		})
		r := catalogRequest()
		r.Header.Del("Authorization")
		for _, value := range auth {
			r.Header.Add("Authorization", value)
		}
		w := serve(h, r)
		if w.Code != 401 || w.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Errorf("invalid auth did not return sanitized 401: %d", w.Code)
		}
		assertSafeResponse(t, w)
	}
}

func TestUnsupportedRoutesMethodsVersionsQueriesAndBodiesNeverReachUpstream(t *testing.T) {
	tests := []struct {
		method, target, body string
		status               int
	}{
		{"GET", "/unknown", "", 404},
		{"GET", "/v1/models", "", 404},
		{"POST", "/v1/responses", `{"input":"SECRET"}`, 404},
		{"POST", "/v1/chat/completions", `{"messages":[]}`, 404},
		{"GET", "/codex/models/", "", 404},
		{"GET", "/codex/%6dodels?client_version=0.165.0", "", 404},
		{"POST", "/codex/models?client_version=0.165.0", "", 405},
		{"HEAD", "/codex/models?client_version=0.165.0", "", 405},
		{"OPTIONS", "/codex/models?client_version=0.165.0", "", 405},
		{"GET", "/codex/models", "", 400},
		{"GET", "/codex/models?client_version=", "", 400},
		{"GET", "/codex/models?client_version=0.165.1", "", 400},
		{"GET", "/codex/models?client_version=0.165.0-alpha.1.2", "", 400},
		{"GET", "/codex/models?client_version=0.165.0&client_version=0.165.0", "", 400},
		{"GET", "/codex/models?client_version=0.165.0&url=http://attacker.invalid/v1/responses", "", 400},
		{"GET", "/codex/models?client_version=0.165.0&api_key=SECRET", "", 400},
		{"GET", "/codex/models?client_version=%zz", "", 400},
		{"GET", "/codex/models?client_version=0.165.0;token=SECRET", "", 400},
		{"GET", "/codex/models?client_version=0.165.0", "SECRET", 400},
		{"GET", "/codex/models?client_version=" + strings.Repeat("x", 4096), "", 400},
	}
	for _, tt := range tests {
		t.Run(tt.method+tt.target, func(t *testing.T) {
			h := testCatalog(t, "", func(*http.Request) (*http.Response, error) {
				t.Error("invalid request or paid endpoint reached upstream")
				return upstreamResponse(200, `{"data":[]}`), nil
			})
			var body io.Reader
			if tt.body != "" {
				body = strings.NewReader(tt.body)
			}
			r := httptest.NewRequest(tt.method, tt.target, body)
			r.Header.Set("Authorization", "Bearer sk-TEST-ONLY")
			w := serve(h, r)
			if w.Code != tt.status {
				t.Errorf("status=%d, want %d", w.Code, tt.status)
			}
			if w.Header().Get("Location") != "" || (tt.status == 405 && w.Header().Get("Allow") != "GET") {
				t.Error("unsafe redirect or missing Allow header")
			}
			assertSafeResponse(t, w)
		})
	}
}

func TestUpstreamDenialsAndFailuresAreSanitized(t *testing.T) {
	for _, tt := range []struct{ upstream, want int }{{401, 401}, {403, 403}, {404, 503}, {429, 503}, {500, 503}, {204, 503}} {
		h := testCatalog(t, "", func(*http.Request) (*http.Response, error) {
			r := upstreamResponse(tt.upstream, `{"error":"SECRET sk-TEST-ONLY"}`)
			r.Header.Set("WWW-Authenticate", "SECRET")
			return r, nil
		})
		w := serve(h, catalogRequest())
		if w.Code != tt.want {
			t.Errorf("upstream %d became %d, want %d", tt.upstream, w.Code, tt.want)
		}
		assertSafeResponse(t, w)
	}
	h := testCatalog(t, "", func(*http.Request) (*http.Response, error) {
		return nil, errors.New("transport leaked SECRET sk-TEST-ONLY http://attacker.invalid")
	})
	w := serve(h, catalogRequest())
	if w.Code != 503 {
		t.Fatal("transport failure did not fail closed")
	}
	assertSafeResponse(t, w)
}

func TestRedirectNeverSendsCredentialToAnotherEndpoint(t *testing.T) {
	for _, location := range []string{"http://attacker.invalid/collect", "/v1/responses", "https://user:SECRET@attacker.invalid/v1/models"} {
		var calls int
		h := testCatalog(t, "", func(r *http.Request) (*http.Response, error) {
			calls++
			if r.URL.String() != "http://main-api:3000/v1/models" {
				t.Error("credential escaped fixed upstream")
			}
			res := upstreamResponse(302, "SECRET")
			res.Header.Set("Location", location)
			return res, nil
		})
		w := serve(h, catalogRequest())
		if w.Code != 503 || calls != 1 {
			t.Fatalf("redirect was followed: status=%d calls=%d", w.Code, calls)
		}
		assertSafeResponse(t, w)
	}
}

func TestMalformedOrOversizedUpstreamNeverGrantsVisibility(t *testing.T) {
	for _, body := range []string{
		``, `null`, `{}`, `[]`, `{"data":null}`, `{"data":{}}`,
		`{"data":[null]}`, `{"data":[{}]}`, `{"data":[{"id":8}]}`,
		`{"data":[{"id":""}]}`, `{"data":[{"id":"zt-claude-opus-5"}]} {}`,
		`{"data":[{"id":"zt-claude-opus-5"},null]}`,
		`{"data":[{"id":"zt-claude-opus-5"}],"extra":"` + string([]byte{0xff}) + `"}`,
		`{"data":[{"id":"zt-claude-opus-5"}]}` + strings.Repeat(" ", 1<<20),
	} {
		h := testCatalog(t, "", func(*http.Request) (*http.Response, error) { return upstreamResponse(200, body), nil })
		w := serve(h, catalogRequest())
		if w.Code != 503 {
			t.Errorf("malformed or oversized upstream accepted: status=%d body length=%d", w.Code, len(body))
		}
		assertSafeResponse(t, w)
	}
}

func TestIPTrustBoundaryAndRightToLeftProxyChain(t *testing.T) {
	tests := []struct{ peer, xff, real, want string }{
		{"198.51.100.8:7", "203.0.113.99", "203.0.113.99", "198.51.100.8"},
		{"198.51.100.8:7", "garbage", "SECRET", "198.51.100.8"},
		{"172.20.0.7:7", "203.0.113.9", "203.0.113.99", "203.0.113.9"},
		{"172.20.0.7:7", "203.0.113.99, 198.51.100.8", "", "198.51.100.8"},
		{"172.20.0.7:7", "203.0.113.99, 198.51.100.8, 172.20.0.8", "", "198.51.100.8"},
		{"172.20.0.7:7", "", "203.0.113.9", "203.0.113.9"},
		{"172.20.0.7:7", "", "", "172.20.0.7"},
		{"[::ffff:198.51.100.8]:7", "203.0.113.99", "", "198.51.100.8"},
		{"172.20.0.7:7", "::ffff:203.0.113.9", "", "203.0.113.9"},
		{"[2001:db8::7]:7", "2001:0db8:1::9", "", "2001:db8:1::9"},
	}
	for _, tt := range tests {
		t.Run(tt.peer+tt.xff+tt.real, func(t *testing.T) {
			h := testCatalog(t, "172.20.0.7/32,172.20.0.8/32,2001:db8::7/128", func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("X-Forwarded-For") != tt.want || r.Header.Get("X-Real-IP") != tt.want {
					t.Errorf("spoofable or noncanonical forwarded IP: %v, want %s", r.Header, tt.want)
				}
				return upstreamResponse(200, `{"data":[]}`), nil
			})
			r := catalogRequest()
			r.RemoteAddr = tt.peer
			if tt.xff != "" {
				r.Header.Set("X-Forwarded-For", tt.xff)
			}
			if tt.real != "" {
				r.Header.Set("X-Real-IP", tt.real)
			}
			w := serve(h, r)
			if w.Code != 200 {
				t.Errorf("valid IP chain rejected: %d", w.Code)
			}
		})
	}
}

func TestMalformedTrustedForwardingFailsClosed(t *testing.T) {
	for _, xff := range []string{"", "SECRET", "203.0.113.9,SECRET", "203.0.113.9,", "203.0.113.9:123", "fe80::1%eth0", "0.0.0.0", strings.Repeat("203.0.113.9,", 32) + "203.0.113.9"} {
		h := testCatalog(t, "172.20.0.7/32", func(*http.Request) (*http.Response, error) {
			t.Error("invalid trusted forwarding reached authorization API")
			return upstreamResponse(200, `{"data":[]}`), nil
		})
		r := catalogRequest()
		r.RemoteAddr = "172.20.0.7:7"
		r.Header.Set("X-Forwarded-For", xff)
		r.Header.Set("X-Real-IP", "203.0.113.99")
		w := serve(h, r)
		if w.Code != 400 {
			t.Errorf("malformed forwarding did not fail closed: %q status=%d", xff, w.Code)
		}
		assertSafeResponse(t, w)
	}
}

func TestConcurrencyIsBoundedWithoutQueuing(t *testing.T) {
	entered := make(chan struct{}, 32)
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	var calls atomic.Int32
	h := testCatalog(t, "", func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		entered <- struct{}{}
		select {
		case <-release:
			return upstreamResponse(200, `{"data":[]}`), nil
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	})
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if w := serve(h, catalogRequest()); w.Code != 200 {
				t.Errorf("in-capacity request failed: %d", w.Code)
			}
		}()
	}
	for i := 0; i < 32; i++ {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("expected capacity never reached")
		}
	}
	w := serve(h, catalogRequest())
	if w.Code != 503 || calls.Load() != 32 {
		t.Error("request beyond bounded concurrency reached upstream")
	}
	assertSafeResponse(t, w)
	once.Do(func() { close(release) })
	wg.Wait()
	if serve(h, catalogRequest()).Code != 200 {
		t.Error("concurrency slot was not released")
	}
}

func TestCanceledRequestIsSanitized(t *testing.T) {
	h := testCatalog(t, "", func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, errors.New("SECRET " + r.Context().Err().Error())
	})
	r := catalogRequest()
	ctx, cancel := context.WithCancel(r.Context())
	cancel()
	w := serve(h, r.WithContext(ctx))
	if w.Code != 503 {
		t.Fatal("cancellation did not fail closed")
	}
	assertSafeResponse(t, w)
}

type trackedBody struct {
	reader io.Reader
	read   int
	closed bool
}

func (b *trackedBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	b.read += n
	return n, err
}

func (b *trackedBody) Close() error { b.closed = true; return nil }

type failedReader struct{}

func (failedReader) Read([]byte) (int, error) { return 0, errors.New("SECRET read failure") }

func TestResponseBodyLimitsReadErrorsAndClosure(t *testing.T) {
	valid := `{"data":[{"id":"zt-claude-opus-5"}]}`
	for _, tt := range []struct {
		name, encoding string
		status         int
		reader         io.Reader
		want           int
	}{
		{"exact-limit", "", 200, strings.NewReader(valid + strings.Repeat(" ", (1<<20)-len(valid))), 200},
		{"over-limit", "", 200, strings.NewReader(strings.Repeat("x", 2<<20)), 503},
		{"read-failure", "", 200, io.MultiReader(strings.NewReader(valid), failedReader{}), 503},
		{"compressed", "gzip", 200, strings.NewReader(valid), 503},
		{"denied", "", 401, failedReader{}, 401},
		{"forbidden", "", 403, failedReader{}, 403},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := &trackedBody{reader: tt.reader}
			h := testCatalog(t, "", func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tt.status, Body: body, Header: http.Header{"Content-Encoding": {tt.encoding}}}, nil
			})
			w := serve(h, catalogRequest())
			if w.Code != tt.want || !body.closed || body.read > (1<<20)+1 {
				t.Errorf("unsafe upstream body lifecycle: status=%d closed=%v read=%d", w.Code, body.closed, body.read)
			}
			assertSafeResponse(t, w)
		})
	}
}

func TestAmbiguousRealIPAndInvalidPeerDoNotReachUpstream(t *testing.T) {
	for _, tt := range []struct {
		peer   string
		values []string
	}{
		{"172.20.0.7:7", []string{"SECRET"}},
		{"172.20.0.7:7", []string{"203.0.113.8", "203.0.113.9"}},
		{"garbage", nil}, {"0.0.0.0:7", nil},
	} {
		h := testCatalog(t, "172.20.0.7/32", func(*http.Request) (*http.Response, error) {
			t.Error("invalid peer or ambiguous real IP reached upstream")
			return upstreamResponse(200, `{"data":[]}`), nil
		})
		r := catalogRequest()
		r.RemoteAddr = tt.peer
		for _, value := range tt.values {
			r.Header.Add("X-Real-IP", value)
		}
		w := serve(h, r)
		if w.Code != 400 {
			t.Errorf("ambiguous client address accepted: %d", w.Code)
		}
		assertSafeResponse(t, w)
	}
}

func TestInternalHealthIsUnauthenticatedAndNeverCallsUpstream(t *testing.T) {
	h := testCatalog(t, "", func(*http.Request) (*http.Response, error) {
		t.Error("health check called upstream")
		return nil, errors.New("SECRET")
	})
	r := httptest.NewRequest("GET", "/healthz", nil)
	w := serve(h, r)
	if w.Code != 200 || w.Body.String() != "healthy\n" || w.Header().Get("Content-Type") != "text/plain; charset=utf-8" ||
		w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("unexpected health response: %d %q %v", w.Code, w.Body.String(), w.Header())
	}
	for _, tt := range []struct {
		method, target string
		status         int
	}{{"POST", "/healthz", 405}, {"HEAD", "/healthz", 405}, {"GET", "/healthz?token=SECRET", 400}, {"GET", "/healthz/", 404}} {
		w := serve(h, httptest.NewRequest(tt.method, tt.target, nil))
		if w.Code != tt.status {
			t.Errorf("unexpected health rejection: %d, want %d", w.Code, tt.status)
		}
		assertSafeResponse(t, w)
	}
}
