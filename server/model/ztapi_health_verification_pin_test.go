package model

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newZTAPIHealthVerificationPinFixture(t *testing.T) (*ZTAPIHealthVerificationStore, ZTAPIHealthVerificationCase, time.Time) {
	t.Helper()
	t.Setenv("ZTAPI_UPSTREAM_MASTER_KEY", "verification-pin-test-master-key-0123456789")
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "verification-pin.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(
		&ZTAPIHealthVerificationCase{},
		&ZTAPIHealthState{},
		&ZTAPIModelConfig{},
		&ZTAPIModelPublicationSnapshot{},
		&Channel{},
	))

	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	publicName := "zt-pin-model"
	config := ZTAPIModelConfig{
		ID: 8101, SourceModel: "pin-model", PublicName: &publicName,
		Protocol: "openai-compatible", ProviderFamily: "openai", Published: true,
		PublicationSnapshotID: 9101, Version: 7, EnabledGroups: `["default"]`,
	}
	require.NoError(t, db.Create(&config).Error)
	require.NoError(t, db.Create(&ZTAPIModelPublicationSnapshot{
		ID: 9101, ModelConfigID: config.ID, ModelVersion: config.Version,
		SourceModel: config.SourceModel, PublicName: publicName,
		Protocol: config.Protocol, ProviderFamily: config.ProviderFamily,
		EnabledGroups: `["default"]`, AllowedChannelIDs: `[8201]`, VerificationIDs: `[]`,
	}).Error)
	require.NoError(t, db.Create(&ZTAPIHealthState{ModelID: config.ID, Generation: 4}).Error)
	require.NoError(t, db.Create(&Channel{
		Id: 8201, Name: "pin-channel", Status: common.ChannelStatusEnabled,
		Key: "pin-test-upstream-secret", Models: "pin-model", ZTAPIManaged: true,
	}).Error)

	credential, err := FingerprintZTAPICredential("pin-test-upstream-secret")
	require.NoError(t, err)
	verificationCase := ZTAPIHealthVerificationCase{
		ID: "5d62c1bc-444d-4c44-a4ed-0fdb67575580", ModelID: config.ID,
		ChannelID: 8201, Protocol: "chat", Stream: true,
		CredentialVersion: credential.String(), Generation: 4, SourceEventID: 7101,
		State: "dispatching", ReadyAt: now.Add(-time.Minute).UnixMilli(),
		LeaseToken: "6cc7f103-8777-4489-9bb4-6e3fd24d7308",
		LeaseUntil: now.Add(time.Minute).UnixMilli(), Attempts: 1,
		ProbeRequestID: "ztapi-health:5d62c1bc-444d-4c44-a4ed-0fdb67575580:1",
		DispatchAt:     now.Add(-time.Second).UnixMilli(), CreatedAt: now.Add(-time.Hour).UnixMilli(),
	}
	require.NoError(t, db.Create(&verificationCase).Error)
	return NewZTAPIHealthVerificationStore(db), verificationCase, now
}

func TestZTAPIHealthVerificationPinLoadsOnlyCurrentDispatch(t *testing.T) {
	store, verificationCase, now := newZTAPIHealthVerificationPinFixture(t)

	pin, err := store.LoadDispatchPin(context.Background(), verificationCase.ID, verificationCase.LeaseToken, verificationCase.ProbeRequestID, now)
	require.NoError(t, err)
	require.Equal(t, verificationCase.ID, pin.CaseID)
	require.Equal(t, verificationCase.ModelID, pin.ModelID)
	require.Equal(t, "zt-pin-model", pin.PublicModel)
	require.Equal(t, verificationCase.CredentialVersion, pin.CredentialVersion)

	for name, mutate := range map[string]func(*ZTAPIHealthVerificationCase){
		"wrong lease":      func(c *ZTAPIHealthVerificationCase) { c.LeaseToken = "wrong" },
		"wrong request id": func(c *ZTAPIHealthVerificationCase) { c.ProbeRequestID = "wrong" },
		"expired":          func(c *ZTAPIHealthVerificationCase) { c.LeaseUntil = now.UnixMilli() },
		"not dispatching":  func(c *ZTAPIHealthVerificationCase) { c.State = "claimed" },
	} {
		t.Run(name, func(t *testing.T) {
			copy := verificationCase
			mutate(&copy)
			_, err := store.LoadDispatchPin(context.Background(), verificationCase.ID, copy.LeaseToken, copy.ProbeRequestID, now)
			if name == "expired" || name == "not dispatching" {
				require.NoError(t, store.DB.Model(&ZTAPIHealthVerificationCase{}).Where("id = ?", verificationCase.ID).
					Updates(map[string]any{"state": copy.State, "lease_until": copy.LeaseUntil}).Error)
				_, err = store.LoadDispatchPin(context.Background(), verificationCase.ID, verificationCase.LeaseToken, verificationCase.ProbeRequestID, now)
				require.NoError(t, store.DB.Model(&ZTAPIHealthVerificationCase{}).Where("id = ?", verificationCase.ID).
					Updates(map[string]any{"state": verificationCase.State, "lease_until": verificationCase.LeaseUntil}).Error)
			}
			require.ErrorIs(t, err, ErrZTAPIVerificationPinInvalid)
		})
	}
}

func TestZTAPIHealthVerificationDispatchPinRejectsEveryRouteMismatch(t *testing.T) {
	store, verificationCase, now := newZTAPIHealthVerificationPinFixture(t)
	pin, err := store.LoadDispatchPin(context.Background(), verificationCase.ID, verificationCase.LeaseToken, verificationCase.ProbeRequestID, now)
	require.NoError(t, err)

	valid := ZTAPIHealthVerificationRouteCheck{
		CaseID: pin.CaseID, LeaseToken: pin.LeaseToken, ProbeRequestID: pin.ProbeRequestID,
		ModelID: pin.ModelID, ChannelID: pin.ChannelID, Protocol: pin.Protocol,
		Stream: pin.Stream, CredentialVersion: pin.CredentialVersion, Generation: pin.Generation,
	}
	require.NoError(t, store.ValidateDispatchPin(context.Background(), valid, now))

	tests := map[string]func(*ZTAPIHealthVerificationRouteCheck){
		"case":       func(v *ZTAPIHealthVerificationRouteCheck) { v.CaseID += "x" },
		"lease":      func(v *ZTAPIHealthVerificationRouteCheck) { v.LeaseToken += "x" },
		"request":    func(v *ZTAPIHealthVerificationRouteCheck) { v.ProbeRequestID += "x" },
		"model":      func(v *ZTAPIHealthVerificationRouteCheck) { v.ModelID++ },
		"channel":    func(v *ZTAPIHealthVerificationRouteCheck) { v.ChannelID++ },
		"protocol":   func(v *ZTAPIHealthVerificationRouteCheck) { v.Protocol = "responses" },
		"stream":     func(v *ZTAPIHealthVerificationRouteCheck) { v.Stream = !v.Stream },
		"credential": func(v *ZTAPIHealthVerificationRouteCheck) { v.CredentialVersion = strings.Repeat("f", 64) },
		"generation": func(v *ZTAPIHealthVerificationRouteCheck) { v.Generation++ },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			require.ErrorIs(t, store.ValidateDispatchPin(context.Background(), candidate, now), ErrZTAPIVerificationPinInvalid)
		})
	}
}

func TestZTAPIHealthVerificationPinFailsClosedWhenCurrentRouteChanges(t *testing.T) {
	for name, mutate := range map[string]func(*gorm.DB, ZTAPIHealthVerificationCase) error{
		"generation": func(db *gorm.DB, verificationCase ZTAPIHealthVerificationCase) error {
			return db.Model(&ZTAPIHealthState{}).Where("model_id = ?", verificationCase.ModelID).Update("generation", verificationCase.Generation+1).Error
		},
		"unmanaged channel": func(db *gorm.DB, verificationCase ZTAPIHealthVerificationCase) error {
			return db.Model(&Channel{}).Where("id = ?", verificationCase.ChannelID).Update("ztapi_managed", false).Error
		},
		"rotated credential": func(db *gorm.DB, verificationCase ZTAPIHealthVerificationCase) error {
			ciphertext, err := encryptZTAPIChannelKey("rotated-key")
			if err != nil {
				return err
			}
			return db.Model(&Channel{}).Where("id = ?", verificationCase.ChannelID).UpdateColumn("ztapi_key_ciphertext", ciphertext).Error
		},
		"model removed": func(db *gorm.DB, verificationCase ZTAPIHealthVerificationCase) error {
			return db.Model(&Channel{}).Where("id = ?", verificationCase.ChannelID).Update("models", "other-model").Error
		},
		"snapshot route removed": func(db *gorm.DB, _ ZTAPIHealthVerificationCase) error {
			return db.Exec("UPDATE ztapi_model_publication_snapshots SET allowed_channel_ids = ? WHERE id = ?", `[]`, 9101).Error
		},
	} {
		t.Run(name, func(t *testing.T) {
			store, verificationCase, now := newZTAPIHealthVerificationPinFixture(t)
			check := ZTAPIHealthVerificationRouteCheck{
				CaseID: verificationCase.ID, LeaseToken: verificationCase.LeaseToken, ProbeRequestID: verificationCase.ProbeRequestID,
				ModelID: verificationCase.ModelID, ChannelID: verificationCase.ChannelID, Protocol: verificationCase.Protocol,
				Stream: verificationCase.Stream, CredentialVersion: verificationCase.CredentialVersion, Generation: verificationCase.Generation,
			}
			require.NoError(t, mutate(store.DB, verificationCase))
			require.ErrorIs(t, store.ValidateDispatchPin(context.Background(), check, now), ErrZTAPIVerificationPinInvalid)
		})
	}
}

func TestZTAPIHealthVerificationPinRejectsMalformedPublicationRoute(t *testing.T) {
	store, verificationCase, now := newZTAPIHealthVerificationPinFixture(t)
	require.NoError(t, store.DB.Exec("UPDATE ztapi_model_publication_snapshots SET allowed_channel_ids = ? WHERE id = ?", "[", 9101).Error)

	_, err := store.LoadDispatchPin(context.Background(), verificationCase.ID, verificationCase.LeaseToken, verificationCase.ProbeRequestID, now)
	require.ErrorIs(t, err, ErrZTAPIVerificationPinInvalid)
}
