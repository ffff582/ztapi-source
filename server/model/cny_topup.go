package model

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	CNYTopUpPending    = "pending"
	CNYTopUpPaid       = "paid"
	CNYTopUpRefundHold = "refund_pending"
	CNYTopUpRefunded   = "refunded"
	CNYCreditSource    = "cny_topup_settlement"
	CNYRefundSource    = "cny_topup_refund_hold"
)

var (
	ErrCNYTopUpConflict = errors.New("CNY topup state or request conflict")
	ErrCNYTopUpEvidence = errors.New("CNY payment evidence does not match the order")
	cnyAmountPattern    = regexp.MustCompile(`^[0-9]{1,8}(\.[0-9]{1,3})?$`)
	cnyIDPattern        = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
)

// Cash and service credits are not interchangeable. Fen and quota are stored
// exactly; the display FX and current policy cannot change an existing order.
type CNYTopUpOrder struct {
	ID              int64   `gorm:"primaryKey"`
	OrderNo         string  `gorm:"type:varchar(64);not null;uniqueIndex"`
	UserID          int     `gorm:"not null;uniqueIndex:idx_cny_request,priority:1"`
	RequestKey      string  `gorm:"type:varchar(64);not null;uniqueIndex:idx_cny_request,priority:2"`
	MerchantNo      string  `gorm:"type:varchar(64);not null"`
	AppID           string  `gorm:"type:varchar(64);not null"`
	Method          string  `gorm:"type:varchar(16);not null"`
	PayFen          int64   `gorm:"not null"`
	Quota           int64   `gorm:"not null"`
	CNYPerUnit      string  `gorm:"type:varchar(32);not null"`
	State           string  `gorm:"type:varchar(24);not null"`
	ProviderTradeNo *string `gorm:"type:varchar(64);uniqueIndex"`
	CheckoutURL     string  `gorm:"type:text"`
	CheckoutQR      string  `gorm:"type:text"`
	CreatedAt       int64   `gorm:"not null"`
	PaidAt          int64
	RefundHeldAt    int64
	RefundedAt      int64
	RefundOperator  int
	ProviderRefund  *string `gorm:"type:varchar(64);uniqueIndex"`
	RefundNoticeAt  int64
}

type CNYPaymentEvidence struct {
	OrderNo, MerchantNo, AppID, TradeNo, Method string
	OrderFen, PaidFen                           int64
}

func ValidCNYPaymentID(s string) bool { return cnyIDPattern.MatchString(s) }

func ParseCNYAmountFen(s string) (int64, error) {
	if !cnyAmountPattern.MatchString(s) {
		return 0, ErrCNYTopUpEvidence
	}
	d, err := decimal.NewFromString(s)
	fen := d.Shift(2)
	if err != nil || !fen.IsPositive() || !fen.IsInteger() {
		return 0, ErrCNYTopUpEvidence
	}
	return fen.IntPart(), nil
}

func CNYTopUpQuota(fen int64, cnyPerUnit, quotaPerUnit decimal.Decimal) (int64, error) {
	if fen <= 0 || !cnyPerUnit.IsPositive() || !quotaPerUnit.IsPositive() || !quotaPerUnit.IsInteger() {
		return 0, ErrCNYTopUpEvidence
	}
	quota, _ := decimal.New(fen, -2).Mul(quotaPerUnit).QuoRem(cnyPerUnit, 0)
	if !quota.IsPositive() || quota.GreaterThan(decimal.NewFromInt(maxBalanceLedgerQuota)) {
		return 0, ErrCNYTopUpEvidence
	}
	return quota.IntPart(), nil
}

func CreateCNYTopUpOrder(ctx context.Context, order CNYTopUpOrder) (CNYTopUpOrder, bool, error) {
	if order.UserID <= 0 || order.PayFen <= 0 || order.Quota <= 0 || order.Quota > maxBalanceLedgerQuota || order.CreatedAt <= 0 ||
		!ValidCNYPaymentID(order.OrderNo) || !ValidCNYPaymentID(order.RequestKey) || !ValidCNYPaymentID(order.MerchantNo) || !ValidCNYPaymentID(order.AppID) ||
		(order.Method != "wxpay" && order.Method != "alipay") || len(order.CNYPerUnit) == 0 || len(order.CNYPerUnit) > 32 {
		return CNYTopUpOrder{}, false, ErrCNYTopUpEvidence
	}
	// Only server-computed immutable inputs may be created, never caller status.
	order = CNYTopUpOrder{OrderNo: order.OrderNo, UserID: order.UserID, RequestKey: order.RequestKey, MerchantNo: order.MerchantNo,
		AppID: order.AppID, Method: order.Method, PayFen: order.PayFen, Quota: order.Quota, CNYPerUnit: order.CNYPerUnit,
		CreatedAt: order.CreatedAt, State: CNYTopUpPending}
	db := DB.WithContext(ctx)
	result := db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "user_id"}, {Name: "request_key"}}, DoNothing: true}).Create(&order)
	if result.Error != nil {
		return CNYTopUpOrder{}, false, result.Error
	}
	var existing CNYTopUpOrder
	if err := db.Where("user_id = ? AND request_key = ?", order.UserID, order.RequestKey).First(&existing).Error; err != nil {
		return CNYTopUpOrder{}, false, err
	}
	if existing.PayFen != order.PayFen || existing.Method != order.Method {
		return CNYTopUpOrder{}, false, ErrCNYTopUpConflict
	}
	return existing, result.RowsAffected == 1 && existing.OrderNo == order.OrderNo, nil
}

func GetCNYTopUpOrder(ctx context.Context, userID int, orderNo string) (CNYTopUpOrder, error) {
	var order CNYTopUpOrder
	err := DB.WithContext(ctx).Where("user_id = ? AND order_no = ?", userID, orderNo).First(&order).Error
	return order, err
}

func ListCNYTopUpOrders(ctx context.Context, userID int) ([]CNYTopUpOrder, error) {
	orders := make([]CNYTopUpOrder, 0)
	err := DB.WithContext(ctx).Where("user_id = ?", userID).Order("id DESC").Limit(20).Find(&orders).Error
	return orders, err
}

func ListAdminCNYTopUpOrders(ctx context.Context, number string, offset, limit int) ([]CNYTopUpOrder, int64, error) {
	query := DB.WithContext(ctx).Model(&CNYTopUpOrder{})
	if number != "" {
		query = query.Where("order_no = ? OR provider_trade_no = ?", number, number)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []CNYTopUpOrder
	err := query.Select("order_no", "provider_trade_no", "method", "pay_fen", "state", "created_at", "paid_at").
		Order("id DESC").Offset(offset).Limit(limit).Find(&rows).Error
	return rows, total, err
}

func RecordCNYTopUpCheckout(ctx context.Context, orderNo, providerTrade, checkoutURL, checkoutQR string) (CNYTopUpOrder, error) {
	return mutateCNYTopUp(ctx, orderNo, func(tx *gorm.DB, order *CNYTopUpOrder) error {
		if !ValidCNYPaymentID(providerTrade) || (order.ProviderTradeNo != nil && *order.ProviderTradeNo != providerTrade) {
			return ErrCNYTopUpEvidence
		}
		if order.State != CNYTopUpPending {
			return nil
		}
		order.ProviderTradeNo, order.CheckoutURL, order.CheckoutQR = &providerTrade, checkoutURL, checkoutQR
		return tx.Model(order).Updates(map[string]any{"provider_trade_no": providerTrade, "checkout_url": checkoutURL, "checkout_qr": checkoutQR}).Error
	})
}

func (order CNYTopUpOrder) matchesPayment(evidence CNYPaymentEvidence) bool {
	return order.ProviderTradeNo != nil && *order.ProviderTradeNo == evidence.TradeNo && ValidCNYPaymentID(evidence.TradeNo) &&
		order.MerchantNo == evidence.MerchantNo && order.AppID == evidence.AppID && order.Method == evidence.Method &&
		order.PayFen == evidence.OrderFen && order.PayFen == evidence.PaidFen
}

func ReadCNYTopUpPaymentOrder(ctx context.Context, evidence CNYPaymentEvidence) (CNYTopUpOrder, error) {
	var order CNYTopUpOrder
	if err := DB.WithContext(ctx).Where("order_no = ?", evidence.OrderNo).First(&order).Error; err != nil {
		return CNYTopUpOrder{}, err
	}
	if !order.matchesPayment(evidence) {
		return CNYTopUpOrder{}, ErrCNYTopUpEvidence
	}
	return order, nil
}

func SettleCNYTopUp(ctx context.Context, evidence CNYPaymentEvidence, now time.Time) (CNYTopUpOrder, error) {
	order, err := mutateCNYTopUp(ctx, evidence.OrderNo, func(tx *gorm.DB, order *CNYTopUpOrder) error {
		if !order.matchesPayment(evidence) {
			return ErrCNYTopUpEvidence
		}
		if order.State == CNYTopUpPaid || order.State == CNYTopUpRefundHold || order.State == CNYTopUpRefunded {
			return nil
		}
		if order.State != CNYTopUpPending || order.Quota <= 0 || now.Unix() < order.CreatedAt {
			return ErrCNYTopUpConflict
		}
		if err := writeCNYTopUpLedger(tx, order, order.Quota, 0, CNYCreditSource, now); err != nil {
			return err
		}
		order.State, order.ProviderTradeNo, order.PaidAt = CNYTopUpPaid, &evidence.TradeNo, now.Unix()
		order.CheckoutURL, order.CheckoutQR = "", ""
		return tx.Model(order).Updates(map[string]any{"state": order.State, "provider_trade_no": evidence.TradeNo, "paid_at": order.PaidAt, "checkout_url": "", "checkout_qr": ""}).Error
	})
	return syncCNYTopUpCache(order, err)
}

func HoldCNYTopUpRefund(ctx context.Context, orderNo string, operator int, now time.Time) (CNYTopUpOrder, error) {
	if operator <= 0 {
		return CNYTopUpOrder{}, ErrCNYTopUpEvidence
	}
	order, err := mutateCNYTopUp(ctx, orderNo, func(tx *gorm.DB, order *CNYTopUpOrder) error {
		if order.State == CNYTopUpRefundHold || order.State == CNYTopUpRefunded {
			return nil
		}
		if order.State != CNYTopUpPaid || order.ProviderTradeNo == nil || order.Quota <= 0 || now.Unix() < order.PaidAt {
			return ErrCNYTopUpConflict
		}
		if err := writeCNYTopUpLedger(tx, order, -order.Quota, operator, CNYRefundSource, now); err != nil {
			return err
		}
		order.State, order.RefundHeldAt = CNYTopUpRefundHold, now.Unix()
		return tx.Model(order).Updates(map[string]any{"state": order.State, "refund_held_at": order.RefundHeldAt}).Error
	})
	return syncCNYTopUpCache(order, err)
}

// Confirm is called only after a trusted operator verifies the full original-
// route refund. The provider's TRADE_REFUND notification alone is not that proof.
func ConfirmCNYTopUpRefund(ctx context.Context, orderNo, trade, refund string, refundFen int64, operator int, now time.Time) (CNYTopUpOrder, error) {
	return mutateCNYTopUp(ctx, orderNo, func(tx *gorm.DB, order *CNYTopUpOrder) error {
		if operator <= 0 || !ValidCNYPaymentID(refund) || order.ProviderTradeNo == nil || *order.ProviderTradeNo != trade || order.PayFen != refundFen {
			return ErrCNYTopUpEvidence
		}
		if order.State == CNYTopUpRefunded && order.ProviderRefund != nil && *order.ProviderRefund == refund {
			return nil
		}
		if order.State != CNYTopUpRefundHold || now.Unix() < order.RefundHeldAt {
			return ErrCNYTopUpConflict
		}
		order.State, order.ProviderRefund, order.RefundedAt, order.RefundOperator = CNYTopUpRefunded, &refund, now.Unix(), operator
		return tx.Model(order).Updates(map[string]any{"state": order.State, "provider_refund": refund, "refunded_at": order.RefundedAt, "refund_operator": operator}).Error
	})
}

func RecordCNYRefundNotice(ctx context.Context, evidence CNYPaymentEvidence, now time.Time) error {
	_, err := mutateCNYTopUp(ctx, evidence.OrderNo, func(tx *gorm.DB, order *CNYTopUpOrder) error {
		if !order.matchesPayment(evidence) || order.PaidAt <= 0 {
			return ErrCNYTopUpEvidence
		}
		if order.RefundNoticeAt != 0 {
			return nil
		}
		return tx.Model(order).Update("refund_notice_at", now.Unix()).Error
	})
	return err
}

func mutateCNYTopUp(ctx context.Context, no string, apply func(*gorm.DB, *CNYTopUpOrder) error) (CNYTopUpOrder, error) {
	if DB.Dialector.Name() == "sqlite" {
		balanceLedgerSQLiteMu.Lock()
		defer balanceLedgerSQLiteMu.Unlock()
	}
	var order CNYTopUpOrder
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := cnyLock(tx).Where("order_no = ?", no).First(&order).Error; err != nil {
			return err
		}
		return apply(tx, &order)
	})
	if err != nil {
		return CNYTopUpOrder{}, err
	}
	return order, nil
}

func cnyLock(tx *gorm.DB) *gorm.DB {
	if tx.Dialector.Name() != "sqlite" {
		return tx.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	return tx
}

func writeCNYTopUpLedger(tx *gorm.DB, order *CNYTopUpOrder, delta int64, operator int, source string, now time.Time) error {
	var user User
	if err := cnyLock(tx).First(&user, order.UserID).Error; err != nil {
		return err
	}
	after, err := checkedBalanceLedgerSum(int64(user.Quota), delta)
	if err != nil {
		return err
	}
	ledger := BalanceLedger{UserID: user.Id, OperatorID: operator, Delta: delta, BalanceBefore: int64(user.Quota), BalanceAfter: after,
		Reason: source, IdempotencyKey: source + ":" + order.OrderNo, RequestID: order.OrderNo, SourceType: source, CreatedAt: now.UTC()}
	if err := tx.Create(&ledger).Error; err != nil {
		return err
	}
	res := tx.Model(&User{}).Where("id = ? AND quota = ?", user.Id, user.Quota).Updates(balanceLedgerUserUpdates(user, after, false))
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return ErrCNYTopUpConflict
	}
	return nil
}

func syncCNYTopUpCache(order CNYTopUpOrder, err error) (CNYTopUpOrder, error) {
	if err == nil && balanceLedgerCacheSync(order.UserID) != nil {
		return order, ErrBalanceLedgerCacheSync
	}
	return order, err
}
