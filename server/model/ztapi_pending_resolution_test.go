package model

import (
	"github.com/QuantumNous/new-api/common"
	"testing"
)

func TestZTAPIPendingNoChargeRequiresProofAndReleasesExactlyOnce(t *testing.T) {
	db, input := setupZTAPISettlement(t)
	if err := db.AutoMigrate(&ZTAPIPendingResolution{}, &ZTAPIRequestAttempt{}); err != nil {
		t.Fatal(err)
	}
	row, err := BeginZTAPIRequestSettlement(input)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := BeginZTAPIRequestAttempt(row.OperationID, 1, "cred-v1", "chat")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = PendZTAPIRequestSettlement(row.OperationID, "{}", `["upstream_billing_unconfirmed"]`); err != nil {
		t.Fatal(err)
	}
	operator := User{Username: "pending-reviewer", Status: common.UserStatusEnabled, Role: common.RoleRootUser, AffCode: "pending-reviewer"}
	if err = db.Create(&operator).Error; err != nil {
		t.Fatal(err)
	}
	proof := ZTAPINoChargeProof{Source: "provider", ProofID: "no-charge-statement-1", VerificationReference: "verified statement row 5", Attempts: []ZTAPINoChargeAttempt{{Attempt: attempt.Attempt, ChannelID: 1, CredentialVersion: "cred-v1", VerificationReference: "statement-row-5"}}}
	if _, err = ResolveZTAPIPendingNoCharge(row.ID, input.UserID, proof); err == nil {
		t.Fatal("ordinary user approved own release")
	}
	bad := proof
	bad.Attempts = nil
	if _, err = ResolveZTAPIPendingNoCharge(row.ID, operator.Id, bad); err == nil {
		t.Fatal("missing attempt evidence accepted")
	}
	assertZTAPISettlementBalances(t, db, *row, 800, 800)
	for i := 0; i < 2; i++ {
		if _, err = ResolveZTAPIPendingNoCharge(row.ID, operator.Id, proof); err != nil {
			t.Fatal(err)
		}
	}
	assertZTAPISettlementBalances(t, db, *row, 1000, 1000)
	var count int64
	db.Model(&ZTAPIPendingResolution{}).Count(&count)
	if count != 1 {
		t.Fatalf("resolutions %d", count)
	}
	proof.ProofID = "different-proof"
	if _, err = ResolveZTAPIPendingNoCharge(row.ID, operator.Id, proof); err == nil {
		t.Fatal("different proof changed resolved record")
	}
}

func TestZTAPIPendingNoChargeRejectsDurableChargeIntent(t *testing.T) {
	db, row, evidence := setupZTAPISettlementEvidence(t)
	if err := db.AutoMigrate(&ZTAPIPendingResolution{}); err != nil {
		t.Fatal(err)
	}
	saveZTAPIIntentTest(t, row, evidence)
	if _, err := PendZTAPIRequestSettlement(row.OperationID, settlementReplayUsage, `["settlement_retry_required"]`); err != nil {
		t.Fatal(err)
	}
	operator := User{Username: "intent-nocharge-reviewer", Status: common.UserStatusEnabled, Role: common.RoleRootUser, AffCode: "intent-nocharge"}
	if err := db.Create(&operator).Error; err != nil {
		t.Fatal(err)
	}
	var attempts []ZTAPIRequestAttempt
	if err := db.Where("settlement_id = ?", row.ID).Order("attempt").Find(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	proof := ZTAPINoChargeProof{Source: "supplier", ProofID: "wrong-nonbilling", VerificationReference: "claimed no charge"}
	for _, a := range attempts {
		proof.Attempts = append(proof.Attempts, ZTAPINoChargeAttempt{Attempt: a.Attempt, ChannelID: a.ChannelID, CredentialVersion: a.CredentialVersion, UpstreamRequestID: a.UpstreamRequestID, VerificationReference: "claimed row"})
	}
	if _, err := ResolveZTAPIPendingNoCharge(row.ID, operator.Id, proof); err == nil {
		t.Fatal("durable known charge was released as no charge")
	}
	assertZTAPISettlementBalances(t, db, row, 800, 800)
}

func TestZTAPIPendingNoChargeCreditsWhileCustomerRemainsInDebt(t *testing.T) {
	db, input := setupZTAPISettlement(t)
	if err := db.AutoMigrate(&ZTAPIPendingResolution{}, &ZTAPIRequestAttempt{}); err != nil {
		t.Fatal(err)
	}
	row, err := BeginZTAPIRequestSettlement(input)
	if err != nil {
		t.Fatal(err)
	}
	_, err = BeginZTAPIRequestAttempt(row.OperationID, 1, "cred-v1", "chat")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = PendZTAPIRequestSettlement(row.OperationID, "{}", `["upstream_billing_unconfirmed"]`); err != nil {
		t.Fatal(err)
	}
	other := input
	other.OperationID = "other-debt-op"
	other.RequestID = "other-debt-request"
	other.ReservedQuota = 100
	if _, err = BeginZTAPIRequestSettlement(other); err != nil {
		t.Fatal(err)
	}
	if _, err = FinalizeZTAPIRequestSettlement(other.OperationID, 1400, `{"input_tokens":1400}`, `[{"dimension":"input_tokens","units":"1400","unit_quota":"1","charged_quota":1400}]`); err != nil {
		t.Fatal(err)
	}
	operator := User{Username: "debt-release-reviewer", Status: common.UserStatusEnabled, Role: common.RoleRootUser, AffCode: "debt-release"}
	if err = db.Create(&operator).Error; err != nil {
		t.Fatal(err)
	}
	proof := ZTAPINoChargeProof{Source: "supplier", ProofID: "debt-release-proof", VerificationReference: "confirmed no charge", Attempts: []ZTAPINoChargeAttempt{{Attempt: 1, ChannelID: 1, CredentialVersion: "cred-v1", VerificationReference: "statement row"}}}
	if _, err = ResolveZTAPIPendingNoCharge(row.ID, operator.Id, proof); err != nil {
		t.Fatal(err)
	}
	var user User
	if err = db.First(&user, input.UserID).Error; err != nil {
		t.Fatal(err)
	}
	if user.Quota != -400 || user.Status != common.UserStatusDisabled {
		t.Fatalf("credit should reduce debt without enabling account: quota=%d status=%d", user.Quota, user.Status)
	}
}
