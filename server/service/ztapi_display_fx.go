package service

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
)

const ztapiDisplayFXURL = "https://www.ecb.europa.eu/stats/eurofxref/eurofxref-daily.xml"

type ZTAPIDisplayFXQuote struct {
	Available  bool   `json:"available"`
	CNYPerUnit string `json:"cny_per_unit"`
	RateDate   string `json:"rate_date"`
	FetchedAt  int64  `json:"fetched_at"`
	Source     string `json:"source"`
	SourceURL  string `json:"source_url"`
	Stale      bool   `json:"stale"`
}

type ztapiDisplayFXStore struct {
	mu    sync.RWMutex
	quote ZTAPIDisplayFXQuote
	path  string
}

func newZTAPIDisplayFXStore(path string) *ztapiDisplayFXStore {
	s := &ztapiDisplayFXStore{path: path}
	if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() && info.Size() <= 8192 {
		if raw, readErr := os.ReadFile(path); readErr == nil {
			var quote ZTAPIDisplayFXQuote
			if common.Unmarshal(raw, &quote) == nil && validateZTAPIDisplayFX(quote, time.Now().UTC()) == nil {
				s.quote = quote
			}
		}
	}
	return s
}

func validateZTAPIDisplayFX(q ZTAPIDisplayFXQuote, now time.Time) error {
	rate, err := decimal.NewFromString(q.CNYPerUnit)
	if err != nil || rate.LessThan(decimal.NewFromInt(1)) || rate.GreaterThan(decimal.NewFromInt(20)) {
		return errors.New("invalid display reference rate")
	}
	date, err := time.Parse("2006-01-02", q.RateDate)
	if err != nil || date.After(now.UTC()) || now.Sub(date) > 7*24*time.Hour || q.FetchedAt <= 0 || q.FetchedAt > now.Unix()+300 {
		return errors.New("invalid display reference date")
	}
	return nil
}

func (s *ztapiDisplayFXStore) snapshot(now time.Time) ZTAPIDisplayFXQuote {
	s.mu.RLock()
	q := s.quote
	s.mu.RUnlock()
	q.Available = validateZTAPIDisplayFX(q, now) == nil
	q.Stale = q.Available && now.Sub(time.Unix(q.FetchedAt, 0)) > 6*time.Hour
	if !q.Available {
		q.CNYPerUnit = ""
	}
	return q
}

func (s *ztapiDisplayFXStore) refresh(ctx context.Context, fetch func(context.Context) (*ZTAPIDisplayFXQuote, error), now time.Time) error {
	quote, err := fetch(ctx)
	if err != nil {
		return err
	}
	if quote == nil || validateZTAPIDisplayFX(*quote, now) != nil {
		return errors.New("unusable display reference")
	}
	quote.Available, quote.Source, quote.SourceURL = true, "ecb_usd_cny_reference", ztapiDisplayFXURL
	s.mu.Lock()
	defer s.mu.Unlock()
	if quote.RateDate < s.quote.RateDate || quote.FetchedAt < s.quote.FetchedAt {
		return errors.New("display reference regressed")
	}
	if s.path != "" {
		if info, statErr := os.Lstat(s.path); statErr == nil && !info.Mode().IsRegular() {
			return errors.New("unsafe display cache file")
		}
		raw, marshalErr := common.Marshal(quote)
		if marshalErr != nil {
			return marshalErr
		}
		file, createErr := os.CreateTemp(filepath.Dir(s.path), ".display-fx-*")
		if createErr != nil {
			return createErr
		}
		defer os.Remove(file.Name())
		_, writeErr := file.Write(raw)
		if writeErr == nil {
			writeErr = file.Sync()
		}
		closeErr := file.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
		if err := os.Rename(file.Name(), s.path); err != nil {
			return err
		}
	}
	s.quote = *quote
	return nil
}

func fetchZTAPIDisplayFX(ctx context.Context, client *http.Client, url string, now time.Time) (*ZTAPIDisplayFXQuote, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, errors.New("display reference unavailable")
	}
	var body struct {
		Cube struct {
			Date struct {
				Time  string `xml:"time,attr"`
				Rates []struct {
					Currency string `xml:"currency,attr"`
					Rate     string `xml:"rate,attr"`
				} `xml:"Cube"`
			} `xml:"Cube"`
		} `xml:"Cube"`
	}
	if err := xml.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&body); err != nil {
		return nil, errors.New("invalid display reference response")
	}
	usd, cny := decimal.Zero, decimal.Zero
	for _, row := range body.Cube.Date.Rates {
		if row.Currency == "USD" {
			usd, err = decimal.NewFromString(row.Rate)
		} else if row.Currency == "CNY" {
			cny, err = decimal.NewFromString(row.Rate)
		} else {
			continue
		}
		if err != nil {
			return nil, errors.New("invalid display reference currency")
		}
	}
	if !usd.IsPositive() || !cny.IsPositive() {
		return nil, errors.New("missing display reference currency")
	}
	quote := &ZTAPIDisplayFXQuote{CNYPerUnit: cny.DivRound(usd, 8).StringFixed(8), RateDate: body.Cube.Date.Time, FetchedAt: now.Unix()}
	return quote, validateZTAPIDisplayFX(*quote, now)
}

var ztapiDisplayFX = newZTAPIDisplayFXStore("/data/ztapi-display-fx.json")
var ztapiDisplayFXOnce sync.Once

func CurrentZTAPIDisplayFX() ZTAPIDisplayFXQuote { return ztapiDisplayFX.snapshot(time.Now().UTC()) }

// Display references never call the billing FX policy, repricer or database.
func StartZTAPIDisplayFXRefreshTask(ctx context.Context) {
	ztapiDisplayFXOnce.Do(func() {
		go func() {
			client := &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			ticker := time.NewTicker(time.Hour)
			defer ticker.Stop()
			for {
				now := time.Now().UTC()
				if err := ztapiDisplayFX.refresh(ctx, func(ctx context.Context) (*ZTAPIDisplayFXQuote, error) {
					return fetchZTAPIDisplayFX(ctx, client, ztapiDisplayFXURL, now)
				}, now); err != nil {
					common.SysLog("ZTAPI display FX refresh unavailable; retaining the dated reference")
				}
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	})
}
