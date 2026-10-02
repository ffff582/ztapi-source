package middleware

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

func TestSetupContextForTokenUsesHashOnlyCompatibilityContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)

	const keyHash = "5e884898da28047151d0e56f8dc6292773603d0d6aabbdd62a11ef721d1542d8"
	var token model.Token
	if err := json.Unmarshal([]byte(`{
		"id": 12,
		"user_id": 34,
		"name": "context-contract",
		"key_prefix": "sk-zt-abcde"
	}`), &token); err != nil {
		t.Fatalf("decode cross-version token fixture: %v", err)
	}
	tokenValue := reflect.ValueOf(&token).Elem()
	if field := tokenValue.FieldByName("Key"); field.IsValid() {
		field.SetString("legacy-plaintext-context-value")
	}
	if field := tokenValue.FieldByName("KeyHash"); field.IsValid() {
		field.SetString(keyHash)
	}

	if err := SetupContextForToken(ctx, &token); err != nil {
		t.Fatalf("SetupContextForToken returned error: %v", err)
	}
	if _, exists := ctx.Get("token_key"); exists {
		t.Fatal("context retained legacy plaintext token_key")
	}
	value, exists := ctx.Get("token_key_hash")
	if !exists || value != keyHash {
		t.Fatalf("context token_key_hash = %#v, want %q", value, keyHash)
	}
}
