package model

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestZTAPIQuotationABWarmReadAvoidsRepeatedParsing(t *testing.T) {
	_, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	decodeAllocations := testing.AllocsPerRun(10, func() {
		var quote ZTAPIABQuotationManifest
		if err := json.Unmarshal(ztapiQuotationABJSON, &quote); err != nil {
			panic(err)
		}
	})
	readAllocations := testing.AllocsPerRun(10, func() {
		if _, err := ZTAPIQuotationABEntries(); err != nil {
			panic(err)
		}
	})
	require.Less(t, readAllocations, decodeAllocations/2,
		"warm immutable quotation reads must avoid JSON parsing allocations")
}

func TestZTAPIQuotationABCachedReadsKeepIndependentNestedData(t *testing.T) {
	want, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	var raw ZTAPIABQuotationManifest
	require.NoError(t, json.Unmarshal(ztapiQuotationABJSON, &raw))
	require.Equal(t, raw, want)

	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			quote, readErr := ZTAPIQuotationABEntries()
			if readErr != nil {
				t.Error(readErr)
				return
			}
			quote.Entries[0].ModelName = "caller-local-change"
			for entryIndex := range quote.Entries {
				for ruleIndex := range quote.Entries[entryIndex].TokenPriceRules {
					rule := &quote.Entries[entryIndex].TokenPriceRules[ruleIndex]
					for _, values := range [][]string{rule.Conditions, rule.NotApplicable, rule.TemporaryFree} {
						if len(values) > 0 {
							values[0] = "caller-local-change"
						}
					}
					for key := range rule.Cost {
						rule.Cost[key] = "999"
					}
					for key := range rule.Sale {
						rule.Sale[key] = "999"
					}
				}
			}
		}()
	}
	wg.Wait()
	after, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	require.Equal(t, raw, want, "previously returned values must not share mutable cache data")
	require.Equal(t, raw, after, "caller mutation must not corrupt the cached template")
}

func BenchmarkZTAPIQuotationABWarmRead(b *testing.B) {
	if _, err := ZTAPIQuotationABEntries(); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := ZTAPIQuotationABEntries(); err != nil {
			b.Fatal(err)
		}
	}
}
