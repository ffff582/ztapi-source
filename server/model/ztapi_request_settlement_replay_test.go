package model

import (
	"errors"
	"sync"
	"testing"

	"gorm.io/gorm"
)

const settlementReplayUsage = `{"input":100}`
const settlementReplayDimensions = `[{"dimension":"input_tokens","units":"100","unit_quota":"1","charged_quota":100}]`

func setupZTAPISettlementReplay(t *testing.T, withEvidence bool) (*gorm.DB, ZTAPIRequestSettlement, ZTAPISettlementEvidence) {
	t.Helper()
	db, initial, evidence := setupZTAPISettlementEvidence(t)
	row := &initial
	var err error
	if withEvidence {
		row, err = FinalizeZTAPIRequestSettlementWithEvidence(row.OperationID, 100, settlementReplayUsage, settlementReplayDimensions, evidence)
	} else {
		row, err = FinalizeZTAPIRequestSettlement(row.OperationID, 100, settlementReplayUsage, settlementReplayDimensions)
	}
	if err != nil {
		t.Fatal(err)
	}
	return db, *row, evidence
}

func setupZTAPISettlementEvidence(t *testing.T) (*gorm.DB, ZTAPIRequestSettlement, ZTAPISettlementEvidence) {
	t.Helper()
	db, input := setupZTAPISettlement(t)
	if err := db.AutoMigrate(&Channel{}, &ZTAPIRequestAttempt{}); err != nil {
		t.Fatal(err)
	}
	if err := MigrateZTAPISupplierRefund(db); err != nil {
		t.Fatal(err)
	}
	if err := MigrateZTAPISettlementLogOutbox(db); err != nil {
		t.Fatal(err)
	}
	channel := Channel{Id: 11}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	row, err := BeginZTAPIRequestSettlement(input)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int{10, 11} {
		if _, err = BeginZTAPIRequestAttempt(row.OperationID, id, "cred-v1", "chat"); err != nil {
			t.Fatal(err)
		}
	}
	if err = RecordZTAPIRequestAttemptResponse(row.OperationID, 2, 11, 200, "wire-final"); err != nil {
		t.Fatal(err)
	}
	evidence := ZTAPISettlementEvidence{FinalAttempt: 2, ConsumeLog: Log{
		UserId: row.UserID, TokenId: row.TokenID, RequestId: row.RequestID,
		ModelName: row.PublicModel, Quota: 100, Type: LogTypeConsume,
		ChannelId: 11, CreatedAt: 1700000000, UseTime: 3,
		PromptTokens: 90, CompletionTokens: 10, IsStream: true, Group: "default",
	}}
	return db, *row, evidence
}

func assertZTAPISettlementReplayUnchanged(t *testing.T, db *gorm.DB, row ZTAPIRequestSettlement, before ZTAPISettlementLogOutbox) {
	t.Helper()
	assertZTAPISettlementBalances(t, db, row, 900, 900)
	var user User
	var token Token
	var channel Channel
	var saved ZTAPIRequestSettlement
	var after ZTAPISettlementLogOutbox
	for _, query := range []*gorm.DB{
		db.First(&user, row.UserID), db.First(&token, row.TokenID), db.First(&channel, 11),
		db.First(&saved, row.ID), db.Where("operation_id = ?", row.OperationID).Take(&after),
	} {
		if query.Error != nil {
			t.Fatal(query.Error)
		}
	}
	if user.UsedQuota != 100 || user.RequestCount != 1 || token.UsedQuota != 100 || channel.UsedQuota != 100 || saved.FinalAttempt != 2 || saved.ChargedQuota != 100 || saved.TokenChargedQuota != 100 || after != before {
		t.Fatal("replay changed canonical charge, counters, or durable log")
	}
	for _, item := range []struct {
		model any
		want  int64
	}{{&BalanceLedger{}, 2}, {&ZTAPISupplierRefundCharge{}, 1}, {&ZTAPISettlementLogOutbox{}, 1}} {
		var count int64
		if err := db.Model(item.model).Count(&count).Error; err != nil || count != item.want {
			t.Fatalf("duplicate financial record %T: count=%d err=%v", item.model, count, err)
		}
	}
}

func TestZTAPISettlementReplayRejectsConflictingEvidence(t *testing.T) {
	db, row, evidence := setupZTAPISettlementReplay(t, true)
	var original ZTAPISettlementLogOutbox
	if err := db.Where("operation_id = ?", row.OperationID).Take(&original).Error; err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		change func(*ZTAPISettlementEvidence)
	}{
		{"earlier_attempt", func(e *ZTAPISettlementEvidence) { e.FinalAttempt = 1 }},
		{"absent_attempt", func(e *ZTAPISettlementEvidence) { e.FinalAttempt = 0 }},
		{"user", func(e *ZTAPISettlementEvidence) { e.ConsumeLog.UserId++ }},
		{"token", func(e *ZTAPISettlementEvidence) { e.ConsumeLog.TokenId++ }},
		{"request", func(e *ZTAPISettlementEvidence) { e.ConsumeLog.RequestId = "other" }},
		{"model", func(e *ZTAPISettlementEvidence) { e.ConsumeLog.ModelName = "other" }},
		{"quota", func(e *ZTAPISettlementEvidence) { e.ConsumeLog.Quota++ }},
		{"channel", func(e *ZTAPISettlementEvidence) { e.ConsumeLog.ChannelId = 10 }},
		{"type", func(e *ZTAPISettlementEvidence) { e.ConsumeLog.Type++ }},
		{"log_id", func(e *ZTAPISettlementEvidence) { e.ConsumeLog.Id = 1 }},
		{"prompt_usage", func(e *ZTAPISettlementEvidence) { e.ConsumeLog.PromptTokens++ }},
		{"completion_usage", func(e *ZTAPISettlementEvidence) { e.ConsumeLog.CompletionTokens++ }},
		{"stream", func(e *ZTAPISettlementEvidence) { e.ConsumeLog.IsStream = false }},
		{"group", func(e *ZTAPISettlementEvidence) { e.ConsumeLog.Group = "other" }},
		{"upstream", func(e *ZTAPISettlementEvidence) { e.ConsumeLog.UpstreamRequestId = "other" }},
		{"financial_metadata", func(e *ZTAPISettlementEvidence) { e.ConsumeLog.Other = `{"billing_source":"wallet"}` }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			changed := evidence
			tc.change(&changed)
			if _, err := FinalizeZTAPIRequestSettlementWithEvidence(row.OperationID, 100, settlementReplayUsage, settlementReplayDimensions, changed); !errors.Is(err, ErrZTAPISettlementConflict) {
				t.Errorf("conflicting evidence accepted: %v", err)
			}
			assertZTAPISettlementReplayUnchanged(t, db, row, original)
		})
	}
}

func TestZTAPISettlementReplayPreservesLegitimateRetries(t *testing.T) {
	db, row, evidence := setupZTAPISettlementReplay(t, true)
	var original ZTAPISettlementLogOutbox
	if err := db.Where("operation_id = ?", row.OperationID).Take(&original).Error; err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			retry := evidence
			retry.ConsumeLog.CreatedAt += int64(i)
			retry.ConsumeLog.UseTime += i
			if i%2 == 1 {
				retry.ConsumeLog.UpstreamRequestId = "wire-final"
			}
			_, err := FinalizeZTAPIRequestSettlementWithEvidence(row.OperationID, 100, settlementReplayUsage, settlementReplayDimensions, retry)
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := FinalizeZTAPIRequestSettlement(row.OperationID, 100, settlementReplayUsage, settlementReplayDimensions); err != nil {
		t.Fatal(err)
	}
	assertZTAPISettlementReplayUnchanged(t, db, row, original)
}

func TestZTAPISettlementReplayCannotAttachEvidenceRetroactively(t *testing.T) {
	db, row, evidence := setupZTAPISettlementReplay(t, false)
	if _, err := FinalizeZTAPIRequestSettlementWithEvidence(row.OperationID, 100, settlementReplayUsage, settlementReplayDimensions, evidence); !errors.Is(err, ErrZTAPISettlementConflict) {
		t.Fatalf("retroactive evidence accepted: %v", err)
	}
	if _, err := FinalizeZTAPIRequestSettlement(row.OperationID, 100, settlementReplayUsage, settlementReplayDimensions); err != nil {
		t.Fatal(err)
	}
	assertZTAPISettlementBalances(t, db, row, 900, 900)
}

func TestZTAPISettlementReplayRejectsMissingOrCorruptOutbox(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "corrupt", true: "missing"}[missing], func(t *testing.T) {
			db, row, evidence := setupZTAPISettlementReplay(t, true)
			query := db.Where("operation_id = ?", row.OperationID)
			var err error
			if missing {
				err = query.Delete(&ZTAPISettlementLogOutbox{}).Error
			} else {
				err = query.Model(&ZTAPISettlementLogOutbox{}).Update("payload_sha256", "corrupt").Error
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = FinalizeZTAPIRequestSettlementWithEvidence(row.OperationID, 100, settlementReplayUsage, settlementReplayDimensions, evidence); !errors.Is(err, ErrZTAPISettlementConflict) {
				t.Fatalf("unverifiable evidence accepted: %v", err)
			}
			assertZTAPISettlementBalances(t, db, row, 900, 900)
		})
	}
}
