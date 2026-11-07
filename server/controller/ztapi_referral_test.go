package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestZTAPIReferralAmountInputIsExact(t *testing.T) {
	for _, value := range []string{"-1", "+20", "2e1", "20.0000001", "020", "NaN", "", " 20", "99999999999999999999"} {
		_, err := parseZTAPIReferralAmount(value)
		require.Error(t, err, value)
	}
	for value, expected := range map[string]int64{"0": 0, "20": 20000000, "20.000001": 20000001, "19.999999": 19999999} {
		actual, err := parseZTAPIReferralAmount(value)
		require.NoError(t, err)
		require.Equal(t, expected, actual)
	}
}

func TestZTAPIReferralHandlersDenyAnonymousAndForeignWithdrawals(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, handler := range []gin.HandlerFunc{GetZTAPIReferralSelf, GetZTAPIReferralCommissions, GetZTAPIReferralWithdrawals, RequestZTAPIReferralWithdrawal, ConfirmZTAPIReferralWithdrawal, CancelZTAPIReferralWithdrawal, GetAdminZTAPIReferrals, GetAdminZTAPIReferralReadiness, UpdateZTAPIReferralCampaign, AdminZTAPIReferralWithdrawalAction} {
		recorder := httptest.NewRecorder()
		engine := gin.New()
		engine.POST("/", handler)
		engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`)))
		require.Contains(t, []int{401, 403}, recorder.Code)
	}
	old := model.DB
	t.Cleanup(func() { model.DB = old })
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:referral-http-%d?mode=memory&cache=shared", time.Now().UnixNano())), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	model.DB = db
	require.NoError(t, db.AutoMigrate(&model.User{}))
	require.NoError(t, model.MigrateZTAPIReferral(db))
	owner := model.User{Id: 1, Username: "owner", AffCode: "ownerInvite", Status: common.UserStatusEnabled, Role: common.RoleCommonUser}
	other := model.User{Id: 2, Username: "other", AffCode: "otherInvite", Status: common.UserStatusEnabled, Role: common.RoleCommonUser}
	require.NoError(t, db.Create(&owner).Error)
	require.NoError(t, db.Create(&other).Error)
	w := model.ZTAPIReferralWithdrawal{ID: 1, Version: 1, UserID: owner.Id, AmountMicros: 20000000, NetAmountMicros: 19000000, Address: model.USDTTopUpContractAddress, Status: "fee_quoted", CreatedAt: time.Now().Unix(), FeeExpiresAt: time.Now().Add(time.Hour).Unix(), IdempotencyKey: "test-only", RequestIdentity: "test-only"}
	require.NoError(t, db.Create(&w).Error)
	for _, action := range []struct {
		path    string
		handler gin.HandlerFunc
	}{{"confirm", ConfirmZTAPIReferralWithdrawal}, {"cancel", CancelZTAPIReferralWithdrawal}} {
		engine := gin.New()
		engine.Use(func(c *gin.Context) { c.Set("id", other.Id); c.Set("role", other.Role); c.Next() })
		engine.POST("/:id/"+action.path, action.handler)
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/1/"+action.path, strings.NewReader(`{"version":1}`)))
		require.Contains(t, []int{404, 409}, recorder.Code)
		var after model.ZTAPIReferralWithdrawal
		require.NoError(t, db.First(&after, 1).Error)
		require.Equal(t, w.Status, after.Status)
		require.Equal(t, w.Version, after.Version)
	}
}

func TestZTAPIReferralCampaignUsesFreshRootRole(t *testing.T) {
	old := model.DB
	t.Cleanup(func() { model.DB = old })
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:referral-role-%d?mode=memory&cache=shared", time.Now().UnixNano())), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	model.DB = db
	require.NoError(t, db.AutoMigrate(&model.User{}))
	operator := model.User{Id: 1, Username: "formerRoot", AffCode: "rootInvite", Status: common.UserStatusEnabled, Role: common.RoleFinanceUser}
	require.NoError(t, db.Create(&operator).Error)
	engine := gin.New()
	engine.Use(func(c *gin.Context) { c.Set("id", operator.Id); c.Set("role", common.RoleRootUser); c.Next() })
	engine.POST("/", UpdateZTAPIReferralCampaign)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"version":0,"enabled":true}`)))
	require.Equal(t, 403, recorder.Code)
}

func TestZTAPIReferralExistingWalletReadsSurviveCatalogFailure(t *testing.T) {
	old := model.DB
	t.Cleanup(func() { model.DB = old })
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:referral-wallet-catalog-%d?mode=memory&cache=shared", time.Now().UnixNano())), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	model.DB = db
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.TwoFA{}))
	require.NoError(t, model.MigrateZTAPIReferral(db))
	user := model.User{Id: 1, Username: "existingPromoter", AffCode: "existingInvite", Status: common.UserStatusEnabled, Role: common.RoleRootUser}
	require.NoError(t, db.Create(&user).Error)
	// No catalog tables: new promises must fail closed without blocking old wallet reads.
	for _, handler := range []gin.HandlerFunc{GetZTAPIReferralSelf, GetAdminZTAPIReferrals} {
		recorder := httptest.NewRecorder()
		engine := gin.New()
		engine.Use(func(c *gin.Context) { c.Set("id", user.Id); c.Set("role", user.Role); c.Next() })
		engine.GET("/", handler)
		engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		require.Contains(t, recorder.Body.String(), `"accepting_invites":false`)
		require.Contains(t, recorder.Body.String(), `"available_commission":"0.000000"`)
	}
}
