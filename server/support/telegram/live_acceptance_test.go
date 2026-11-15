// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package telegram

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/support"
)

const experienceEnvironment = "telegram-acceptance-v2"

type experienceCase struct {
	Name           string                  `json:"name"`
	ActorID        int64                   `json:"actor_id"`
	Reply          string                  `json:"reply"`
	CallID         string                  `json:"call_id"`
	Attempts       int                     `json:"attempts"`
	Outcome        string                  `json:"outcome"`
	Checks         map[string]bool         `json:"checks"`
	FinancialUsage *support.FinancialUsage `json:"-"`
}
type experienceReport struct {
	SchemaVersion   int                      `json:"schema_version"`
	Environment     string                   `json:"environment"`
	CreatedAt       time.Time                `json:"created_at"`
	Cases           []experienceCase         `json:"cases"`
	Usages          []support.FinancialUsage `json:"usages"`
	CollectionError string                   `json:"collection_error,omitempty"`
}

// Only this opt-in test may use the real provider. Telegram and Bridge remain
// in-memory fakes; a failed attempt is never retried or replaced by a new call.
func TestLiveSupportExperienceAcceptance(t *testing.T) {
	configPath, outputPath := os.Getenv("SUPPORT_EXPERIENCE_LIVE_CONFIG"), os.Getenv("SUPPORT_EXPERIENCE_LIVE_OUTPUT")
	if configPath == "" && outputPath == "" {
		t.Skip("real model acceptance disabled; protected absolute config and exclusive output required")
	}
	if !filepath.IsAbs(configPath) || !filepath.IsAbs(outputPath) {
		t.Fatal("live config and output must both be absolute paths")
	}
	cfg, err := readExperienceProvider(configPath)
	if err != nil {
		t.Fatal(err)
	}
	imagePath := os.Getenv("SUPPORT_EXPERIENCE_LIVE_IMAGE")
	if !filepath.IsAbs(imagePath) {
		t.Fatal("live simulated-503 screenshot must be an absolute path")
	}
	imageFile, err := os.Open(imagePath)
	if err != nil {
		t.Fatal("live screenshot unavailable")
	}
	image, err := io.ReadAll(io.LimitReader(imageFile, support.MaxImageBytes+1))
	_ = imageFile.Close()
	if err != nil || len(image) > support.MaxImageBytes || support.ValidateImage(&support.ImageInput{MIMEType: http.DetectContentType(image), DataBase64: base64.StdEncoding.EncodeToString(image)}) != nil {
		t.Fatal("invalid live screenshot")
	}
	provider, err := support.NewHTTPProvider(cfg, nil)
	if err != nil {
		t.Fatal("protected live provider rejected")
	}
	output, err := openExperienceOutput(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	report := experienceReport{SchemaVersion: 1, Environment: experienceEnvironment, CreatedAt: time.Now().UTC(), Usages: []support.FinancialUsage{}}
	defer func() {
		if err := writeExperienceReport(output, report); err != nil {
			t.Error(err)
		}
	}()
	runExperienceSamples(t, provider, image, cfg.ChannelID, cfg.Model, cfg.Supplier, &report)
	for _, sample := range report.Cases {
		if sample.Attempts != 1 || sample.Outcome != "completed" || sample.Reply == busyReply || sample.Reply == seriousFallback {
			t.Errorf("sample %s did not complete cleanly; receipt exported without retry", sample.Name)
		}
		for name, ok := range sample.Checks {
			if !ok {
				t.Errorf("sample %s failed %s; no retry", sample.Name, name)
			}
		}
	}
}

func runExperienceSamples(t *testing.T, provider support.Provider, image []byte, channel int, model, supplier string, report *experienceReport) {
	t.Helper()
	h := newHarness(t)
	h.cfg.Environment = experienceEnvironment
	h.cfg.Now = func() time.Time { return time.Now().UTC() }
	h.cfg.ChannelID, h.cfg.Model, h.cfg.Supplier = channel, model, supplier
	h.cfg.VisionEnabled = true
	h.cfg.Bridge = &fakeBridge{}
	h.provider.fn = provider.Complete
	h.now = time.Now().UTC()
	h.restart()
	h.worker.knowledge = (&support.Reader{}).PublicKnowledge
	h.fileData = image
	start := h.now.Add(-time.Minute)
	report.SchemaVersion, report.Environment, report.CreatedAt = 1, experienceEnvironment, h.now
	report.Usages = []support.FinancialUsage{}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		usages, err := support.ReadFinancialUsage(ctx, h.db, start, time.Now().UTC().Add(time.Minute))
		if err != nil {
			report.CollectionError = "immutable support receipt export failed"
			t.Error(report.CollectionError)
			return
		}
		report.Usages = usages
		for i := range report.Cases {
			for j := range report.Usages {
				if report.Cases[i].CallID == report.Usages[j].CallID {
					report.Cases[i].FinancialUsage = &report.Usages[j]
					report.Cases[i].Outcome = report.Usages[j].Outcome
				}
			}
		}
	}()
	samples := []struct{ name, text string }{
		{"normal_meme", "小 Z，周一写代码像在等周五，这梗你接得住吗？"},
		{"configuration_help", "我用 Windows 上的 Codex，Base URL 应该怎么配置？"},
		{"serious_billing", "请解释公开计费规则，费用未知时应该如何核实？"},
		{"serious_fault", "接口报错 HTTP 503，应该先排查哪一步？"},
		{"vision_503", "请识别当前这张模拟 HTTP 503 故障截图，只依据画面说明下一步。"},
	}
	for i, sample := range samples {
		actor := int64(900001 + i)
		report.Cases = append(report.Cases, experienceCase{Name: sample.name, ActorID: actor, Outcome: "not_attempted"})
		current := &report.Cases[len(report.Cases)-1]
		h.now = time.Now().UTC()
		update := h.private(int64(i+1), actor, sample.text)
		if i == 4 {
			update.Message.Photo = []PhotoSize{{FileID: "fake-503-screenshot", FileSize: int64(len(image)), Width: 2, Height: 2}}
		}
		before := h.provider.calls
		h.updates = []Update{update}
		stepErr := h.worker.Step(context.Background())
		current.Attempts = h.provider.calls - before
		var row inbox
		if h.db.Where("update_id = ?", update.UpdateID).Take(&row).Error == nil {
			current.CallID = row.CallID
		}
		for _, sent := range h.sent {
			if sent.ChatID == actor {
				current.Reply = sent.Text
			}
		}
		current.Checks = map[string]bool{
			"one_attempt": current.Attempts == 1, "reply_present": current.Reply != "",
			"no_false_operation": !falseOperation.MatchString(current.Reply) && !unsupportedClaim.MatchString(current.Reply),
			"no_credential_echo": !h.worker.hasCredentials(current.Reply),
			"one_question":       strings.Count(current.Reply, "?")+strings.Count(current.Reply, "？") <= 1,
			"serious_tone":       !serious(sample.text) || !jokingOutput.MatchString(current.Reply),
		}
		for key, value := range experienceContentChecks(sample.name, current.Reply) {
			current.Checks[key] = value
		}
		if stepErr != nil {
			current.Outcome = "worker_error"
			return
		}
	}
}

func experienceContentChecks(name, reply string) map[string]bool {
	checks := map[string]bool{}
	switch name {
	case "configuration_help":
		checks["correct_base_url"] = strings.Contains(reply, "https://ztapi.vip/v1")
	case "vision_503":
		checks["visual_code_503"] = strings.Contains(reply, "503")
	case "serious_fault":
		checks["fault_code_503"] = strings.Contains(reply, "503")
	case "serious_billing":
		checks["evidence_not_guess"] = strings.Contains(reply, "核对") || strings.Contains(reply, "证据") || strings.Contains(reply, "未知") || strings.Contains(reply, "日志") || strings.Contains(reply, "人工")
	}
	return checks
}

func readExperienceProvider(path string) (support.ProviderConfig, error) {
	var cfg support.ProviderConfig
	if !filepath.IsAbs(path) {
		return cfg, errors.New("live provider config must be absolute and protected")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return cfg, errors.New("live provider config must be a protected regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return cfg, errors.New("live provider config unavailable")
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !os.SameFile(info, actual) {
		return cfg, errors.New("live provider config changed")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 16385))
	if err != nil || len(raw) > 16384 || common.RejectDuplicateJsonObjectMembers(bytes.NewReader(raw)) != nil || common.DecodeJsonStrict(bytes.NewReader(raw), &cfg) != nil || cfg.ChannelID != 8 || !cfg.VisionEnabled {
		return support.ProviderConfig{}, errors.New("invalid protected live provider configuration")
	}
	if _, err = support.NewHTTPProvider(cfg, nil); err != nil {
		return support.ProviderConfig{}, errors.New("unauthorized live provider configuration")
	}
	return cfg, nil
}

func openExperienceOutput(path string) (*os.File, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("live output must be absolute")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, errors.New("live output must not already exist")
	}
	return f, nil
}
func writeExperienceReport(f *os.File, report experienceReport) error {
	defer f.Close()
	raw, err := common.Marshal(report)
	if err != nil {
		return errors.New("live receipt output encoding failed")
	}
	if _, err = f.Write(append(raw, '\n')); err != nil {
		return errors.New("live receipt output write failed")
	}
	if err = f.Sync(); err != nil {
		return errors.New("live receipt output sync failed")
	}
	return nil
}
