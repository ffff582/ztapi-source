package model

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestZTAPIPublicationCacheRetriesConcurrentRefresh(t *testing.T) {
	db, c := healthCacheRegressionFixture(t)
	var refreshed atomic.Bool
	var refreshErr error
	const callback = "test:publication-concurrent-refresh"
	require.NoError(t, db.Callback().Row().After("gorm:row").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.TableExpr != nil && strings.Contains(tx.Statement.TableExpr.SQL, "ztapi_model_configs AS c") && refreshed.CompareAndSwap(false, true) {
			refreshErr = refreshZTAPIAliasCache()
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Row().Remove(callback) })

	identity, err := ResolveZTAPIRequestIdentity(c.PublicNameValue(), "default")
	require.True(t, refreshed.Load(), "the authority read must overlap a real cache refresh")
	require.NoError(t, refreshErr)
	require.NoError(t, err, "a competing refresh is not a model outage")
	require.Equal(t, c.PublicNameValue(), identity.PublicName)
	require.Equal(t, c.SourceModel, identity.SourceModel)
}

func TestZTAPIPublicationCacheRetriesInvalidationDuringLoad(t *testing.T) {
	db, c := healthCacheRegressionFixture(t)
	InvalidateZTAPIAliasCache()
	var invalidated atomic.Bool
	const callback = "test:publication-concurrent-invalidation"
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "ztapi_model_price_sources" && invalidated.CompareAndSwap(false, true) {
			InvalidateZTAPIAliasCache()
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })

	identity, err := ResolveZTAPIRequestIdentity(c.PublicNameValue(), "default")
	require.True(t, invalidated.Load())
	require.NoError(t, err, "reload current authority instead of leaking a cache race")
	require.Equal(t, c.PublicNameValue(), identity.PublicName)
}

func TestZTAPIPublicationCacheSerializesColdRefresh(t *testing.T) {
	db, c := healthCacheRegressionFixture(t)
	InvalidateZTAPIAliasCache()
	var active, maximum, loads atomic.Int32
	const callback = "test:publication-cold-burst"
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table != "ztapi_model_configs" || tx.Statement.Clauses["WHERE"].Expression != nil {
			return
		}
		loads.Add(1)
		n := active.Add(1)
		defer active.Add(-1)
		for old := maximum.Load(); n > old; old = maximum.Load() {
			if maximum.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	start := make(chan struct{})
	errs := make(chan error, 12)
	var wg sync.WaitGroup
	for i := 0; i < cap(errs); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _, err := ResolveZTAPICanonicalPublicName(c.PublicNameValue())
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.EqualValues(t, 1, maximum.Load(), "cold cache loads must not overlap")
	require.EqualValues(t, 1, loads.Load(), "one cold cache load must serve the entire burst")
}

func TestZTAPIPublicationCacheConflictRetryIsBounded(t *testing.T) {
	db, c := healthCacheRegressionFixture(t)
	InvalidateZTAPIAliasCache()
	loads := 0
	const callback = "test:publication-repeated-invalidation"
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "ztapi_model_price_sources" {
			loads++
			InvalidateZTAPIAliasCache()
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	_, _, err := ResolveZTAPICanonicalPublicName(c.PublicNameValue())
	require.ErrorIs(t, err, ErrZTAPIModelVersionConflict)
	require.Equal(t, 3, loads, "persistent churn must fail closed after a bounded retry")
}

func TestZTAPIPublicationCacheDatabaseFailureDoesNotRetry(t *testing.T) {
	db, c := healthCacheRegressionFixture(t)
	InvalidateZTAPIAliasCache()
	want := errors.New("test database unavailable")
	loads := 0
	const callback = "test:publication-database-failure"
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "ztapi_model_configs" {
			loads++
			tx.AddError(want)
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	_, _, err := ResolveZTAPICanonicalPublicName(c.PublicNameValue())
	require.ErrorIs(t, err, want)
	require.Equal(t, 1, loads, "do not retry arbitrary database faults or serve stale authority")
}
