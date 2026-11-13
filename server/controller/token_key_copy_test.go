package controller

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

const tokenCopyTestMasterKey = "token-copy-test-master-key-0123456789"

type tokenKeyResponse struct {
	ID          int    `json:"id"`
	Key         string `json:"key"`
	KeyPrefix   string `json:"key_prefix"`
	KeyCopyable bool   `json:"key_copyable"`
}

func createCopyableToken(t *testing.T, userID int) tokenKeyResponse {
	t.Helper()
	body := map[string]any{"name": "copyable", "expired_time": -1, "unlimited_quota": true}
	ctx, recorder := newAuthenticatedContext(t, http.MethodPost, "/api/token/", body, userID)
	AddToken(ctx)
	response := decodeAPIResponse(t, recorder)
	if !response.Success {
		t.Fatalf("create failed: %s", response.Message)
	}
	var created tokenKeyResponse
	if err := common.Unmarshal(response.Data, &created); err != nil {
		t.Fatalf("decode create data: %v", err)
	}
	return created
}

func callTokenKeyAction(t *testing.T, action gin.HandlerFunc, path string, tokenID int, userID int) (tokenAPIResponse, int, string) {
	t.Helper()
	ctx, recorder := newAuthenticatedContext(t, http.MethodPost, "/api/token/"+strconv.Itoa(tokenID)+path, nil, userID)
	ctx.Params = gin.Params{{Key: "id", Value: strconv.Itoa(tokenID)}}
	action(ctx)
	return decodeAPIResponse(t, recorder), recorder.Code, recorder.Header().Get("Cache-Control")
}

func TestCreatedTokenKeyCanBeCopiedByItsOwnerOnly(t *testing.T) {
	t.Setenv("ZTAPI_UPSTREAM_MASTER_KEY", tokenCopyTestMasterKey)
	db := setupTokenControllerTestDB(t)
	created := createCopyableToken(t, 31)
	if !created.KeyCopyable {
		t.Fatal("a token created with a master key must be copyable")
	}

	var stored model.Token
	if err := db.First(&stored, created.ID).Error; err != nil {
		t.Fatalf("load stored token: %v", err)
	}
	if stored.KeyCiphertext == "" || strings.Contains(stored.KeyCiphertext, created.Key) {
		t.Fatalf("stored ciphertext = %q, want sealed key", stored.KeyCiphertext)
	}

	listCtx, listRecorder := newAuthenticatedContext(t, http.MethodGet, "/api/token/?p=1&size=10", nil, 31)
	GetAllTokens(listCtx)
	assertNoSecretTokenFields(t, listRecorder.Body.Bytes(), created.Key, stored.KeyHash)
	if strings.Contains(listRecorder.Body.String(), stored.KeyCiphertext) {
		t.Fatalf("list leaked the key ciphertext: %s", listRecorder.Body.String())
	}
	if !strings.Contains(listRecorder.Body.String(), `"key_copyable":true`) {
		t.Fatalf("list did not mark the key copyable: %s", listRecorder.Body.String())
	}

	response, status, cacheControl := callTokenKeyAction(t, RevealTokenKey, "/key", created.ID, 31)
	if !response.Success || status != http.StatusOK {
		t.Fatalf("reveal failed (%d): %s", status, response.Message)
	}
	var revealed tokenKeyResponse
	if err := common.Unmarshal(response.Data, &revealed); err != nil {
		t.Fatalf("decode reveal data: %v", err)
	}
	if revealed.Key != created.Key {
		t.Fatalf("revealed %q, want %q", revealed.Key, created.Key)
	}
	if cacheControl != "no-store" {
		t.Fatalf("reveal Cache-Control = %q, want no-store", cacheControl)
	}

	response, _, _ = callTokenKeyAction(t, RevealTokenKey, "/key", created.ID, 32)
	if response.Success || strings.Contains(string(response.Data), created.Key) {
		t.Fatalf("another user could read the key: %+v", response)
	}
}

func TestHashOnlyTokenCannotBeCopiedButCanBeRegenerated(t *testing.T) {
	t.Setenv("ZTAPI_UPSTREAM_MASTER_KEY", tokenCopyTestMasterKey)
	db := setupTokenControllerTestDB(t)
	legacy, oldPlaintext := seedToken(t, db, 41, "legacy")

	response, status, _ := callTokenKeyAction(t, RevealTokenKey, "/key", legacy.Id, 41)
	if response.Success || status != http.StatusConflict {
		t.Fatalf("reveal of a hash-only token = %d %+v, want 409", status, response)
	}

	response, status, cacheControl := callTokenKeyAction(t, RegenerateTokenKey, "/regenerate", legacy.Id, 41)
	if !response.Success || status != http.StatusOK {
		t.Fatalf("regenerate failed (%d): %s", status, response.Message)
	}
	if cacheControl != "no-store" {
		t.Fatalf("regenerate Cache-Control = %q, want no-store", cacheControl)
	}
	var regenerated tokenKeyResponse
	if err := common.Unmarshal(response.Data, &regenerated); err != nil {
		t.Fatalf("decode regenerate data: %v", err)
	}
	if regenerated.ID != legacy.Id || !regenerated.KeyCopyable || regenerated.Key == oldPlaintext {
		t.Fatalf("regenerated = %+v", regenerated)
	}
	if regenerated.KeyPrefix != regenerated.Key[:12] {
		t.Fatalf("regenerated prefix = %q", regenerated.KeyPrefix)
	}

	if _, err := model.ValidateUserToken(oldPlaintext); err == nil {
		t.Fatal("the replaced key must stop authenticating")
	}
	if token, err := model.ValidateUserToken(regenerated.Key); err != nil || token.Id != legacy.Id {
		t.Fatalf("the new key must authenticate the same token: %+v, %v", token, err)
	}

	response, _, _ = callTokenKeyAction(t, RevealTokenKey, "/key", legacy.Id, 41)
	var revealed tokenKeyResponse
	if err := common.Unmarshal(response.Data, &revealed); err != nil || revealed.Key != regenerated.Key {
		t.Fatalf("regenerated key is not copyable afterwards: %+v, %v", response, err)
	}
}

func TestRegenerateRefusesAnotherUsersToken(t *testing.T) {
	t.Setenv("ZTAPI_UPSTREAM_MASTER_KEY", tokenCopyTestMasterKey)
	db := setupTokenControllerTestDB(t)
	victim, plaintext := seedToken(t, db, 51, "victim")

	response, _, _ := callTokenKeyAction(t, RegenerateTokenKey, "/regenerate", victim.Id, 52)
	if response.Success {
		t.Fatalf("another user regenerated the token: %+v", response)
	}
	if token, err := model.ValidateUserToken(plaintext); err != nil || token.Id != victim.Id {
		t.Fatalf("the owner's key must keep working: %+v, %v", token, err)
	}
}

func TestTokenCreationStillWorksWithoutAMasterKey(t *testing.T) {
	t.Setenv("ZTAPI_UPSTREAM_MASTER_KEY", "")
	setupTokenControllerTestDB(t)
	created := createCopyableToken(t, 61)
	if !strings.HasPrefix(created.Key, "sk-zt-") {
		t.Fatalf("create response key = %q", created.Key)
	}
	if created.KeyCopyable {
		t.Fatal("a key that could not be sealed must not be reported as copyable")
	}
}
