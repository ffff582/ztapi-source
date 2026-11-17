package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

type admissionFailingBody struct{ closed int }

func (body *admissionFailingBody) Read([]byte) (int, error) {
	return 0, errors.New("synthetic truncated error body")
}
func (body *admissionFailingBody) Close() error { body.closed++; return nil }

func TestZTAPIUpstreamAdmissionErrorResponseBodyAlwaysClosed(t *testing.T) {
	body := &admissionFailingBody{}
	resp := &http.Response{StatusCode: 503, Header: make(http.Header), Body: body}
	require.NotNil(t, RelayErrorHandler(context.Background(), resp, false))
	require.Equal(t, 1, body.closed, "a failed error-body read must release the upstream admission lease")
}
