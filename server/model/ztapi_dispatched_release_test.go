package model

import (
	"errors"
	"strconv"
	"testing"

	"gorm.io/gorm"
)

// setupDispatchedRejection builds a dispatched settlement whose single attempt
// came back with the given upstream status, parked on the unconfirmed-billing hold.
func setupDispatchedRejection(t *testing.T, status int) (*gorm.DB, ZTAPIRequestSettlement) {
	t.Helper()
	db, input := setupZTAPISettlement(t)
	if err := db.AutoMigrate(&ZTAPIRequestAttempt{}, &Channel{}); err != nil {
		t.Fatal(err)
	}
	row, err := BeginZTAPIRequestSettlement(input)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Create(&Channel{Id: 31}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = BeginZTAPIRequestAttempt(row.OperationID, 31, "cred-v1", "responses"); err != nil {
		t.Fatal(err)
	}
	if err = RecordZTAPIRequestAttemptResponse(row.OperationID, 1, 31, status, "wire-1"); err != nil {
		t.Fatal(err)
	}
	if err = MarkZTAPIRequestDispatched(row.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err = PendZTAPIRequestSettlement(row.OperationID, "{}", ZTAPIMissingUpstreamBillingUnconfirmed); err != nil {
		t.Fatal(err)
	}
	if err = db.First(row, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	return db, *row
}

func assertDispatchedReleased(t *testing.T, db *gorm.DB, row ZTAPIRequestSettlement) {
	t.Helper()
	var saved ZTAPIRequestSettlement
	if err := db.First(&saved, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Status != ZTAPISettlementReleased {
		t.Fatalf("status=%q want %q", saved.Status, ZTAPISettlementReleased)
	}
	if saved.ChargedQuota != 0 {
		t.Fatalf("charged=%d want 0", saved.ChargedQuota)
	}
	assertZTAPISettlementBalances(t, db, row, 1000, 1000)
}

func assertDispatchedStillHeld(t *testing.T, db *gorm.DB, row ZTAPIRequestSettlement, err error) {
	t.Helper()
	if !errors.Is(err, ErrZTAPISettlementPending) {
		t.Fatalf("err=%v want ErrZTAPISettlementPending", err)
	}
	var saved ZTAPIRequestSettlement
	if dbErr := db.First(&saved, row.ID).Error; dbErr != nil {
		t.Fatal(dbErr)
	}
	if saved.Status != ZTAPISettlementPending {
		t.Fatalf("status=%q want %q", saved.Status, ZTAPISettlementPending)
	}
	// The reservation must still be withheld; releasing it would hand back money
	// the supplier may have charged us for.
	assertZTAPISettlementBalances(t, db, row, 800, 800)
}

// 503 is the gateway declining to route the request. This is the status behind
// the bulk of the 2026-10-12 Claude holds.
func TestZTAPIDispatchedReleaseReturnsReservationOnUpstreamUnavailable(t *testing.T) {
	db, row := setupDispatchedRejection(t, 503)
	if _, err := ReleaseZTAPIDispatchedRejectedSettlement(row.OperationID); err != nil {
		t.Fatal(err)
	}
	assertDispatchedReleased(t, db, row)
}

// 422 is the supplier rejecting the request body itself, which is what the
// Responses-API traffic produced.
func TestZTAPIDispatchedReleaseReturnsReservationOnUnprocessableRequest(t *testing.T) {
	db, row := setupDispatchedRejection(t, 422)
	if _, err := ReleaseZTAPIDispatchedRejectedSettlement(row.OperationID); err != nil {
		t.Fatal(err)
	}
	assertDispatchedReleased(t, db, row)
}

func TestZTAPIDispatchedReleaseIsIdempotent(t *testing.T) {
	db, row := setupDispatchedRejection(t, 503)
	for i := 0; i < 3; i++ {
		if _, err := ReleaseZTAPIDispatchedRejectedSettlement(row.OperationID); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	assertDispatchedReleased(t, db, row)
	var entries int64
	if err := db.Model(&BalanceLedger{}).Where("user_id = ? AND source_type = ?", row.UserID, BalanceLedgerSourceBillingRefund).Count(&entries).Error; err != nil {
		t.Fatal(err)
	}
	if entries != 1 {
		t.Fatalf("refund ledger entries=%d want 1", entries)
	}
}

// 500, 502 and 504 can follow a model run that the supplier already billed, so
// they must never release customer funds automatically.
func TestZTAPIDispatchedReleaseHoldsAmbiguousUpstreamFailures(t *testing.T) {
	// Each subtest needs its own name, because the test database is keyed on it.
	for _, status := range []int{500, 502, 504} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			db, row := setupDispatchedRejection(t, status)
			_, err := ReleaseZTAPIDispatchedRejectedSettlement(row.OperationID)
			assertDispatchedStillHeld(t, db, row, err)
		})
	}
}

func TestZTAPIDispatchedReleaseHoldsSuccessfulUpstreamResponse(t *testing.T) {
	db, row := setupDispatchedRejection(t, 200)
	_, err := ReleaseZTAPIDispatchedRejectedSettlement(row.OperationID)
	assertDispatchedStillHeld(t, db, row, err)
}

// A dispatched settlement with no attempt row proves nothing either way.
func TestZTAPIDispatchedReleaseHoldsWhenNoAttemptWasRecorded(t *testing.T) {
	db, input := setupZTAPISettlement(t)
	if err := db.AutoMigrate(&ZTAPIRequestAttempt{}, &Channel{}); err != nil {
		t.Fatal(err)
	}
	row, err := BeginZTAPIRequestSettlement(input)
	if err != nil {
		t.Fatal(err)
	}
	if err = MarkZTAPIRequestDispatched(row.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err = PendZTAPIRequestSettlement(row.OperationID, "{}", ZTAPIMissingUpstreamBillingUnconfirmed); err != nil {
		t.Fatal(err)
	}
	_, err = ReleaseZTAPIDispatchedRejectedSettlement(row.OperationID)
	assertDispatchedStillHeld(t, db, *row, err)
}

// Every attempt must clear the bar, not just the last one.
func TestZTAPIDispatchedReleaseRequiresEveryAttemptToProveNonBilling(t *testing.T) {
	db, input := setupZTAPISettlement(t)
	if err := db.AutoMigrate(&ZTAPIRequestAttempt{}, &Channel{}); err != nil {
		t.Fatal(err)
	}
	row, err := BeginZTAPIRequestSettlement(input)
	if err != nil {
		t.Fatal(err)
	}
	for i, status := range []int{503, 200} {
		channelID := 41 + i
		if err = db.Create(&Channel{Id: channelID}).Error; err != nil {
			t.Fatal(err)
		}
		if _, err = BeginZTAPIRequestAttempt(row.OperationID, channelID, "cred-v1", "responses"); err != nil {
			t.Fatal(err)
		}
		if err = RecordZTAPIRequestAttemptResponse(row.OperationID, i+1, channelID, status, "wire-x"); err != nil {
			t.Fatal(err)
		}
	}
	if err = MarkZTAPIRequestDispatched(row.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err = PendZTAPIRequestSettlement(row.OperationID, "{}", ZTAPIMissingUpstreamBillingUnconfirmed); err != nil {
		t.Fatal(err)
	}
	_, err = ReleaseZTAPIDispatchedRejectedSettlement(row.OperationID)
	assertDispatchedStillHeld(t, db, *row, err)
}

// Holds parked for a different reason belong to another review path.
func TestZTAPIDispatchedReleaseLeavesUnrelatedPendingReasonsAlone(t *testing.T) {
	db, input := setupZTAPISettlement(t)
	if err := db.AutoMigrate(&ZTAPIRequestAttempt{}, &Channel{}); err != nil {
		t.Fatal(err)
	}
	row, err := BeginZTAPIRequestSettlement(input)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Create(&Channel{Id: 51}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = BeginZTAPIRequestAttempt(row.OperationID, 51, "cred-v1", "responses"); err != nil {
		t.Fatal(err)
	}
	if err = RecordZTAPIRequestAttemptResponse(row.OperationID, 1, 51, 503, "wire-1"); err != nil {
		t.Fatal(err)
	}
	if err = MarkZTAPIRequestDispatched(row.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err = PendZTAPIRequestSettlement(row.OperationID, "{}", "[\"upstream_usage_missing\"]"); err != nil {
		t.Fatal(err)
	}
	_, err = ReleaseZTAPIDispatchedRejectedSettlement(row.OperationID)
	assertDispatchedStillHeld(t, db, *row, err)
}

// The sweep is what clears a backlog an outage already created. It must return
// only the holds it can prove, and leave the rest untouched.
func TestZTAPIRejectedHoldSweepReturnsOnlyProvableHolds(t *testing.T) {
	db, input := setupZTAPISettlement(t)
	if err := db.AutoMigrate(&ZTAPIRequestAttempt{}, &Channel{}); err != nil {
		t.Fatal(err)
	}
	type seed struct {
		op      string
		status  int
		missing string
		release bool
	}
	seeds := []seed{
		{op: "sweep-503", status: 503, missing: ZTAPIMissingUpstreamBillingUnconfirmed, release: true},
		{op: "sweep-422", status: 422, missing: ZTAPIMissingUpstreamBillingUnconfirmed, release: true},
		{op: "sweep-504", status: 504, missing: ZTAPIMissingUpstreamBillingUnconfirmed, release: false},
		{op: "sweep-200", status: 200, missing: ZTAPIMissingUpstreamBillingUnconfirmed, release: false},
		{op: "sweep-other", status: 503, missing: "[\"upstream_usage_missing\"]", release: false},
	}
	for i, s := range seeds {
		row := input
		row.OperationID = s.op
		row.RequestID = s.op + "-request"
		if _, err := BeginZTAPIRequestSettlement(row); err != nil {
			t.Fatal(err)
		}
		channelID := 61 + i
		if err := db.Create(&Channel{Id: channelID}).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := BeginZTAPIRequestAttempt(s.op, channelID, "cred-v1", "responses"); err != nil {
			t.Fatal(err)
		}
		if err := RecordZTAPIRequestAttemptResponse(s.op, 1, channelID, s.status, "wire"); err != nil {
			t.Fatal(err)
		}
		if err := MarkZTAPIRequestDispatched(s.op); err != nil {
			t.Fatal(err)
		}
		if _, err := PendZTAPIRequestSettlement(s.op, "{}", s.missing); err != nil {
			t.Fatal(err)
		}
	}

	released, err := ReleaseZTAPIRejectedDispatchedHolds(50)
	if err != nil {
		t.Fatal(err)
	}
	if released != 2 {
		t.Fatalf("released=%d want 2", released)
	}
	for _, s := range seeds {
		var saved ZTAPIRequestSettlement
		if err := db.Where("operation_id = ?", s.op).Take(&saved).Error; err != nil {
			t.Fatal(err)
		}
		want := ZTAPISettlementPending
		if s.release {
			want = ZTAPISettlementReleased
		}
		if saved.Status != want {
			t.Fatalf("%s status=%q want %q", s.op, saved.Status, want)
		}
	}
	// Two of five reservations came back; the other three stay withheld.
	assertZTAPISettlementBalances(t, db, input, 1000-3*200, 1000-3*200)
}

func TestZTAPIRejectedHoldSweepRespectsLimitAndIsIdempotent(t *testing.T) {
	db, row := setupDispatchedRejection(t, 503)
	if n, err := ReleaseZTAPIRejectedDispatchedHolds(0); err != nil || n != 0 {
		t.Fatalf("zero limit: n=%d err=%v", n, err)
	}
	for i := 0; i < 2; i++ {
		if _, err := ReleaseZTAPIRejectedDispatchedHolds(50); err != nil {
			t.Fatalf("sweep %d: %v", i, err)
		}
	}
	assertDispatchedReleased(t, db, row)
}

func TestZTAPIUpstreamRejectedBeforeInferenceBoundaries(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 408, 422, 429, 499, 503} {
		if !ztapiUpstreamRejectedBeforeInference(status) {
			t.Fatalf("status %d should prove non-billing", status)
		}
	}
	for _, status := range []int{0, 200, 201, 204, 301, 399, 500, 502, 504, 505} {
		if ztapiUpstreamRejectedBeforeInference(status) {
			t.Fatalf("status %d must not prove non-billing", status)
		}
	}
}
