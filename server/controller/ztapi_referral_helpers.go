package controller

import (
	"errors"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var ztapiReferralAmountPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,12})(\.[0-9]{1,6})?$`)

func parseZTAPIReferralAmount(value string) (int64, error) {
	if !ztapiReferralAmountPattern.MatchString(value) {
		return 0, model.ErrZTAPIReferralInvalid
	}
	parts := strings.Split(value, ".")
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || whole > math.MaxInt64/1000000 {
		return 0, model.ErrZTAPIReferralOverflow
	}
	fraction := int64(0)
	if len(parts) == 2 {
		fraction, err = strconv.ParseInt(parts[1]+strings.Repeat("0", 6-len(parts[1])), 10, 64)
		if err != nil {
			return 0, model.ErrZTAPIReferralInvalid
		}
	}
	if fraction > math.MaxInt64-whole*1000000 {
		return 0, model.ErrZTAPIReferralOverflow
	}
	return whole*1000000 + fraction, nil
}

func ztapiReferralUser(c *gin.Context) (*model.User, bool) {
	id := c.GetInt("id")
	if id <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "code": "unauthorized", "message": "请先登录"})
		return nil, false
	}
	var user model.User
	if model.DB == nil {
		ztapiReferralHTTPError(c, errors.New("unavailable"))
		return nil, false
	}
	if err := model.DB.WithContext(c.Request.Context()).First(&user, id).Error; err != nil || user.Status != common.UserStatusEnabled {
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			ztapiReferralHTTPError(c, err)
		} else {
			c.JSON(http.StatusUnauthorized, gin.H{"success": false, "code": "unauthorized", "message": "登录状态已失效"})
		}
		return nil, false
	}
	return &user, true
}

func ztapiReferralHTTPError(c *gin.Context, err error) {
	status, code, message := http.StatusServiceUnavailable, "referral_unavailable", "推广服务暂时不可用，请稍后重试"
	switch {
	case errors.Is(err, model.ErrZTAPIReferralNotReady):
		status, code, message = http.StatusConflict, "referral_not_ready", "活动尚未开放，请查看启用检查结果"
	case errors.Is(err, model.ErrZTAPIReferralNotFound), errors.Is(err, gorm.ErrRecordNotFound):
		status, code, message = http.StatusNotFound, "referral_not_found", "记录不存在或不属于当前账号"
	case errors.Is(err, model.ErrZTAPIReferralConflict):
		status, code, message = http.StatusConflict, "referral_conflict", "记录状态已变化，请刷新后重试"
	case errors.Is(err, model.ErrZTAPIReferralInsufficient):
		status, code, message = http.StatusBadRequest, "referral_insufficient", "可提现佣金不足，或佣金仍在冻结期内"
	case errors.Is(err, model.ErrZTAPIReferralInvalid), errors.Is(err, model.ErrZTAPIReferralOverflow):
		status, code, message = http.StatusBadRequest, "referral_invalid", "金额、地址或操作参数无效"
	}
	c.JSON(status, gin.H{"success": false, "code": code, "message": message})
}

func ztapiReferralPage(c *gin.Context) (int, int, bool) {
	page, e1 := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, e2 := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if e1 != nil || e2 != nil || page < 1 || page > 100000 || size < 1 || size > 100 {
		ztapiReferralHTTPError(c, model.ErrZTAPIReferralInvalid)
		return 0, 0, false
	}
	return page, size, true
}

func ztapiReferralWithdrawalProjection(row model.ZTAPIReferralWithdrawal) gin.H {
	return gin.H{"id": row.ID, "version": row.Version, "user_id": row.UserID, "amount": model.FormatZTAPIReferralMicros(row.AmountMicros), "fee": model.FormatZTAPIReferralMicros(row.FeeMicros), "net_amount": model.FormatZTAPIReferralMicros(row.NetAmountMicros), "address": row.Address, "status": row.Status, "created_at": row.CreatedAt, "updated_at": row.UpdatedAt, "fee_expires_at": row.FeeExpiresAt, "paid_at": row.PaidAt, "holder_id": row.HolderID, "tx_id": row.TxID, "note": row.Note}
}
