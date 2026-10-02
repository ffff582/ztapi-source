package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

const ztapiFXInitialRepricingOptionKey = "ZTAPIFXInitialRepricingAt"

// Prices must clear cost by at least this factor, so an FX or quotation
// mistake cannot publish a model that sells below its upstream bill.
var ztapiMinimumMarginMultiplier = decimal.RequireFromString("1.05")

func ztapiPreviewKeepsMinimumMargin(preview *ZTAPIModelPricePreview) error {
	if preview == nil {
		return errors.New("price preview is missing")
	}
	for dimension, rawCost := range preview.CostUSD {
		cost, err := decimal.NewFromString(strings.TrimSpace(rawCost))
		if err != nil {
			return fmt.Errorf("cost for %s is invalid", dimension)
		}
		sale, err := decimal.NewFromString(strings.TrimSpace(preview.SaleUSD[dimension]))
		if err != nil {
			return fmt.Errorf("sale price for %s is invalid", dimension)
		}
		if cost.IsPositive() && sale.LessThan(cost.Mul(ztapiMinimumMarginMultiplier)) {
			return fmt.Errorf("sale price for %s is below cost plus the minimum margin", dimension)
		}
	}
	return nil
}

func ztapiFirstTierSale(source *ZTAPIModelPriceSource) (string, string) {
	if source == nil {
		return "", ""
	}
	if strings.TrimSpace(source.TokenPriceRulesJSON) != "" {
		var rules []ztapiABFrozenSaleRule
		if err := json.Unmarshal([]byte(source.TokenPriceRulesJSON), &rules); err == nil && len(rules) > 0 {
			return rules[0].Sale[ZTAPIBillingDimensionInputTokens], rules[0].Sale[ZTAPIBillingDimensionOutputTokens]
		}
		return "", ""
	}
	preview, err := BuildZTAPIModelPricePreview(source)
	if err != nil {
		return "", ""
	}
	return preview.SaleUSD[ZTAPIBillingDimensionInputTokens], preview.SaleUSD[ZTAPIBillingDimensionOutputTokens]
}

func ztapiSameDecimal(left, right string) bool {
	a, aErr := decimal.NewFromString(strings.TrimSpace(left))
	b, bErr := decimal.NewFromString(strings.TrimSpace(right))
	if aErr != nil || bErr != nil {
		return strings.TrimSpace(left) == strings.TrimSpace(right)
	}
	return a.Equal(b)
}

// ztapiPublishedABTextModelNames lists the published models the A/B workbook
// can price as text. Media quotes and quotes with unsupported conditions stay
// on their existing price source.
func ztapiPublishedABTextModelNames(db *gorm.DB, quote ZTAPIABQuotationManifest) ([]string, error) {
	frozen, err := ZTAPIQuotationEntries()
	if err != nil {
		return nil, err
	}
	bridge, err := BuildZTAPIABQuotationIdentityBridge(quote, frozen)
	if err != nil {
		return nil, err
	}
	var sourceModels []string
	if err := db.Model(&ZTAPIModelConfig{}).Where("published = ?", true).
		Pluck("source_model", &sourceModels).Error; err != nil {
		return nil, err
	}
	published := make(map[string]bool, len(sourceModels))
	for _, sourceModel := range sourceModels {
		published[sourceModel] = true
	}
	names := make([]string, 0, len(bridge.Mapped))
	for _, identity := range bridge.Mapped {
		if identity.ModelName == ztapiImage2QuotationModel || !published[identity.SourceModel] {
			continue
		}
		if _, err := BuildZTAPIABTextPriceSource(quote, identity.ModelName, 1, 1, ztapiFXPreviewEffectiveAt); err != nil {
			continue
		}
		names = append(names, identity.ModelName)
	}
	sort.Strings(names)
	return names, nil
}

const (
	ztapiImage2QuotationModel  = "GPT Image 2"
	ztapiFXPreviewEffectiveAt  = int64(1_789_000_000)
	ztapiFXRepricingAuditEvent = "model.fx_platform_rate_repriced"
)

// ApplyZTAPIFXRepricing re-prices every published A/B text model and every
// legacy CNY-quoted model under the FX policy in force, in one catalog write.
// USD quotes are skipped while the upstream settlement rate is unset.
func ApplyZTAPIFXRepricing(operatorID int) (ZTAPICommercialRepricingResult, error) {
	result := ZTAPICommercialRepricingResult{}
	if operatorID <= 0 || DB == nil {
		return result, errors.New("FX repricing requires an operator and database")
	}
	quote, err := ZTAPIQuotationABEntries()
	if err != nil {
		return result, err
	}
	err = withZTAPICatalogWrite(func(tx *gorm.DB) error {
		fxParams, fxErr := currentZTAPIABFXParams(tx)
		if fxErr != nil {
			return fxErr
		}
		if fxParams.Mode != ZTAPIFXModePlatformV1 {
			return errors.New("no FX policy is in force")
		}
		names, nameErr := ztapiPublishedABTextModelNames(tx, quote)
		if nameErr != nil {
			return nameErr
		}
		if len(names) == 0 {
			return nil
		}
		return applyZTAPIABCommercialPricingTx(tx, quote, operatorID, names, fxParams, &result)
	})
	if err != nil {
		return ZTAPICommercialRepricingResult{}, err
	}
	// Models still priced from the first quotation are repriced separately, so
	// a problem there cannot roll back the quoted catalog.
	legacyErr := withZTAPICatalogWrite(func(tx *gorm.DB) error {
		fxParams, fxErr := currentZTAPIABFXParams(tx)
		if fxErr != nil {
			return fxErr
		}
		return applyZTAPILegacyCNYFXRepricingTx(tx, operatorID, fxParams, quote.WorkbookSHA256, &result)
	})
	if legacyErr != nil {
		common.SysError("ZTAPI legacy CNY repricing failed: " + legacyErr.Error())
	}
	invalidateZTAPICatalogCaches()
	return result, nil
}

// applyZTAPILegacyCNYFXRepricingTx moves models still priced from the first
// quotation (DeepSeek today) onto the platform rate, keeping their CNY costs.
func applyZTAPILegacyCNYFXRepricingTx(tx *gorm.DB, operatorID int, fxParams ZTAPIABFXParams, abChecksum string, result *ZTAPICommercialRepricingResult) error {
	if fxParams.Mode != ZTAPIFXModePlatformV1 {
		return nil
	}
	var configs []ZTAPIModelConfig
	if err := tx.Where("published = ?", true).Order("source_model ASC").Find(&configs).Error; err != nil {
		return err
	}
	publications, err := loadZTAPIQuotedPublications(tx)
	if err != nil {
		return err
	}
	bound := make(map[int]ZTAPIRuntimePublication, len(publications))
	for _, publication := range publications {
		bound[publication.ModelConfigID] = publication
	}
	platform := fxParams.PlatformRate.StringFixed(10)
	for i := range configs {
		config := configs[i]
		publication, ok := bound[config.ID]
		if !ok || publication.SnapshotID != config.PublicationSnapshotID || publication.Version != config.Version {
			continue
		}
		var oldSource ZTAPIModelPriceSource
		if err := tx.First(&oldSource, publication.PriceSourceID).Error; err != nil {
			return err
		}
		if oldSource.SourceDocumentChecksum == abChecksum || oldSource.Currency != "CNY" ||
			strings.TrimSpace(oldSource.MediaPriceContractJSON) != "" ||
			strings.TrimSpace(oldSource.TokenPriceRulesJSON) != "" {
			continue
		}
		if oldSource.FXMode == ZTAPIFXModePlatformV1 && ztapiSameDecimal(oldSource.PlatformCNYPerUnit, platform) {
			result.Unchanged++
			continue
		}
		var previous ZTAPIModelPublicationSnapshot
		if err := tx.First(&previous, publication.SnapshotID).Error; err != nil {
			return err
		}
		if len(previous.ChannelIDs()) == 0 {
			continue
		}
		target := oldSource
		target.ID, target.Version, target.CreatedAt = 0, 0, 0
		target.FXMode = ZTAPIFXModePlatformV1
		target.PlatformCNYPerUnit = platform
		target.CNYPerUSD = platform
		target.UpstreamCNYPerUSD = "0"
		target.OperatorID = operatorID
		preview, err := BuildZTAPIModelPricePreview(&target)
		if err != nil {
			return fmt.Errorf("%s price preview: %w", config.SourceModel, err)
		}
		if err := ztapiPreviewKeepsMinimumMargin(preview); err != nil {
			return fmt.Errorf("%s: %w", config.SourceModel, err)
		}
		if err := republishZTAPIPriceSourceTx(tx, &config, previous, &oldSource, &target, preview, operatorID); err != nil {
			return err
		}
		result.Imported++
		result.Republished++
		result.Models = append(result.Models, config.PublicNameValue())
	}
	return nil
}

// republishZTAPIPriceSourceTx binds a rebuilt price source to a fresh
// publication snapshot, preserving the routes and verifications already proven.
func republishZTAPIPriceSourceTx(tx *gorm.DB, config *ZTAPIModelConfig, previous ZTAPIModelPublicationSnapshot,
	oldSource, target *ZTAPIModelPriceSource, preview *ZTAPIModelPricePreview, operatorID int) error {
	var latest uint64
	if err := tx.Model(&ZTAPIModelPriceSource{}).Where("model_config_id = ?", config.ID).
		Select("COALESCE(MAX(version), 0)").Scan(&latest).Error; err != nil {
		return err
	}
	now := common.GetTimestamp()
	target.Version = latest + 1
	target.CreatedAt = now
	if err := tx.Create(target).Error; err != nil {
		return err
	}
	config.CacheReadRatio = previous.CacheReadRatio
	config.CacheCreationRatio = previous.CacheCreationRatio
	config.CacheCreation5mRatio = previous.CacheCreation5mRatio
	config.CacheCreation1hRatio = previous.CacheCreation1hRatio
	config.ImageRatio = previous.ImageRatio
	config.AudioRatio = previous.AudioRatio
	config.AudioCompletionRatio = previous.AudioCompletionRatio
	if err := applyZTAPICommercialPreview(config, preview); err != nil {
		return err
	}
	var verificationIDs []int64
	if err := json.Unmarshal([]byte(previous.VerificationIDs), &verificationIDs); err != nil {
		return fmt.Errorf("%s has invalid frozen verification evidence", config.SourceModel)
	}
	evidence := ztapiPublicationEvidence{
		AllowedChannelIDs: previous.ChannelIDs(), VerificationIDs: verificationIDs,
		PriceSourceID: target.ID, IdentityUpdatedAt: previous.IdentityUpdatedAt,
		ImageProtocolContractJSON: previous.ImageProtocolContractJSON,
		VideoProtocolContractJSON: previous.VideoProtocolContractJSON,
	}
	nextVersion := config.Version + 1
	snapshot, err := createZTAPIModelPublicationSnapshotTx(tx, config, nextVersion, evidence)
	if err != nil {
		return fmt.Errorf("%s snapshot: %w", config.SourceModel, err)
	}
	updates := map[string]any{
		"input_cost_per_million": config.InputCostPerMillion, "output_cost_per_million": config.OutputCostPerMillion,
		"input_price_per_million": config.InputPricePerMillion, "output_price_per_million": config.OutputPricePerMillion,
		"cache_read_ratio": config.CacheReadRatio, "cache_creation_ratio": config.CacheCreationRatio,
		"cache_creation_5m_ratio": config.CacheCreation5mRatio, "cache_creation_1h_ratio": config.CacheCreation1hRatio,
		"publication_snapshot_id": snapshot.ID, "version": nextVersion, "updated_at": now,
	}
	updated := tx.Model(&ZTAPIModelConfig{}).Where("id = ? AND version = ?", config.ID, config.Version).Updates(updates)
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrZTAPIModelVersionConflict
	}
	payload := common.MapToJsonStr(map[string]any{
		"previous_price_source_id": oldSource.ID, "price_source_id": target.ID,
		"fx_mode": target.FXMode, "platform_cny_per_unit": target.PlatformCNYPerUnit,
		"upstream_cny_per_usd": target.UpstreamCNYPerUSD, "price_policy": target.PricePolicy,
	})
	return tx.Create(&ZTAPIAuditEvent{
		Action: ztapiFXRepricingAuditEvent, ModelConfigID: config.ID,
		PublicName: config.PublicNameValue(), Version: nextVersion,
		OperatorID: operatorID, Payload: payload, CreatedAt: now,
	}).Error
}

type ZTAPIFXPricingRow struct {
	PublicName        string `json:"public_name"`
	SourceModel       string `json:"source_model"`
	QuotationModel    string `json:"quotation_model,omitempty"`
	Kind              string `json:"kind"`
	QuotationGrade    string `json:"quotation_grade,omitempty"`
	QuoteCurrency     string `json:"quote_currency,omitempty"`
	CostCNYInput      string `json:"cost_cny_input,omitempty"`
	CurrentInputSale  string `json:"current_input_sale,omitempty"`
	CurrentOutputSale string `json:"current_output_sale,omitempty"`
	CurrentFXMode     string `json:"current_fx_mode,omitempty"`
	CurrentPlatform   string `json:"current_platform_cny_per_unit,omitempty"`
	ProposedInput     string `json:"proposed_input_sale,omitempty"`
	ProposedOutput    string `json:"proposed_output_sale,omitempty"`
	Status            string `json:"status"`
	Reason            string `json:"reason,omitempty"`
	NeedsRelist       bool   `json:"needs_relist,omitempty"`
}

type ZTAPIFXPricingPreview struct {
	Policy *ZTAPIFXPolicy      `json:"policy"`
	Rows   []ZTAPIFXPricingRow `json:"rows"`
}

// PreviewZTAPIFXRepricing shows, per published model, what the FX policy in
// force would charge compared with the price customers pay today.
func PreviewZTAPIFXRepricing() (ZTAPIFXPricingPreview, error) {
	preview := ZTAPIFXPricingPreview{Rows: []ZTAPIFXPricingRow{}}
	if DB == nil {
		return preview, errors.New("ZTAPI database is not initialized")
	}
	policy, err := CurrentZTAPIFXPolicy(DB)
	if err != nil {
		return preview, err
	}
	preview.Policy = policy
	fxParams := ZTAPIABFXParams{}
	if policy != nil {
		if fxParams, err = policy.ABFXParams(); err != nil {
			return preview, err
		}
	}
	quote, err := ZTAPIQuotationABEntries()
	if err != nil {
		return preview, err
	}
	frozen, err := ZTAPIQuotationEntries()
	if err != nil {
		return preview, err
	}
	bridge, err := BuildZTAPIABQuotationIdentityBridge(quote, frozen)
	if err != nil {
		return preview, err
	}
	nameBySource := make(map[string]string, len(bridge.Mapped))
	for _, identity := range bridge.Mapped {
		nameBySource[identity.SourceModel] = identity.ModelName
	}
	var configs []ZTAPIModelConfig
	if err := DB.Where("published = ?", true).Order("source_model ASC").Find(&configs).Error; err != nil {
		return preview, err
	}
	publications, err := loadZTAPIRepriceablePublications(DB)
	if err != nil {
		return preview, err
	}
	bound := make(map[int]ZTAPIRuntimePublication, len(publications))
	for _, publication := range publications {
		bound[publication.ModelConfigID] = publication
	}
	for i := range configs {
		config := configs[i]
		row := ZTAPIFXPricingRow{PublicName: config.PublicNameValue(), SourceModel: config.SourceModel, Kind: "not_covered", Status: "unchanged"}
		publication, ok := bound[config.ID]
		if !ok {
			row.Status = "blocked"
			row.Reason = "没有有效的已发布价格"
			preview.Rows = append(preview.Rows, row)
			continue
		}
		var oldSource ZTAPIModelPriceSource
		if err := DB.First(&oldSource, publication.PriceSourceID).Error; err != nil {
			return preview, err
		}
		row.CurrentInputSale, row.CurrentOutputSale = ztapiFirstTierSale(&oldSource)
		row.CurrentFXMode = oldSource.FXMode
		// A corrected quotation takes the old price out of the catalog until it
		// is repriced, so the page can say why the model is off sale.
		if oldSource.SourceDocumentChecksum == quote.WorkbookSHA256 && validateZTAPIABPriceSource(&oldSource) != nil {
			row.NeedsRelist = true
			row.Reason = "当前价格与报价单不一致，已暂时下架，改价后恢复"
		}
		if oldSource.FXMode == ZTAPIFXModePlatformV1 {
			row.CurrentPlatform = oldSource.PlatformCNYPerUnit
		}
		name := nameBySource[config.SourceModel]
		var target ZTAPIModelPriceSource
		var buildErr error
		switch {
		case strings.TrimSpace(oldSource.MediaPriceContractJSON) != "" || name == ztapiImage2QuotationModel:
			row.Reason = "图片、视频模型本期不参与汇率重算"
			preview.Rows = append(preview.Rows, row)
			continue
		case name != "" && ztapiABTextQuotable(quote, name):
			row.Kind = "ab_text"
			row.QuotationModel = name
			if rows, _, basisErr := quote.PublishablePricingBasis(name); basisErr == nil && len(rows) == 1 && len(rows[0].TokenPriceRules) > 0 {
				row.QuotationGrade = rows[0].Grade
				row.QuoteCurrency = rows[0].TokenPriceRules[0].Currency
				row.CostCNYInput = ztapiQuotedInputCostCNY(rows[0], fxParams)
			}
			target, buildErr = BuildZTAPIABTextPriceSourceWithFX(quote, name, config.ID, 1, ztapiFXPreviewEffectiveAt, fxParams)
		case oldSource.Currency == "CNY" && strings.TrimSpace(oldSource.TokenPriceRulesJSON) == "":
			row.Kind = "legacy_cny"
			row.QuoteCurrency = "CNY"
			row.CostCNYInput = oldSource.InputPerMillion
			if fxParams.Mode != ZTAPIFXModePlatformV1 {
				row.Status = "blocked"
				row.Reason = "尚未设置汇率"
				preview.Rows = append(preview.Rows, row)
				continue
			}
			target = oldSource
			target.ID, target.Version, target.CreatedAt = 0, 0, 0
			target.FXMode = ZTAPIFXModePlatformV1
			target.PlatformCNYPerUnit = fxParams.PlatformRate.StringFixed(10)
			target.CNYPerUSD = target.PlatformCNYPerUnit
			target.UpstreamCNYPerUSD = "0"
		default:
			row.Reason = "本期不参与汇率重算"
			preview.Rows = append(preview.Rows, row)
			continue
		}
		if errors.Is(buildErr, ErrZTAPIUpstreamUSDRateUnset) {
			row.Status = "blocked"
			row.Reason = "上游美元汇率未设置"
			preview.Rows = append(preview.Rows, row)
			continue
		}
		if buildErr != nil {
			row.Status = "blocked"
			row.Reason = buildErr.Error()
			preview.Rows = append(preview.Rows, row)
			continue
		}
		row.ProposedInput, row.ProposedOutput = ztapiFirstTierSale(&target)
		if !row.NeedsRelist && ztapiSameDecimal(row.CurrentInputSale, row.ProposedInput) && ztapiSameDecimal(row.CurrentOutputSale, row.ProposedOutput) {
			row.Status = "unchanged"
		} else {
			row.Status = "reprice"
		}
		preview.Rows = append(preview.Rows, row)
	}
	return preview, nil
}

func ztapiABTextQuotable(quote ZTAPIABQuotationManifest, modelName string) bool {
	_, err := BuildZTAPIABTextPriceSource(quote, modelName, 1, 1, ztapiFXPreviewEffectiveAt)
	return err == nil
}

func ztapiQuotedInputCostCNY(row ZTAPIABQuotationEntry, fxParams ZTAPIABFXParams) string {
	if len(row.TokenPriceRules) == 0 {
		return ""
	}
	rule := row.TokenPriceRules[0]
	cost, err := decimal.NewFromString(rule.Cost[ZTAPIBillingDimensionInputTokens])
	if err != nil {
		return ""
	}
	if rule.Currency == "USD" {
		if fxParams.Mode != ZTAPIFXModePlatformV1 || !fxParams.UpstreamUSDRate.IsPositive() {
			return ""
		}
		cost = cost.Mul(fxParams.UpstreamUSDRate)
	}
	return cost.Round(10).String()
}

// ZTAPIRootOperatorID names the account that owns automatic catalog changes.
func ZTAPIRootOperatorID() (int, error) {
	if DB == nil {
		return 0, errors.New("ZTAPI database is not initialized")
	}
	var ids []int
	if err := DB.Model(&User{}).Where("role = ?", common.RoleRootUser).Order("id ASC").Limit(1).
		Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	return ids[0], nil
}

// RunZTAPIFXInitialRepricing applies the seeded platform rate once, so the
// first release of FX-aware pricing needs no manual repricing.
func RunZTAPIFXInitialRepricing() {
	if DB == nil {
		return
	}
	var done int64
	if err := DB.Model(&Option{}).Where(&Option{Key: ztapiFXInitialRepricingOptionKey}).Count(&done).Error; err != nil {
		common.SysError("ZTAPI initial FX repricing state is unreadable: " + err.Error())
		return
	}
	if done > 0 {
		return
	}
	operatorID, err := ZTAPIRootOperatorID()
	if err != nil || operatorID <= 0 {
		common.SysLog("ZTAPI initial FX repricing postponed: no root operator yet")
		return
	}
	result, err := ApplyZTAPIFXRepricing(operatorID)
	if err != nil {
		common.SysError("ZTAPI initial FX repricing failed: " + err.Error())
		return
	}
	common.SysLog(fmt.Sprintf("ZTAPI initial FX repricing: republished=%d unchanged=%d skipped=%d",
		result.Republished, result.Unchanged, len(result.Skipped)))
	marker := Option{Key: ztapiFXInitialRepricingOptionKey, Value: strconv.FormatInt(common.GetTimestamp(), 10)}
	if err := DB.Save(&marker).Error; err != nil {
		common.SysError("ZTAPI initial FX repricing marker was not stored: " + err.Error())
	}
}
