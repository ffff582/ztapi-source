package openai

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenaiImageManagedJSONDeliveryDropsStaleContentEncoding(t *testing.T) {
	body := append([]byte(`{"data":[{"b64_json":"`), bytes.Repeat([]byte("AAAA"), 1<<20)...)
	body = append(body, []byte(`"}],"usage":`+observedGPTImageUsage+`}`)...)
	for _, tt := range []struct {
		name      string
		encodings http.Header
	}{
		{name: "no encoding"},
		{name: "identity", encodings: http.Header{"Content-Encoding": {"identity"}}},
		{name: "gzip", encodings: http.Header{"Content-Encoding": {"gzip"}}},
		{name: "br", encodings: http.Header{"Content-Encoding": {"br"}}},
		{name: "stacked", encodings: http.Header{"Content-Encoding": {"gzip, br"}}},
		{name: "lowercase name", encodings: http.Header{"content-encoding": {"gzip"}}},
		{name: "duplicate case names", encodings: http.Header{"Content-Encoding": {"gzip"}, "content-encoding": {"br"}}},
		{name: "multiple values", encodings: http.Header{"Content-Encoding": {"gzip", "br"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			info := gptImageEvidenceInfo(t, body)
			header := http.Header{"Content-Type": {"application/json"}, "Content-Length": {"7"}}
			for name, values := range tt.encodings {
				header[name] = append([]string(nil), values...)
			}
			original := header.Clone()
			result := make(chan *gin.Context, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				c, _ := gin.CreateTestContext(w)
				c.Request = r
				_, apiErr := OpenaiImageHandler(c, info, &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(bytes.NewReader(body))})
				if apiErr != nil {
					c.Status(apiErr.StatusCode)
				}
				result <- c
			}))
			defer server.Close()
			transport := &http.Transport{}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
			resp, err := client.Get(server.URL)
			require.NoError(t, err)
			defer resp.Body.Close()
			received, readErr := io.ReadAll(resp.Body)
			c := <-result
			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.NoError(t, readErr, "a validated plain JSON body must not trigger client decompression")
			require.Empty(t, resp.Header.Get("Content-Encoding"))
			require.EqualValues(t, len(body), resp.ContentLength)
			require.Equal(t, body, received)
			require.Equal(t, original, header, "retain the original provider headers for evidence")
			require.Empty(t, c.Errors)
			require.False(t, info.ZTAPIImageDeliveryFailed())
			_, evidence := info.GetZTAPIImageSettlementEvidence()
			require.NotNil(t, evidence)
			require.Equal(t, observedGPTImageUsage, string(evidence.GetRawUsageJSON()))
		})
	}
}
