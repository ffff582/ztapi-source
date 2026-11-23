/*
Copyright (C) 2025 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/

package controller_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

func TestAdminSettingsExposeOnlySupportedSafeContractAndUseExpectedValueCAS(t *testing.T) {
	db, engine := setupAdminOperationsTest(t)
	root, rootToken := createAdminOperationsUser(t, db, "settings-root", common.RoleRootUser, 0)
	admin, adminToken := createAdminOperationsUser(t, db, "settings-admin", common.RoleAdminUser, 0)

	originalSystemName, originalRegisterEnabled := common.SystemName, common.RegisterEnabled
	originalOptionMap := common.OptionMap
	originalPayAddress, originalEpayID, originalEpayKey := operation_setting.PayAddress, operation_setting.EpayId, operation_setting.EpayKey
	originalPayMethods, originalMinTopUp := operation_setting.PayMethods, operation_setting.MinTopUp
	originalStripeSecret, originalStripeWebhook, originalStripePrice, originalStripeMin := setting.StripeApiSecret, setting.StripeWebhookSecret, setting.StripePriceId, setting.StripeMinTopUp
	t.Cleanup(func() {
		common.SystemName, common.RegisterEnabled = originalSystemName, originalRegisterEnabled
		common.OptionMap = originalOptionMap
		operation_setting.PayAddress, operation_setting.EpayId, operation_setting.EpayKey = originalPayAddress, originalEpayID, originalEpayKey
		operation_setting.PayMethods, operation_setting.MinTopUp = originalPayMethods, originalMinTopUp
		setting.StripeApiSecret, setting.StripeWebhookSecret, setting.StripePriceId, setting.StripeMinTopUp = originalStripeSecret, originalStripeWebhook, originalStripePrice, originalStripeMin
	})

	common.SystemName = "ZTAPI"
	common.RegisterEnabled = true
	common.OptionMap = map[string]string{
		"SystemName":      "ZTAPI",
		"Notice":          "Scheduled maintenance",
		"RegisterEnabled": "true",
		"EpayKey":         "EPAY_SECRET_VALUE",
		"SMTPToken":       "SMTP_SECRET_VALUE",
		"UpstreamSecret":  "UPSTREAM_SECRET_VALUE",
	}
	operation_setting.PayAddress = "https://payments.example.test"
	operation_setting.EpayId = "merchant-id"
	operation_setting.EpayKey = "EPAY_SECRET_VALUE"
	operation_setting.PayMethods = []map[string]string{{"name": "Alipay", "type": "alipay"}}
	operation_setting.MinTopUp = 5
	setting.StripeApiSecret = "STRIPE_SECRET_VALUE"
	setting.StripeWebhookSecret = "STRIPE_WEBHOOK_SECRET"
	setting.StripePriceId = "price-configured"
	setting.StripeMinTopUp = 7

	publishedName := "zt-published"
	published := model.ZTAPIModelConfig{SourceModel: "gpt-source", PublicName: &publishedName, Family: model.ZTAPIModelFamilyOpenAI, Published: true, EnabledGroups: `["default"]`, Version: 1}
	if err := db.Create(&published).Error; err != nil {
		t.Fatalf("create published model: %v", err)
	}

	read := performAdminOperationsRequest(t, engine, http.MethodGet, "/api/admin/settings", root, rootToken, "", "settings-read")
	if read.Code != http.StatusOK {
		t.Fatalf("settings read status=%d body=%s", read.Code, read.Body.String())
	}
	for _, required := range []string{`"brand_name":"ZTAPI"`, `"announcement":"Scheduled maintenance"`, `"registration_enabled":true`, `"published_count":1`, `"manage_path":"/models"`, `"read_only":true`, `"provider":"epay"`, `"configured":true`, `"minimum_topup":5`, `"provider":"stripe"`, `"minimum_topup":7`} {
		if !strings.Contains(read.Body.String(), required) {
			t.Fatalf("settings response missing %q: %s", required, read.Body.String())
		}
	}
	assertBodyOmits(t, read.Body.String(), "EPAY_SECRET_VALUE", "SMTP_SECRET_VALUE", "UPSTREAM_SECRET_VALUE", "STRIPE_SECRET_VALUE", "STRIPE_WEBHOOK_SECRET", "merchant-id", "payments.example.test", "api_secret", "webhook_secret", "gateway")

	deniedRead := performAdminOperationsRequest(t, engine, http.MethodGet, "/api/admin/settings", admin, adminToken, "", "settings-denied-read")
	if deniedRead.Code != http.StatusOK || !strings.Contains(deniedRead.Body.String(), `"success":false`) {
		t.Fatalf("admin settings read status=%d body=%s", deniedRead.Code, deniedRead.Body.String())
	}

	updateBody := `{"value":"ZTAPI Enterprise","expected_value":"ZTAPI","reason":"approved brand rollout"}`
	updated := performAdminOperationsRequest(t, engine, http.MethodPatch, "/api/admin/settings/brand_name", root, rootToken, updateBody, "settings-update")
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"brand_name":"ZTAPI Enterprise"`) {
		t.Fatalf("settings update status=%d body=%s", updated.Code, updated.Body.String())
	}
	if common.SystemName != "ZTAPI Enterprise" {
		t.Fatalf("system name=%q", common.SystemName)
	}

	stale := performAdminOperationsRequest(t, engine, http.MethodPatch, "/api/admin/settings/brand_name", root, rootToken, updateBody, "settings-stale")
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale settings status=%d body=%s", stale.Code, stale.Body.String())
	}

	secretWrite := performAdminOperationsRequest(t, engine, http.MethodPatch, "/api/admin/settings/EpayKey", root, rootToken, `{"value":"NEW_SECRET","expected_value":"EPAY_SECRET_VALUE","reason":"must be rejected"}`, "settings-secret-write")
	if secretWrite.Code != http.StatusBadRequest || operation_setting.EpayKey != "EPAY_SECRET_VALUE" {
		t.Fatalf("secret write status=%d epay_key=%q body=%s", secretWrite.Code, operation_setting.EpayKey, secretWrite.Body.String())
	}

	var audit model.Log
	if err := db.Where("type = ? AND request_id = ?", model.LogTypeManage, "settings-update").First(&audit).Error; err != nil {
		t.Fatalf("load settings audit: %v", err)
	}
	for _, required := range []string{`"action":"settings.update"`, `"key":"brand_name"`, `"from_value":"ZTAPI"`, `"to_value":"ZTAPI Enterprise"`, `"reason":"approved brand rollout"`} {
		if !strings.Contains(audit.Other, required) {
			t.Fatalf("settings audit missing %q: %s", required, audit.Other)
		}
	}
	for _, forbidden := range []string{"EPAY_SECRET_VALUE", "STRIPE_SECRET_VALUE", "SMTP_SECRET_VALUE"} {
		if strings.Contains(audit.Other, forbidden) || strings.Contains(audit.Content, forbidden) {
			t.Fatalf("settings audit exposed %q: content=%s other=%s", forbidden, audit.Content, audit.Other)
		}
	}

	_ = fmt.Sprintf("%d", root.Id)
}
