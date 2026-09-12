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

package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

type adminModelPublicationSetting struct {
	PublishedCount int64  `json:"published_count"`
	ManagePath     string `json:"manage_path"`
	ReadOnly       bool   `json:"read_only"`
}

type adminPaymentSetting struct {
	Provider     string `json:"provider"`
	Configured   bool   `json:"configured"`
	MinimumTopUp int    `json:"minimum_topup"`
}

type adminSettingsResponse struct {
	BrandName           string                       `json:"brand_name"`
	Announcement        string                       `json:"announcement"`
	RegistrationEnabled bool                         `json:"registration_enabled"`
	ModelPublication    adminModelPublicationSetting `json:"model_publication"`
	Payments            []adminPaymentSetting        `json:"payments"`
	USDTTRC20           adminUSDTTopUpStatus         `json:"usdt_trc20"`
}

type adminUSDTTopUpStatus struct {
	Configured            bool      `json:"configured"`
	Enabled               bool      `json:"enabled"`
	PaymentCompliance     bool      `json:"payment_compliance_confirmed"`
	PendingOrders         int64     `json:"pending_orders"`
	ConfirmingOrders      int64     `json:"confirming_orders"`
	WatcherActive         bool      `json:"watcher_active"`
	WatcherStandby        bool      `json:"watcher_standby"`
	WatcherHealthy        bool      `json:"watcher_healthy"`
	LastSuccessfulQueryAt time.Time `json:"last_successful_query_at"`
	ConsecutiveFailures   int       `json:"consecutive_failures"`
	LastErrorCategory     string    `json:"last_error_category"`
	SettledTotal          int64     `json:"settled_total"`
	ExpiredTotal          int64     `json:"expired_total"`
	ManualReviewTotal     int64     `json:"manual_review_total"`
	UnmatchedTotal        int64     `json:"unmatched_total"`
}

type adminSettingMutationRequest struct {
	Value         json.RawMessage `json:"value"`
	ExpectedValue json.RawMessage `json:"expected_value"`
	Reason        string          `json:"reason"`
}

type adminSettingDefinition struct {
	OptionKey string
	Kind      string
	Min       int64
	Max       int64
}

var adminSettingDefinitions = map[string]adminSettingDefinition{
	"brand_name":                  {OptionKey: "SystemName", Kind: "string", Min: 1, Max: 100},
	"announcement":                {OptionKey: "Notice", Kind: "string", Min: 0, Max: 5000},
	"registration_enabled":        {OptionKey: "RegisterEnabled", Kind: "bool"},
	"minimum_topup":               {OptionKey: "MinTopUp", Kind: "int", Min: 1, Max: 1000000},
	"stripe_minimum_topup":        {OptionKey: "StripeMinTopUp", Kind: "int", Min: 1, Max: 1000000},
	"waffo_minimum_topup":         {OptionKey: "WaffoMinTopUp", Kind: "int", Min: 1, Max: 1000000},
	"waffo_pancake_minimum_topup": {OptionKey: "WaffoPancakeMinTopUp", Kind: "int", Min: 1, Max: 1000000},
}

func GetAdminSettings(c *gin.Context) {
	writeAdminSettings(c)
}

func UpdateAdminSetting(c *gin.Context) {
	publicKey := c.Param("key")
	definition, ok := adminSettingDefinitions[publicKey]
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "unsupported setting"})
		return
	}
	var request adminSettingMutationRequest
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || len(bytes.TrimSpace(request.Value)) == 0 || len(bytes.TrimSpace(request.ExpectedValue)) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "value and expected_value are required"})
		return
	}
	request.Reason = strings.TrimSpace(request.Reason)
	if request.Reason == "" || len(request.Reason) > 500 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "reason is required and must not exceed 500 bytes"})
		return
	}
	nextValue, err := normalizeAdminSettingValue(definition, request.Value)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	expectedValue, err := normalizeAdminSettingValue(definition, request.ExpectedValue)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid expected_value"})
		return
	}
	currentValue := currentAdminOptionValue(definition.OptionKey)
	if err := model.UpdateOptionIfMatches(definition.OptionKey, expectedValue, nextValue, currentValue); err != nil {
		if errors.Is(err, model.ErrOptionValueConflict) {
			c.JSON(http.StatusConflict, gin.H{"success": false, "message": "setting value changed"})
			return
		}
		if errors.Is(err, model.ErrOptionRuntimeSyncPending) {
			recordManageAudit(c, "settings.update", map[string]interface{}{
				"key": publicKey, "from_value": expectedValue, "to_value": nextValue,
				"reason": request.Reason, "result": "committed_runtime_sync_pending",
			})
			c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"key": publicKey, "value": nextValue}, "warning": "committed_runtime_sync_pending"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "unable to update setting"})
		return
	}
	recordManageAudit(c, "settings.update", map[string]interface{}{
		"key":        publicKey,
		"from_value": expectedValue,
		"to_value":   nextValue,
		"reason":     request.Reason,
		"result":     "committed",
	})
	writeAdminSettings(c)
}

func writeAdminSettings(c *gin.Context) {
	var publishedCount int64
	if err := model.DB.Model(&model.ZTAPIModelConfig{}).Where("published = ?", true).Count(&publishedCount).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "unable to load settings"})
		return
	}
	response := adminSettingsResponse{
		BrandName:           currentAdminOptionValue("SystemName"),
		Announcement:        currentAdminOptionValue("Notice"),
		RegistrationEnabled: currentAdminOptionValue("RegisterEnabled") == "true",
		ModelPublication: adminModelPublicationSetting{
			PublishedCount: publishedCount,
			ManagePath:     "/models",
			ReadOnly:       true,
		},
		Payments:  configuredAdminPayments(),
		USDTTRC20: currentAdminUSDTTopUpStatus(),
	}
	common.ApiSuccess(c, response)
}

func currentAdminUSDTTopUpStatus() adminUSDTTopUpStatus {
	config, err := setting.LoadUSDTTopUpConfig()
	snapshot := service.CurrentUSDTWatcherSnapshot()
	compliance := operation_setting.IsPaymentComplianceConfirmed()
	configured := err == nil && config.Enabled
	return adminUSDTTopUpStatus{
		Configured: configured, Enabled: configured && compliance, PaymentCompliance: compliance,
		PendingOrders: snapshot.PendingOrders, ConfirmingOrders: snapshot.ConfirmingOrders, WatcherActive: snapshot.Active,
		WatcherStandby: snapshot.Standby, WatcherHealthy: snapshot.Healthy,
		LastSuccessfulQueryAt: snapshot.LastSuccessfulQueryAt,
		ConsecutiveFailures:   snapshot.ConsecutiveFailures, LastErrorCategory: snapshot.LastErrorCategory,
		SettledTotal: snapshot.SettledTotal, ExpiredTotal: snapshot.ExpiredTotal,
		ManualReviewTotal: snapshot.ManualReviewTotal, UnmatchedTotal: snapshot.UnmatchedTotal,
	}
}

func currentAdminOptionValue(key string) string {
	common.OptionMapRWMutex.RLock()
	value, ok := common.OptionMap[key]
	common.OptionMapRWMutex.RUnlock()
	if ok {
		return common.Interface2String(value)
	}
	switch key {
	case "SystemName":
		return common.SystemName
	case "RegisterEnabled":
		return strconv.FormatBool(common.RegisterEnabled)
	case "MinTopUp":
		return strconv.Itoa(operation_setting.MinTopUp)
	case "StripeMinTopUp":
		return strconv.Itoa(setting.StripeMinTopUp)
	case "WaffoMinTopUp":
		return strconv.Itoa(setting.WaffoMinTopUp)
	case "WaffoPancakeMinTopUp":
		return strconv.Itoa(setting.WaffoPancakeMinTopUp)
	default:
		return ""
	}
}

func normalizeAdminSettingValue(definition adminSettingDefinition, raw json.RawMessage) (string, error) {
	switch definition.Kind {
	case "string":
		var value string
		if err := json.Unmarshal(raw, &value); err != nil || int64(len(value)) < definition.Min || int64(len(value)) > definition.Max {
			return "", errors.New("invalid setting value")
		}
		return value, nil
	case "bool":
		var value bool
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", errors.New("invalid setting value")
		}
		return strconv.FormatBool(value), nil
	case "int":
		var value int64
		if err := json.Unmarshal(raw, &value); err != nil || value < definition.Min || value > definition.Max {
			return "", errors.New("invalid setting value")
		}
		return strconv.FormatInt(value, 10), nil
	default:
		return "", errors.New("unsupported setting")
	}
}

func configuredAdminPayments() []adminPaymentSetting {
	payments := make([]adminPaymentSetting, 0, 5)
	if isEpayWebhookConfigured() && len(operation_setting.PayMethods) > 0 {
		payments = append(payments, adminPaymentSetting{Provider: "epay", Configured: true, MinimumTopUp: operation_setting.MinTopUp})
	}
	if strings.TrimSpace(setting.StripeApiSecret) != "" && strings.TrimSpace(setting.StripeWebhookSecret) != "" && strings.TrimSpace(setting.StripePriceId) != "" {
		payments = append(payments, adminPaymentSetting{Provider: "stripe", Configured: true, MinimumTopUp: setting.StripeMinTopUp})
	}
	if strings.TrimSpace(setting.WaffoApiKey) != "" && strings.TrimSpace(setting.WaffoPrivateKey) != "" && strings.TrimSpace(setting.WaffoPublicCert) != "" {
		payments = append(payments, adminPaymentSetting{Provider: "waffo", Configured: true, MinimumTopUp: setting.WaffoMinTopUp})
	}
	if strings.TrimSpace(setting.WaffoPancakeMerchantID) != "" && strings.TrimSpace(setting.WaffoPancakePrivateKey) != "" && strings.TrimSpace(setting.WaffoPancakeProductID) != "" {
		payments = append(payments, adminPaymentSetting{Provider: "waffo_pancake", Configured: true, MinimumTopUp: setting.WaffoPancakeMinTopUp})
	}
	if config, err := setting.LoadUSDTTopUpConfig(); err == nil && config.Enabled && operation_setting.IsPaymentComplianceConfirmed() {
		payments = append(payments, adminPaymentSetting{Provider: model.PaymentProviderUSDTTRC20, Configured: true, MinimumTopUp: int(config.MinTopUp)})
	}
	return payments
}
