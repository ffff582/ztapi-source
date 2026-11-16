package service

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadFromBase64SniffsMissingTextMimeType(t *testing.T) {
	cached, err := loadFromBase64(base64.StdEncoding.EncodeToString([]byte("plain text payload")), "")
	require.NoError(t, err)
	require.Equal(t, "text/plain", cached.MimeType)
}

func TestLoadFromBase64KeepsUnknownBinaryAsOctetStream(t *testing.T) {
	cached, err := loadFromBase64(base64.StdEncoding.EncodeToString([]byte{0, 1, 2, 3}), "")
	require.NoError(t, err)
	require.Equal(t, "application/octet-stream", cached.MimeType)
}
