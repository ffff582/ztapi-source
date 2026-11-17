package bridgeapi

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSignatureCoversExactMethodPathNonceAndBody(t *testing.T) {
	secret := []byte("12345678901234567890123456789012")
	sig := Signature(secret, "100", "nonce", "POST", Path, []byte(`{"action":"binding_status"}`))
	require.Len(t, sig, 64)
	require.Equal(t, sig, Signature(secret, "100", "nonce", "POST", Path, []byte(`{"action":"binding_status"}`)))
	require.NotEqual(t, sig, Signature(secret, "100", "nonce2", "POST", Path, []byte(`{"action":"binding_status"}`)))
	require.NotEqual(t, sig, Signature(secret, "100", "nonce", "GET", Path, []byte(`{"action":"binding_status"}`)))
	require.NotEqual(t, sig, Signature(secret, "100", "nonce", "POST", Path, []byte(`{"action":"diagnose"}`)))
}
