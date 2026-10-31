package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/hostedsearch"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

var errHostedSearchConfiguration = errors.New("hosted_search_configuration_invalid")

func HostedResponsesOptionsFromEnvironment() (HostedResponsesOptions, bool, error) {
	options := HostedResponsesOptions{}
	if os.Getenv("ZTAPI_HOSTED_SEARCH_ENABLED") != "1" {
		return options, false, nil
	}
	channelID, err := strconv.Atoi(os.Getenv("ZTAPI_HOSTED_SEARCH_CHANNEL_ID"))
	if err != nil || channelID <= 0 {
		return options, true, errHostedSearchConfiguration
	}
	path := os.Getenv("ZTAPI_HOSTED_SEARCH_KEY_FILE")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 16384 || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return options, true, errHostedSearchConfiguration
	}
	file, err := os.Open(path)
	if err != nil {
		return options, true, errHostedSearchConfiguration
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 16385))
	if err != nil || len(raw) > 16384 || common.RejectDuplicateJsonObjectMembers(bytes.NewReader(raw)) != nil {
		return options, true, errHostedSearchConfiguration
	}
	var configuration map[string]json.RawMessage
	if json.Unmarshal(raw, &configuration) != nil {
		return options, true, errHostedSearchConfiguration
	}
	var apiKey string
	var endpoints []string
	if json.Unmarshal(configuration["api_key"], &apiKey) != nil || json.Unmarshal(configuration["allowed_endpoints"], &endpoints) != nil || len(endpoints) != 2 || endpoints[0] != "https://api.deepseek.com/v1/tools/search" || endpoints[1] != "https://api.deepseek.com/v1/tools/fetch" {
		return options, true, errHostedSearchConfiguration
	}
	provider, err := hostedsearch.NewDeepSeekProvider(apiKey, nil)
	if err != nil {
		return options, true, errHostedSearchConfiguration
	}
	hash := sha256.Sum256([]byte(apiKey))
	version := hex.EncodeToString(hash[:])
	apiKey = ""
	cache := hostedsearch.NewPageCache(8*1024*1024, 10*time.Minute)
	options.Provider = provider
	options.Preflight = func(*gin.Context) error { return service.CheckZTAPIHostedToolSource(channelID) }
	options.Authorize = func(c *gin.Context, requested string) bool {
		catalog, err := getAuthorizedPublicCatalog(c)
		if err != nil {
			return false
		}
		for _, entry := range catalog {
			if entry.ModelName == requested {
				return true
			}
		}
		return false
	}
	options.Pages = func(c *gin.Context) hostedsearch.PageStore {
		keyHash := common.GetContextKeyString(c, constant.ContextKeyTokenKeyHash)
		if keyHash == "" || c.GetInt("id") <= 0 || c.GetInt("token_id") <= 0 {
			return nil
		}
		return cache.ForIdentity(strconv.Itoa(c.GetInt("id")) + ":" + strconv.Itoa(c.GetInt("token_id")) + ":" + keyHash)
	}
	options.Dispatch = func(c *gin.Context, parent string) hostedsearch.ToolDispatch {
		userID, tokenID := c.GetInt("id"), c.GetInt("token_id")
		if userID <= 0 || tokenID <= 0 {
			return nil
		}
		return func(ctx context.Context, tool hostedsearch.ToolRequest, call func(context.Context) (hostedsearch.Result, error)) (hostedsearch.Result, error) {
			if err := ctx.Err(); err != nil {
				return hostedsearch.Result{}, err
			}
			row, err := service.ReserveZTAPIHostedToolBilling(service.ZTAPIHostedToolBillingInput{UserID: userID, TokenID: tokenID, ParentRequestID: parent, Action: tool.Action, OfficialChannelID: channelID, CredentialVersion: version})
			if err != nil {
				return hostedsearch.Result{}, errors.New("hosted_search_tool_reservation_failed")
			}
			if err := ctx.Err(); err != nil {
				_, _ = model.ReleaseZTAPIRequestSettlement(row.OperationID)
				return hostedsearch.Result{}, err
			}
			if _, err = service.MarkZTAPIHostedToolBillingDispatched(row.OperationID); err != nil {
				_, _ = model.ReleaseZTAPIRequestSettlement(row.OperationID)
				return hostedsearch.Result{}, errors.New("hosted_search_tool_dispatch_not_recorded")
			}
			result, callErr := call(ctx)
			_, settleErr := service.FinalizeZTAPIHostedToolBilling(row.OperationID, service.ZTAPIHostedToolBillingResult{Outcome: string(result.Outcome), HTTPStatus: result.HTTPStatus, UpstreamRequestID: result.UpstreamRequestID, ResponseSHA256: result.ResponseSHA256, NonEmpty: len(result.Sources) > 0 || strings.TrimSpace(result.Content) != ""})
			if settleErr != nil {
				return result, errors.New("hosted_search_tool_settlement_pending")
			}
			return result, callErr
		}
	}
	return options, true, nil
}
