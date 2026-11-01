// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package support

import "context"

type ImageInput struct {
	MIMEType   string `json:"mime_type"`
	DataBase64 string `json:"data_base64"`
}
type PromptMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}
type ProviderInput struct {
	Messages []PromptMessage
	Image    *ImageInput
}
type ProviderResult struct {
	Content                                    string
	Outcome                                    string
	InputTokens, OutputTokens, CacheReadTokens *int64
	QuoteCostU                                 *string
	UpstreamCredit                             *string
	QuoteEvidence, UpstreamRequestID           string
}
type Provider interface {
	Complete(context.Context, ProviderInput) (ProviderResult, error)
}
