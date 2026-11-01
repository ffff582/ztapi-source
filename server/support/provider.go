// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package support

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	_ "golang.org/x/image/webp"
)

const MaxImageBytes = 3 << 20

var safeUpstreamID = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

type ProviderConfig struct {
	Endpoint                  string `json:"endpoint"`
	APIKey                    string `json:"api_key"`
	Model                     string `json:"model"`
	ChannelID                 int    `json:"channel_id"`
	Supplier                  string `json:"supplier"`
	Group                     string `json:"group"`
	GroupMultiplier           string `json:"group_multiplier"`
	CashPerCredit             string `json:"cash_per_credit"`
	InputCreditPerMillion     string `json:"input_credit_per_million"`
	OutputCreditPerMillion    string `json:"output_credit_per_million"`
	CacheReadCreditPerMillion string `json:"cache_read_credit_per_million"`
	QuoteEvidence             string `json:"quote_evidence"`
	VisionEnabled             bool   `json:"vision_enabled"`
}

type HTTPProvider struct {
	cfg                               ProviderConfig
	client                            *http.Client
	input, output, cache, group, cash decimal.Decimal
}

func NewHTTPProvider(cfg ProviderConfig, client *http.Client) (*HTTPProvider, error) {
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/v1/chat/completions" {
		return nil, errors.New("invalid support provider endpoint")
	}
	localTest := client != nil && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost")
	if !localTest && (u.Hostname() != "www.xcloudtoken.com" && u.Hostname() != "xcloudtoken.com" && u.Hostname() != "api.xcloudtoken.com") {
		return nil, errors.New("support provider host is not authorized")
	}
	if len(cfg.APIKey) < 16 || len(cfg.APIKey) > 256 || strings.ContainsAny(cfg.APIKey, " \r\n\x00") || cfg.ChannelID <= 0 || cfg.Supplier != "xcloud" || cfg.Group == "" || len(cfg.QuoteEvidence) < 3 || len(cfg.QuoteEvidence) > 8192 {
		return nil, errors.New("invalid support provider identity")
	}
	if cfg.Model != "deepseek-v4.1-flash" && cfg.Model != "deepseek-flash" && cfg.Model != "deepseek-v4-flash" {
		return nil, errors.New("support model must be approved Flash")
	}
	p := &HTTPProvider{cfg: cfg}
	values := []*decimal.Decimal{&p.input, &p.output, &p.cache, &p.group, &p.cash}
	for i, raw := range []string{cfg.InputCreditPerMillion, cfg.OutputCreditPerMillion, cfg.CacheReadCreditPerMillion, cfg.GroupMultiplier, cfg.CashPerCredit} {
		value, e := decimal.NewFromString(raw)
		if e != nil || value.IsNegative() || value.GreaterThan(decimal.NewFromInt(1_000_000)) || (i != 2 && !value.IsPositive()) {
			return nil, errors.New("invalid support quote")
		}
		*values[i] = value
	}
	if client == nil {
		client = &http.Client{Timeout: 45 * time.Second}
	}
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if copy.Timeout == 0 {
		copy.Timeout = 45 * time.Second
	}
	p.client = &copy
	return p, nil
}

func ValidateImage(in *ImageInput) error {
	if in == nil {
		return nil
	}
	formats := map[string]string{"image/png": "png", "image/jpeg": "jpeg", "image/webp": "webp"}
	format, ok := formats[in.MIMEType]
	if !ok || len(in.DataBase64) > base64.StdEncoding.EncodedLen(MaxImageBytes) {
		return errors.New("unsupported support image")
	}
	raw, err := base64.StdEncoding.DecodeString(in.DataBase64)
	if err != nil || len(raw) == 0 || len(raw) > MaxImageBytes {
		return errors.New("invalid support image")
	}
	config, actual, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || actual != format || config.Width <= 0 || config.Height <= 0 || config.Width > 20000 || config.Height > 20000 || int64(config.Width)*int64(config.Height) > 20_000_000 {
		return errors.New("invalid support image dimensions")
	}
	return nil
}

func (p *HTTPProvider) Complete(ctx context.Context, in ProviderInput) (ProviderResult, error) {
	out := ProviderResult{Outcome: "upstream_error", QuoteEvidence: p.cfg.QuoteEvidence}
	if in.Image != nil && (!p.cfg.VisionEnabled || ValidateImage(in.Image) != nil) {
		out.Outcome = "image_not_enabled"
		return out, errors.New("support image is unavailable")
	}
	messages := append([]PromptMessage(nil), in.Messages...)
	if in.Image != nil {
		if len(messages) == 0 || messages[len(messages)-1].Role != "user" {
			out.Outcome = "invalid_prompt"
			return out, errors.New("image needs a user message")
		}
		text, ok := messages[len(messages)-1].Content.(string)
		if !ok {
			return out, errors.New("invalid image message")
		}
		messages[len(messages)-1].Content = []any{map[string]any{"type": "text", "text": text}, map[string]any{"type": "image_url", "image_url": map[string]string{"url": "data:" + in.Image.MIMEType + ";base64," + in.Image.DataBase64}}}
	}
	maxTokens := 1024
	requestBody := struct {
		Model     string            `json:"model"`
		Messages  []PromptMessage   `json:"messages"`
		Stream    *bool             `json:"stream,omitempty"`
		MaxTokens *int              `json:"max_tokens,omitempty"`
		Thinking  map[string]string `json:"thinking"`
	}{Model: p.cfg.Model, Messages: messages, Stream: new(bool), MaxTokens: &maxTokens, Thinking: map[string]string{"type": "disabled"}}
	raw, err := common.Marshal(requestBody)
	if err != nil {
		return out, errors.New("support request encoding failed")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.Endpoint, bytes.NewReader(raw))
	if err != nil {
		return out, errors.New("support provider request failed")
	}
	req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ZTAPI-Support/1.0")
	response, err := p.client.Do(req)
	if err != nil {
		out.Outcome = "upstream_transport"
		return out, errors.New("support upstream transport failed")
	}
	defer response.Body.Close()
	for _, name := range []string{"X-Request-Id", "Request-Id"} {
		if value := response.Header.Get(name); safeUpstreamID.MatchString(value) {
			out.UpstreamRequestID = value
			break
		}
	}
	if response.StatusCode != http.StatusOK {
		switch response.StatusCode {
		case 401, 403:
			out.Outcome = "upstream_auth"
		case 429:
			out.Outcome = "upstream_rate_limit"
		default:
			out.Outcome = "upstream_http_error"
		}
		return out, errors.New("support upstream rejected the request")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil || len(body) > 2<<20 || common.RejectDuplicateJsonObjectMembers(bytes.NewReader(body)) != nil {
		out.Outcome = "invalid_response"
		return out, errors.New("invalid support upstream response")
	}
	var decoded struct {
		ID      string `json:"id"`
		Choices []struct {
			Message struct {
				Content any `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     *int64 `json:"prompt_tokens"`
			CompletionTokens *int64 `json:"completion_tokens"`
			CacheHit         *int64 `json:"prompt_cache_hit_tokens"`
			CacheMiss        *int64 `json:"prompt_cache_miss_tokens"`
			Details          *struct {
				CachedTokens *int64 `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if common.Unmarshal(body, &decoded) != nil {
		out.Outcome = "invalid_response"
		return out, errors.New("invalid support upstream JSON")
	}
	if out.UpstreamRequestID == "" && safeUpstreamID.MatchString(decoded.ID) {
		out.UpstreamRequestID = decoded.ID
	}
	if len(decoded.Choices) == 1 {
		switch content := decoded.Choices[0].Message.Content.(type) {
		case string:
			out.Content = Redact(content)
		case []any:
			for _, part := range content {
				if value, ok := part.(map[string]any); ok && value["type"] == "text" {
					if text, ok := value["text"].(string); ok {
						out.Content += Redact(text)
					}
				}
			}
		}
	}
	u := decoded.Usage
	if u == nil || u.PromptTokens == nil || u.CompletionTokens == nil {
		out.Outcome = "usage_missing"
		return out, errors.New("support upstream usage is missing")
	}
	out.InputTokens, out.OutputTokens = u.PromptTokens, u.CompletionTokens
	cache := u.CacheHit
	if u.Details != nil && u.Details.CachedTokens != nil {
		if cache != nil && *cache != *u.Details.CachedTokens {
			out.Outcome = "usage_conflict"
			return out, errors.New("support upstream cache usage conflicts")
		}
		cache = u.Details.CachedTokens
	}
	if cache == nil && u.CacheMiss != nil && *u.CacheMiss >= 0 && *u.PromptTokens >= *u.CacheMiss {
		hit := *u.PromptTokens - *u.CacheMiss
		cache = &hit
	}
	out.CacheReadTokens = cache
	if *u.PromptTokens < 0 || *u.CompletionTokens < 0 || cache != nil && (*cache < 0 || *cache > *u.PromptTokens) || u.CacheMiss != nil && cache != nil && (*u.CacheMiss < 0 || *u.CacheMiss != *u.PromptTokens-*cache) || strings.TrimSpace(out.Content) != "" && *u.CompletionTokens == 0 {
		out.Outcome = "usage_conflict"
		return out, errors.New("support upstream usage conflicts")
	}
	if cache == nil {
		out.Outcome = "usage_missing"
		return out, errors.New("support upstream cache scope is missing")
	}
	uncached := decimal.NewFromInt(*u.PromptTokens - *cache)
	credit := uncached.Mul(p.input).Add(decimal.NewFromInt(*u.CompletionTokens).Mul(p.output)).Add(decimal.NewFromInt(*cache).Mul(p.cache)).Div(decimal.NewFromInt(1_000_000)).Mul(p.group)
	creditText := credit.String()
	out.UpstreamCredit = &creditText
	amount := credit.Mul(p.cash).String()
	out.QuoteCostU = &amount
	if strings.TrimSpace(out.Content) == "" {
		out.Outcome = "empty_output"
		return out, errors.New("support upstream returned no visible reply")
	}
	out.Outcome = "completed"
	return out, nil
}
