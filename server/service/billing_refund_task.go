package service

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/bytedance/gopkg/util/gopool"
)

const (
	billingRefundRetryInterval = time.Minute
	billingRefundRetryBatch    = 100
	billingRefundAlertCount    = 10
)

var (
	billingRefundRetryOnce    sync.Once
	billingRefundRetryRunning atomic.Bool
)

func StartBillingRefundRetryTask() {
	billingRefundRetryOnce.Do(func() {
		if !common.IsMasterNode {
			return
		}
		gopool.Go(func() {
			ticker := time.NewTicker(billingRefundRetryInterval)
			defer ticker.Stop()
			runBillingRefundRetryOnce()
			for range ticker.C {
				runBillingRefundRetryOnce()
			}
		})
	})
}

func runBillingRefundRetryOnce() {
	if !billingRefundRetryRunning.CompareAndSwap(false, true) {
		return
	}
	defer billingRefundRetryRunning.Store(false)

	ctx := context.Background()
	runZTAPICommercialMaintenance(ctx)
	processed, err := model.ProcessPendingBillingRefunds(billingRefundRetryBatch)
	if err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("billing refund retry failed: %v", err))
	}
	pending, countErr := model.CountPendingBillingRefunds()
	if countErr != nil {
		logger.LogWarn(ctx, fmt.Sprintf("billing refund backlog count failed: %v", countErr))
		return
	}
	if pending >= billingRefundAlertCount {
		logger.LogError(ctx, fmt.Sprintf("billing refund backlog requires operator attention: pending=%d", pending))
	} else if common.DebugEnabled && processed > 0 {
		logger.LogDebug(ctx, "billing refund retry completed: processed=%d pending=%d", processed, pending)
	}
	deadLetters, deadLetterErr := model.CountDeadLetterBillingRefunds()
	if deadLetterErr != nil {
		logger.LogWarn(ctx, fmt.Sprintf("billing refund dead-letter count failed: %v", deadLetterErr))
	} else if deadLetters > 0 {
		logger.LogError(ctx, fmt.Sprintf("billing refunds require manual recovery: dead_letters=%d", deadLetters))
	}
}

func runZTAPICommercialMaintenance(ctx context.Context) {
	if err := model.ReconcileZTAPIAttemptBillingReviews(billingRefundRetryBatch); err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("ZTAPI attempt review recovery failed: %v", err))
	}
	if err := model.RetryZTAPISettlementFinalizations(billingRefundRetryBatch); err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("ZTAPI exact settlement retry failed: %v", err))
	}
	if err := model.RetryZTAPIAttemptBillings(billingRefundRetryBatch, EnqueueZTAPIAttemptBillingLogTx); err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("ZTAPI verified attempt billing retry failed: %v", err))
	}
	if err := model.RetryZTAPISettlementCaches(billingRefundRetryBatch); err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("ZTAPI balance cache retry failed: %v", err))
	}
	if _, err := model.RetryZTAPISupplierRefunds(0, billingRefundRetryBatch); err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("ZTAPI supplier refund retry failed: %v", err))
	}
	if _, err := model.ProcessPendingZTAPISettlementLogs(billingRefundRetryBatch); err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("ZTAPI settlement log retry failed: %v", err))
	}
	if err := RunZTAPIFinanceMaintenance(ctx); err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("ZTAPI finance alert retry failed: %v", err))
	}
}
