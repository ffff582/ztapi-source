package model

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestZTAPIHealthManagedUnpublishedAdmission(t *testing.T) {
	s, c, _ := healthFixture(t)
	ticket := healthAdmit(t, s, c, "before-unpublish")
	require.NoError(t, s.DB.Model(&c).Update("published", false).Error)
	admitted, err := s.AdmitRequest(context.Background(), c.PublicNameValue(), "after-unpublish", "request", 1, false)
	require.ErrorIs(t, err, ErrZTAPIModelNotPublic)
	require.Nil(t, admitted)
	require.ErrorIs(t, s.AdmitAttempt(context.Background(), ticket, 1, "openai", healthCredentialVersion(t, "missing-model")), ErrZTAPIModelNotPublic)
}

func TestZTAPIHealthAdmissionCompatibility(t *testing.T) {
	s, c, _ := healthFixture(t)
	ctx := context.Background()
	untracked, err := s.AdmitRequest(ctx, "untracked-model", "untracked", "request", 1, false)
	require.NoError(t, err)
	require.Nil(t, untracked)
	require.NoError(t, s.AdmitAttempt(ctx, nil, 1, "openai", healthCredentialVersion(t, "nil-ticket")))
	ticket := healthAdmit(t, s, c, "before-price-edit")
	require.NoError(t, s.DB.Model(&c).Updates(map[string]any{"version": c.Version + 1, "input_price_per_million": 12}).Error)
	require.NoError(t, s.AdmitAttempt(ctx, ticket, 1, "openai", healthCredentialVersion(t, "stable-route")), "price changes do not invalidate health identity")
	healthRecord(t, s, ticket, "failure")
	state, err := s.GetState(ctx, c.ID)
	require.NoError(t, err)
	require.EqualValues(t, 1, state.ConsecutiveFailures)
	t.Setenv("ZTAPI_HEALTH_ENABLED", "false")
	disabled, err := s.AdmitRequest(ctx, c.PublicNameValue(), "disabled", "request", 1, false)
	require.NoError(t, err)
	require.Nil(t, disabled)
	require.NoError(t, s.DB.Model(state).Update("open", true).Error)
	_, err = s.AdmitRequest(ctx, c.PublicNameValue(), "disabled-open", "request", 1, false)
	require.ErrorIs(t, err, ErrZTAPIHealthCircuitOpen, "disabled instrumentation still enforces persisted trips")
}

func TestZTAPIHealthAdmissionConcurrentTripUnpublish(t *testing.T) {
	s, c, _ := healthFixture(t)
	queried, resume := make(chan struct{}), make(chan struct{})
	var paused atomic.Bool
	const callback = "test:admission-trip-interleave"
	require.NoError(t, s.DB.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), "JOIN ztapi_model_configs AS c") && paused.CompareAndSwap(false, true) {
			close(queried)
			<-resume
		}
	}))
	t.Cleanup(func() { _ = s.DB.Callback().Query().Remove(callback) })
	done := make(chan error, 1)
	go func() {
		_, err := s.AdmitRequest(context.Background(), c.PublicNameValue(), "racing-admission", "request", 1, false)
		done <- err
	}()
	select {
	case <-queried:
	case <-time.After(5 * time.Second):
		close(resume)
		t.Fatal("admission did not reach availability query")
	}
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&ZTAPIHealthState{ModelID: c.ID, Generation: 1, Open: true}).Error; err != nil {
			return err
		}
		return tx.Model(&c).Update("published", false).Error
	})
	close(resume)
	require.NoError(t, err)
	require.ErrorIs(t, <-done, ErrZTAPIHealthCircuitOpen)
}

func healthCacheRegressionFixture(t *testing.T) (*gorm.DB, ZTAPIModelConfig) {
	t.Helper()
	t.Setenv("ZTAPI_HEALTH_ENABLED", "true")
	db := setupZTAPIPublicCatalogTestDB(t)
	require.NoError(t, MigrateZTAPIHealth(db))
	c := seedZTAPIPublicCatalogRecord(t, db, "gpt-5.5", "zt-gpt-5.5", ZTAPIProviderOpenAI, ZTAPIProtocolOpenAICompatible,
		[]string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens})
	require.NoError(t, refreshZTAPIAliasCache())
	return db, c
}

func TestZTAPIHealthCacheRejectsChangedAuthority(t *testing.T) {
	for _, field := range []string{"published", "publication_snapshot_id", "version", "public_name", "source_model", "snapshot_name", "protocol", "provider_family"} {
		t.Run(field, func(t *testing.T) {
			db, c := healthCacheRegressionFixture(t)
			name := c.PublicNameValue()
			updates := map[string]any{"published": false, "publication_snapshot_id": 0, "version": c.Version + 1,
				"public_name": "zt-renamed", "source_model": "changed-source", "protocol": "claude", "provider_family": "anthropic"}
			if field == "snapshot_name" {
				// Seed cross-process snapshot corruption solely to exercise cache fail-closed behavior.
				require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Model(&ZTAPIModelPublicationSnapshot{}).Where("id = ?", c.PublicationSnapshotID).Update("public_name", "zt-renamed").Error)
			} else if field == "public_name" {
				// Identity APIs version changes; unversioned mutable display edits
				// cannot override an already approved immutable snapshot's name.
				require.NoError(t, db.Model(&c).Updates(map[string]any{"public_name": updates[field], "version": c.Version + 1}).Error)
			} else {
				require.NoError(t, db.Model(&c).Update(field, updates[field]).Error)
			}
			// Simulate another process: do not invalidate this process's cache.
			publication, err := GetZTAPIRuntimePublication(name)
			require.ErrorIs(t, err, gorm.ErrRecordNotFound)
			require.Nil(t, publication)
		})
	}
}

func TestZTAPIHealthCacheRefreshInvalidationFence(t *testing.T) {
	db, c := healthCacheRegressionFixture(t)
	queried, resume := make(chan struct{}), make(chan struct{})
	var paused atomic.Bool
	const callback = "test:cache-refresh-interleave"
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "ztapi_model_price_sources" && paused.CompareAndSwap(false, true) {
			close(queried)
			<-resume
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	done := make(chan error, 1)
	go func() { done <- refreshZTAPIAliasCache() }()
	select {
	case <-queried:
	case <-time.After(5 * time.Second):
		close(resume)
		t.Fatal("refresh did not reach snapshot pricing query")
	}
	InvalidateZTAPIAliasCache()
	close(resume)
	<-done
	ztapiAliasCache.RLock()
	loaded := ztapiAliasCache.loaded
	_, found := ztapiAliasCache.aliases[c.PublicNameValue()]
	ztapiAliasCache.RUnlock()
	require.False(t, loaded, "invalidation must defeat an older refresh")
	require.False(t, found)
	publication, err := GetZTAPIRuntimePublication(c.PublicNameValue())
	require.NoError(t, err)
	require.NotNil(t, publication, "a subsequent fresh read can reload current authority")
}

func TestZTAPIHealthCacheInvalidationRetainsEnforcement(t *testing.T) {
	healthCacheRegressionFixture(t)
	// A reader can have finished ensureZTAPIAliasCache before invalidation,
	// then observe these flags while resolving its request under RLock.
	InvalidateZTAPIAliasCache()
	ztapiAliasCache.RLock()
	enforced := ztapiAliasCache.catalogEnforced
	ztapiAliasCache.RUnlock()
	require.True(t, enforced, "cache invalidation must not permit direct-source fallback")
}
