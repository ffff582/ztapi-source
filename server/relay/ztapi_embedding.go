package relay

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
)

func validateZTAPIEmbeddingHTTPResponse(response *http.Response, request *dto.EmbeddingRequest) error {
	if response == nil || response.Body == nil || request == nil {
		return errors.New("embedding upstream response is missing")
	}
	defer response.Body.Close()
	if strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		return errors.New("embedding upstream cannot stream")
	}
	count, err := common.ZTAPIEmbeddingInputCount(request.Input)
	if err != nil {
		return err
	}
	const bodyLimit = 16 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, bodyLimit+1))
	if err != nil {
		return err
	}
	if len(body) > bodyLimit {
		return errors.New("embedding upstream response exceeds validation limit")
	}
	if _, err := common.ValidateZTAPIEmbeddingResponse(body, count, 1536); err != nil {
		return err
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	return nil
}
