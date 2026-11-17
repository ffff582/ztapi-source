package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const maxConcurrentRequests = 32
const upstreamTimeout = 5 * time.Second

type catalog struct {
	client         *http.Client
	upstreamURL    string
	trustedProxies []netip.Prefix
	modelsJSON     []byte
	slots          chan struct{}
}

func newCatalog(cfg config, entry []byte) *catalog {
	return &catalog{
		client: &http.Client{
			Timeout: upstreamTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
			Transport: &http.Transport{
				// Never consult HTTP(S)_PROXY: credentials go only to the configured API.
				Proxy:                  nil,
				DialContext:            (&net.Dialer{Timeout: 2 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				TLSHandshakeTimeout:    3 * time.Second,
				ResponseHeaderTimeout:  4 * time.Second,
				MaxResponseHeaderBytes: 16 << 10,
				MaxConnsPerHost:        maxConcurrentRequests,
				MaxIdleConns:           maxConcurrentRequests,
				MaxIdleConnsPerHost:    maxConcurrentRequests,
				IdleConnTimeout:        30 * time.Second,
				DisableCompression:     true,
			},
		},
		upstreamURL:    cfg.upstream.String(),
		trustedProxies: append([]netip.Prefix(nil), cfg.trustedProxies...),
		modelsJSON:     []byte(`{"models":[` + string(entry) + "]}\n"),
		slots:          make(chan struct{}, maxConcurrentRequests),
	}
}

func (c *catalog) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.EscapedPath() == "/healthz" {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if r.URL.RawQuery != "" || r.URL.ForceQuery || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
			writeError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("healthy\n"))
		return
	}
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	default:
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable, "catalog_busy")
		return
	}
	// No ServeMux path cleaning, redirects, fallback routes, or paid API handlers.
	if r.URL.EscapedPath() != "/codex/models" {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if len(r.RequestURI) > 2048 || len(r.URL.RawQuery) > 128 || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		w.Header().Set("Connection", "close")
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if r.Body != nil && r.Body != http.NoBody {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1))
		if err != nil || len(body) != 0 {
			w.Header().Set("Connection", "close")
			writeError(w, http.StatusBadRequest, "invalid_request")
			return
		}
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query) != 1 || len(query["client_version"]) != 1 || query.Get("client_version") != "0.165.0" {
		writeError(w, http.StatusBadRequest, "unsupported_client_version_or_query")
		return
	}
	auth, ok := bearerAuthorization(r.Header.Values("Authorization"))
	if !ok {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	clientIP, ok := c.clientIP(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_client_ip")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), upstreamTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.upstreamURL, nil)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "upstream_unavailable")
		return
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Forwarded-For", clientIP)
	req.Header.Set("X-Real-IP", clientIP)
	res, err := c.client.Do(req)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "upstream_unavailable")
		return
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if res.StatusCode == http.StatusForbidden {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	if res.StatusCode != http.StatusOK || res.ContentLength > maxJSONBytes || res.Header.Get("Content-Encoding") != "" {
		writeError(w, http.StatusServiceUnavailable, "upstream_unavailable")
		return
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxJSONBytes+1))
	if err != nil || len(data) > maxJSONBytes || !utf8.Valid(data) {
		writeError(w, http.StatusServiceUnavailable, "upstream_unavailable")
		return
	}
	var envelope map[string]json.RawMessage
	var listing []map[string]json.RawMessage
	if !uniqueJSON(data) || json.Unmarshal(data, &envelope) != nil ||
		json.Unmarshal(envelope["data"], &listing) != nil || listing == nil {
		writeError(w, http.StatusServiceUnavailable, "upstream_unavailable")
		return
	}
	found := false
	for _, model := range listing {
		var id string
		if json.Unmarshal(model["id"], &id) != nil || id == "" {
			writeError(w, http.StatusServiceUnavailable, "upstream_unavailable")
			return
		}
		found = found || id == modelSlug
	}
	if found {
		_, _ = w.Write(c.modelsJSON)
		return
	}
	_, _ = w.Write([]byte("{\"models\":[]}\n"))
}

func bearerAuthorization(values []string) (string, bool) {
	if len(values) != 1 || len(values[0]) < 8 || len(values[0]) > 8192 || !strings.EqualFold(values[0][:7], "Bearer ") {
		return "", false
	}
	token := values[0][7:]
	padding := false
	for _, char := range token {
		if char == '=' {
			padding = true
			continue
		}
		if padding || !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("-._~+/", char)) {
			return "", false
		}
	}
	if token[0] == '=' {
		return "", false
	}
	return "Bearer " + token, true
}

func writeError(w http.ResponseWriter, status int, code string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Error string `json:"error"`
	}{Error: code})
}
