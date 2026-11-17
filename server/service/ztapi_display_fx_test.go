package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestZTAPIDisplayFXReferenceIsIndependentAndPersistent(t *testing.T) {
	// Reload validates the persisted reference against the wall clock.
	now := time.Now().UTC()
	rateDate := now.AddDate(0, 0, -2).Format(time.DateOnly)
	s := newZTAPIDisplayFXStore(filepath.Join(t.TempDir(), "display.json"))
	err := s.refresh(context.Background(), func(context.Context) (*ZTAPIDisplayFXQuote, error) {
		return &ZTAPIDisplayFXQuote{CNYPerUnit: "6.67129422", RateDate: rateDate, FetchedAt: now.Unix()}, nil
	}, now)
	require.NoError(t, err)
	q := s.snapshot(now)
	require.True(t, q.Available)
	require.False(t, q.Stale)
	require.Equal(t, "6.67129422", q.CNYPerUnit)
	require.Equal(t, "ecb_usd_cny_reference", q.Source)
	require.Error(t, s.refresh(context.Background(), func(context.Context) (*ZTAPIDisplayFXQuote, error) {
		return nil, errors.New("source unavailable")
	}, now.Add(time.Hour)))
	require.Equal(t, q.CNYPerUnit, s.snapshot(now.Add(time.Hour)).CNYPerUnit)
	reloaded := newZTAPIDisplayFXStore(s.path)
	require.Equal(t, q.CNYPerUnit, reloaded.snapshot(now).CNYPerUnit)
	require.True(t, reloaded.snapshot(now.Add(8*time.Hour)).Stale)
	require.False(t, reloaded.snapshot(now.Add(8*24*time.Hour)).Available)
	require.Error(t, s.refresh(context.Background(), func(context.Context) (*ZTAPIDisplayFXQuote, error) {
		return &ZTAPIDisplayFXQuote{CNYPerUnit: "6.50", RateDate: now.AddDate(0, 0, -3).Format(time.DateOnly), FetchedAt: now.Unix()}, nil
	}, now))
	require.Equal(t, q.CNYPerUnit, s.snapshot(now).CNYPerUnit)
}

func TestZTAPIDisplayFXRejectsInvalidAndRegressingReferences(t *testing.T) {
	now := time.Date(2026, 11, 8, 8, 0, 0, 0, time.UTC)
	for _, quote := range []ZTAPIDisplayFXQuote{
		{CNYPerUnit: "0", RateDate: "2026-11-06"},
		{CNYPerUnit: "NaN", RateDate: "2026-11-06"},
		{CNYPerUnit: "500", RateDate: "2026-11-06"},
		{CNYPerUnit: "6.67", RateDate: "2026-11-09"},
		{CNYPerUnit: "6.67", RateDate: "bad-date"},
		{CNYPerUnit: "6.67", RateDate: "2026-10-01"},
	} {
		s := newZTAPIDisplayFXStore("")
		err := s.refresh(context.Background(), func(context.Context) (*ZTAPIDisplayFXQuote, error) { return &quote, nil }, now)
		require.Error(t, err)
		require.False(t, s.snapshot(now).Available)
	}
}

func TestZTAPIDisplayFXParsesECBStandardReference(t *testing.T) {
	fixture := `<?xml version="1.0"?><Envelope><Cube><Cube time="2026-11-06"><Cube currency="USD" rate="1.1165"/><Cube currency="CNY" rate="7.4485"/></Cube></Cube></Envelope>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(fixture)) }))
	defer server.Close()
	quote, err := fetchZTAPIDisplayFX(context.Background(), server.Client(), server.URL, time.Date(2026, 11, 8, 8, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, "6.67129422", quote.CNYPerUnit)
	require.Equal(t, "2026-11-06", quote.RateDate)
}
