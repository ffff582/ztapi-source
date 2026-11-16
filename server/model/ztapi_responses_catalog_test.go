package model

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/require"
)

func TestZTAPIResponsesCatalogUsesPublishedSource(t *testing.T) {
	for _, tt := range []struct {
		source, alias, protocol string
		endpoints               []constant.EndpointType
	}{
		{"gpt-5.4-pro", "zt-gpt-5.4-pro", ZTAPIProtocolOpenAICompatible, []constant.EndpointType{constant.EndpointTypeOpenAIResponse}},
		{"gpt-5.4-pro", "customer-friendly-name", ZTAPIProtocolOpenAICompatible, []constant.EndpointType{constant.EndpointTypeOpenAIResponse}},
		{"o3-pro", "research", ZTAPIProtocolOpenAICompatible, []constant.EndpointType{constant.EndpointTypeOpenAIResponse}},
		// A text model also answers the Anthropic messages endpoint, which is
		// what Claude Code and the Anthropic SDKs speak.
		{"gpt-5.5", "zt-gpt-5.4-pro", ZTAPIProtocolOpenAICompatible, []constant.EndpointType{constant.EndpointTypeOpenAI, constant.EndpointTypeAnthropic}},
		{"claude-opus-4-6", "claude", ZTAPIProtocolAnthropic, []constant.EndpointType{constant.EndpointTypeAnthropic}},
		{"gemini-2.5-pro", "gemini", ZTAPIProtocolGemini, []constant.EndpointType{constant.EndpointTypeGemini}},
	} {
		t.Run(tt.source+"/"+tt.alias, func(t *testing.T) {
			items := buildZTAPIPublicCatalog([]ZTAPIRuntimePublication{{SourceModel: tt.source, PublicName: tt.alias, Protocol: tt.protocol}})
			require.Len(t, items, 1)
			require.Equal(t, tt.endpoints, items[0].SupportedEndpointTypes)
		})
	}
}

func TestZTAPIResponsesCatalogProjectsActualEndpoint(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	seedZTAPIPublicCatalogRecord(t, db, "gpt-5.4-pro", "zt-gpt-5.4-pro", ZTAPIProviderOpenAI, ZTAPIProtocolOpenAICompatible,
		[]string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens})
	catalog, err := ListZTAPIPublicCatalog()
	require.NoError(t, err)
	require.Len(t, catalog, 1)
	endpoints := ProjectZTAPIPublicSupportedEndpoints([]Pricing{{SupportedEndpointTypes: catalog[0].SupportedEndpointTypes}}, nil)
	require.Len(t, endpoints, 1)
	require.Equal(t, "/v1/responses", endpoints[string(constant.EndpointTypeOpenAIResponse)].Path)
	require.Equal(t, "POST", endpoints[string(constant.EndpointTypeOpenAIResponse)].Method)
}
