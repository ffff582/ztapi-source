// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package telegram

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/support"
)

func TestLiveContentChecksRejectWrongCodexURLAndMissingVisualCode(t *testing.T) {
	if experienceContentChecks("configuration_help", "请使用 https://api.openai.com/v1")["correct_base_url"] {
		t.Fatal("wrong provider URL accepted")
	}
	if !experienceContentChecks("configuration_help", "base_url = https://ztapi.vip/v1")["correct_base_url"] {
		t.Fatal("approved URL rejected")
	}
	if experienceContentChecks("vision_503", "我无法查看图片")["visual_code_503"] {
		t.Fatal("unread image passed vision")
	}
	if !experienceContentChecks("vision_503", "截图显示HTTP 503")["visual_code_503"] {
		t.Fatal("visible code rejected")
	}
}

func TestExperienceAcceptanceReportHasFiveDistinctActorsAndImmutableReceipts(t *testing.T) {
	p := &fakeProvider{}
	var report experienceReport
	runExperienceSamples(t, p, testImage(t, "png"), 8, "deepseek-v4.1-flash", "xcloud", &report)
	if len(report.Cases) != 5 || len(report.Usages) != 5 || p.calls != 5 {
		t.Fatalf("acceptance sample count: cases=%d calls=%d", len(report.Cases), p.calls)
	}
	actors := map[int64]bool{}
	for _, s := range report.Cases {
		if actors[s.ActorID] || s.FinancialUsage == nil || !s.FinancialUsage.Dispatched || s.FinancialUsage.CallID == "" || s.Attempts != 1 {
			t.Fatalf("incomplete immutable sample: %+v", s)
		}
		actors[s.ActorID] = true
	}
	if p.inputs[4].Image == nil {
		t.Fatal("vision did not use current screenshot")
	}
	path := filepath.Join(t.TempDir(), "exclusive.json")
	f, err := openExperienceOutput(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeExperienceReport(f, report); err != nil {
		t.Fatal(err)
	}
	if _, err := openExperienceOutput(path); err == nil {
		t.Fatal("acceptance output overwritten")
	}
	raw, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(raw), `"usages"`) || strings.Contains(string(raw), "api_key") {
		t.Fatal("output missing facts or leaking configuration")
	}
}

func TestExperienceAcceptanceProviderFailureNeverRetriesAndKeepsUnknownReceipts(t *testing.T) {
	p := &fakeProvider{fn: func(context.Context, support.ProviderInput) (support.ProviderResult, error) {
		return support.ProviderResult{Outcome: "upstream_transport"}, errors.New("transport unknown")
	}}
	var report experienceReport
	runExperienceSamples(t, p, testImage(t, "png"), 8, "deepseek-v4.1-flash", "xcloud", &report)
	if p.calls != 5 {
		t.Fatal("provider attempts exceeded sample bound")
	}
	for _, s := range report.Cases {
		if s.FinancialUsage == nil || s.FinancialUsage.InputTokens != nil || s.FinancialUsage.ActualCostU != nil || s.FinancialUsage.Outcome != "upstream_transport" || s.Attempts != 1 {
			t.Fatalf("unknown evidence lost: %+v", s)
		}
	}
}

func TestExperienceLiveInputsRejectRelativeSymlinkPermissiveAndIncompletePaths(t *testing.T) {
	if _, err := readExperienceProvider("relative.json"); err == nil {
		t.Fatal("relative config accepted")
	}
	if _, err := openExperienceOutput("relative.json"); err == nil {
		t.Fatal("relative output accepted")
	}
	path := filepath.Join(t.TempDir(), "provider.json")
	if err := os.WriteFile(path, []byte(`{"api_key":"must-not-appear-in-error"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readExperienceProvider(path); err == nil || strings.Contains(err.Error(), "must-not-appear-in-error") {
		t.Fatal("invalid provider config accepted or echoed")
	}
}

func TestExperienceProtectedProviderFixtureAndSymlinkBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "provider.json")
	cfg := support.ProviderConfig{Endpoint: "https://www.xcloudtoken.com/v1/chat/completions", APIKey: "offline-live-fixture-not-real", Model: "deepseek-v4.1-flash", ChannelID: 8, Supplier: "xcloud", Group: "fixture", GroupMultiplier: "0.6", CashPerCredit: "0.5", InputCreditPerMillion: "0.14", OutputCreditPerMillion: "0.5", CacheReadCreditPerMillion: "0.003", QuoteEvidence: "offline-quote-fixture", VisionEnabled: true}
	raw, err := common.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = readExperienceProvider(path); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err = os.Chmod(path, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err = readExperienceProvider(path); err == nil {
			t.Fatal("world-readable protected config accepted")
		}
		if err = os.Chmod(path, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("symlink", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "provider-link.json")
		if err := os.Symlink(path, link); err != nil {
			t.Skip("symlink privilege unavailable")
		}
		if _, err := readExperienceProvider(link); err == nil {
			t.Fatal("symlink config accepted")
		}
	})
}

func TestExperienceAcceptanceUsesWallClockForAdmissionAndCompletion(t *testing.T) {
	before := time.Now().UTC()
	p := &fakeProvider{fn: func(context.Context, support.ProviderInput) (support.ProviderResult, error) {
		time.Sleep(2 * time.Millisecond)
		return support.ProviderResult{Content: "请先确认客户端名称。", Outcome: "completed"}, nil
	}}
	var report experienceReport
	runExperienceSamples(t, p, testImage(t, "png"), 8, "deepseek-v4.1-flash", "xcloud", &report)
	after := time.Now().UTC()
	if len(report.Usages) != 5 {
		t.Fatal("missing acceptance usages")
	}
	for _, usage := range report.Usages {
		if usage.StartedAt.Before(before) || usage.StartedAt.After(after) || usage.CompletedAt == nil || !usage.CompletedAt.After(usage.StartedAt) || usage.CompletedAt.After(after) || usage.Environment != "telegram-acceptance-v2" {
			t.Fatalf("acceptance timestamps were fixed/future or environment drifted: %+v", usage)
		}
	}
	if !strings.Contains(promptText(t, p.inputs[1]), "https://ztapi.vip/v1") || !strings.Contains(promptText(t, p.inputs[1]), "/docs/codex") {
		t.Fatal("approved static configuration knowledge absent")
	}
}
