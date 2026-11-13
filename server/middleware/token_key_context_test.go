package middleware

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

func TestSetupContextForTokenStoresOnlyLookupHash(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)

	const plaintext = "sk-zt-middleware-context-fixture"
	keyHash := common.HashZTAPIKey(plaintext)
	token := &model.Token{
		Id:        12,
		UserId:    34,
		KeyHash:   keyHash,
		KeyPrefix: plaintext[:12],
		Name:      "context-contract",
	}

	if err := SetupContextForToken(ctx, token); err != nil {
		t.Fatalf("SetupContextForToken returned error: %v", err)
	}
	if got := common.GetContextKeyString(ctx, constant.ContextKeyTokenKeyHash); got != keyHash {
		t.Fatalf("context lookup hash = %q, want %q", got, keyHash)
	}
	if _, exists := ctx.Get("token_key"); exists {
		t.Fatal("legacy plaintext token_key context value still exists")
	}
}
