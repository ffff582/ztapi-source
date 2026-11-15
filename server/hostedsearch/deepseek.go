package hostedsearch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
)

const (
	deepSeekSearchURL = "https://api.deepseek.com/v1/tools/search"
	deepSeekFetchURL  = "https://api.deepseek.com/v1/tools/fetch"
	deepSeekTimeout   = 20 * time.Second
	deepSeekMaxBody   = 1 << 20
)

var (
	errDeepSeekKey       = errors.New("deepseek invalid API key")
	errDeepSeekQuery     = errors.New("deepseek invalid search query or count")
	errDeepSeekURL       = errors.New("deepseek requires a credential-free public HTTP(S) URL")
	errDeepSeekTransport = errors.New("deepseek request outcome unknown")
	errDeepSeekRejected  = errors.New("deepseek request rejected")
	errDeepSeekStatus    = errors.New("deepseek unexpected HTTP status; outcome unknown")
	errDeepSeekSchema    = errors.New("deepseek invalid response schema; outcome unknown")
	errDeepSeekTextLimit = errors.New("deepseek response text or source limit exceeded; outcome unknown")
	errDeepSeekBodyLimit = errors.New("deepseek response exceeds 1 MiB; outcome unknown; hash covers received prefix")
	errDeepSeekRead      = errors.New("deepseek response incomplete; outcome unknown; hash covers received bytes")
)

type deepSeekProvider struct {
	apiKey string
	client *http.Client
}

func NewDeepSeekProvider(apiKey string, transport http.RoundTripper) (Provider, error) {
	if len(apiKey) == 0 || len(apiKey) > 4096 {
		return nil, errDeepSeekKey
	}
	for _, c := range apiKey {
		if c < '!' || c > '~' {
			return nil, errDeepSeekKey
		}
	}
	if transport == nil {
		transport = &http.Transport{
			Proxy:                  nil,
			DialContext:            (&net.Dialer{Timeout: deepSeekTimeout, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2:      true,
			DisableCompression:     true,
			MaxIdleConns:           10,
			IdleConnTimeout:        90 * time.Second,
			TLSHandshakeTimeout:    10 * time.Second,
			ResponseHeaderTimeout:  deepSeekTimeout,
			ExpectContinueTimeout:  time.Second,
			MaxResponseHeaderBytes: 64 << 10,
			TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		}
	}
	return &deepSeekProvider{
		apiKey: apiKey,
		client: &http.Client{
			Transport: transport,
			Timeout:   deepSeekTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (p *deepSeekProvider) Search(ctx context.Context, query string, count int) (Result, error) {
	if !utf8.ValidString(query) || utf8.RuneCountInString(query) < 1 || utf8.RuneCountInString(query) > 128 || strings.TrimSpace(query) == "" || count < 1 || count > 10 {
		return Result{Outcome: OutcomeRejected}, errDeepSeekQuery
	}
	result, raw, err := p.request(ctx, deepSeekSearchURL, struct {
		Query       string `json:"query"`
		Count       int    `json:"count"`
		NeedContent bool   `json:"need_content"`
	}{Query: query, Count: count, NeedContent: false})
	if err != nil {
		return result, err
	}
	object, err := deepSeekResponseObject(raw)
	if err != nil {
		return result, err
	}
	var entries []map[string]json.RawMessage
	if common.Unmarshal(object["results"], &entries) != nil || entries == nil {
		return result, errDeepSeekSchema
	}
	if len(entries) > 10 {
		return result, errDeepSeekTextLimit
	}
	sources := make([]Source, 0, len(entries))
	for _, entry := range entries {
		title, titleOK := deepSeekResponseString(entry, "title", true)
		target, urlOK := deepSeekResponseString(entry, "url", true)
		summary, summaryOK := deepSeekResponseString(entry, "summary", true)
		_, contentOK := deepSeekResponseString(entry, "content", false)
		if !titleOK || !urlOK || !summaryOK || !contentOK || ValidatePublicURL(target) != nil {
			return result, errDeepSeekSchema
		}
		if utf8.RuneCountInString(summary) > 4096 {
			return result, errDeepSeekTextLimit
		}
		sources = append(sources, Source{Title: title, URL: target, Summary: summary})
	}
	result.Sources = sources
	result.Outcome = OutcomeSucceeded
	if len(sources) == 0 {
		result.Outcome = OutcomeEmpty
	}
	return result, nil
}

func (p *deepSeekProvider) Fetch(ctx context.Context, target string) (Result, error) {
	if err := ValidatePublicURL(target); err != nil {
		return Result{Outcome: OutcomeRejected}, err
	}
	result, raw, err := p.request(ctx, deepSeekFetchURL, struct {
		URL string `json:"url"`
	}{URL: target})
	if err != nil {
		return result, err
	}
	object, err := deepSeekResponseObject(raw)
	if err != nil {
		return result, err
	}
	title, titleOK := deepSeekResponseString(object, "title", true)
	content, contentOK := deepSeekResponseString(object, "content", true)
	if !titleOK || !contentOK {
		return result, errDeepSeekSchema
	}
	if utf8.RuneCountInString(content) > 64000 {
		return result, errDeepSeekTextLimit
	}
	result.Title, result.Content, result.Outcome = title, content, OutcomeSucceeded
	if strings.TrimSpace(content) == "" {
		result.Outcome = OutcomeEmpty
	}
	return result, nil
}

func (p *deepSeekProvider) request(ctx context.Context, endpoint string, payload any) (Result, []byte, error) {
	result := Result{Outcome: OutcomeUnknown}
	if ctx == nil || ctx.Err() != nil || endpoint != deepSeekSearchURL && endpoint != deepSeekFetchURL {
		return result, nil, errDeepSeekTransport
	}
	ctx, cancel := context.WithTimeout(ctx, deepSeekTimeout)
	defer cancel()
	body, err := common.Marshal(payload)
	if err != nil {
		return result, nil, errDeepSeekTransport
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return result, nil, errDeepSeekTransport
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	response, err := p.client.Do(req)
	if err != nil {
		return result, nil, errDeepSeekTransport
	}
	defer response.Body.Close()
	result.HTTPStatus = response.StatusCode
	result.UpstreamRequestID = deepSeekRequestID(response.Header)
	// Read one sentinel byte, but never drain an unbounded or incomplete response.
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, deepSeekMaxBody+1))
	hash := sha256.Sum256(raw)
	result.ResponseSHA256 = hex.EncodeToString(hash[:])
	if readErr != nil || ctx.Err() != nil {
		return result, nil, errDeepSeekRead
	}
	if len(raw) > deepSeekMaxBody {
		return result, nil, errDeepSeekBodyLimit
	}
	if response.StatusCode >= 400 && response.StatusCode <= 499 && response.StatusCode != http.StatusRequestTimeout {
		result.Outcome = OutcomeRejected
		return result, nil, errDeepSeekRejected
	}
	if response.StatusCode != http.StatusOK {
		return result, nil, errDeepSeekStatus
	}
	return result, raw, nil
}

func deepSeekResponseObject(raw []byte) (map[string]json.RawMessage, error) {
	if !utf8.Valid(raw) || common.RejectDuplicateJsonObjectMembers(bytes.NewReader(raw)) != nil {
		return nil, errDeepSeekSchema
	}
	var object map[string]json.RawMessage
	if common.Unmarshal(raw, &object) != nil || object == nil {
		return nil, errDeepSeekSchema
	}
	return object, nil
}

func deepSeekResponseString(object map[string]json.RawMessage, key string, required bool) (string, bool) {
	raw, exists := object[key]
	if !exists {
		return "", !required
	}
	raw = bytes.TrimSpace(raw)
	var value string
	if len(raw) == 0 || raw[0] != '"' || common.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

func deepSeekRequestID(headers http.Header) string {
	id := ""
	for key, values := range headers {
		if !strings.EqualFold(key, "X-Request-Id") && !strings.EqualFold(key, "X-Ds-Request-Id") {
			continue
		}
		if len(values) != 1 || len(values[0]) == 0 || len(values[0]) > 128 {
			return ""
		}
		for _, c := range values[0] {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
				return ""
			}
		}
		if id != "" && id != values[0] {
			return ""
		}
		id = values[0]
	}
	return id
}

// ValidatePublicURL performs no DNS lookup or network I/O. The remote fetch
// service must also enforce public DNS resolutions and redirect destinations.
func ValidatePublicURL(raw string) error {
	if raw == "" || len(raw) > 8192 || !utf8.ValidString(raw) || strings.Contains(raw, "\\") {
		return errDeepSeekURL
	}
	for _, c := range raw {
		if c <= ' ' || c == 127 {
			return errDeepSeekURL
		}
	}
	target, err := url.Parse(raw)
	if err != nil || target.Opaque != "" || target.User != nil || target.Host == "" || target.Scheme != "http" && target.Scheme != "https" {
		return errDeepSeekURL
	}
	if port := target.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return errDeepSeekURL
		}
	} else if strings.HasSuffix(target.Host, ":") {
		return errDeepSeekURL
	}
	host := strings.TrimSuffix(strings.ToLower(target.Hostname()), ".")
	if addr, err := netip.ParseAddr(host); err == nil {
		if strings.HasPrefix(target.Host, "[") != addr.Is6() || !deepSeekPublicIP(addr) {
			return errDeepSeekURL
		}
		return nil
	}
	if strings.ContainsAny(target.Host, "[]") || len(host) > 253 {
		return errDeepSeekURL
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return errDeepSeekURL
	}
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' || label == "metadata" || label == "instance-data" {
			return errDeepSeekURL
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return errDeepSeekURL
			}
		}
	}
	last := labels[len(labels)-1]
	if strings.Trim(last, "0123456789") == "" || strings.HasPrefix(last, "0x") {
		return errDeepSeekURL
	}
	for _, suffix := range []string{"localhost", "local", "localdomain", "localdomain6", "internal", "intranet", "lan", "home", "home.arpa", "corp"} {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return errDeepSeekURL
		}
	}
	return nil
}

var deepSeekBlockedIPRanges = [...]netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
}

func deepSeekPublicIP(addr netip.Addr) bool {
	if addr.Zone() != "" {
		return false
	}
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr == netip.MustParseAddr("168.63.129.16") {
		return false
	}
	// Exclude IPv6 translation/compatibility prefixes that can encode local IPv4.
	if addr.Is6() && !netip.MustParsePrefix("2000::/3").Contains(addr) {
		return false
	}
	for _, blocked := range deepSeekBlockedIPRanges {
		if blocked.Contains(addr) {
			return false
		}
	}
	return true
}
