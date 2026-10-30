package model

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

type ZTAPICashMarginDowngrade20261030 struct {
	CanDowngrade        bool     `json:"can_downgrade"`
	ActiveModels        []string `json:"active_models"`
	UnsettledRequestIDs []uint   `json:"unsettled_request_ids"`
	Requirement         string   `json:"requirement"`
}

// This is an observational guard, not an admission fence. The deployment
// operator must quiesce new admissions and drain in-flight requests before
// accepting a ready result and replacing the compatible reader.
func CheckZTAPICashMarginDowngrade20261030() (ZTAPICashMarginDowngrade20261030, error) {
	result := ZTAPICashMarginDowngrade20261030{ActiveModels: []string{}, UnsettledRequestIDs: []uint{},
		Requirement: "Quiesce admissions and drain in-flight requests; restore old-policy snapshots; require no new-policy or ambiguous unsettled requests before binary downgrade."}
	if DB == nil {
		return result, errors.New("database unavailable for downgrade check")
	}
	policies := []string{string(ZTAPIPricePolicyCashEnterprise20261030), string(ZTAPIPricePolicyCashPool20261030)}
	names := make([]string, 0, len(ztapiCashMarginSources20261030))
	for _, source := range ztapiCashMarginSources20261030 {
		contract, err := ztapiCashContractFor20261030(source)
		if err != nil {
			return result, err
		}
		names = append(names, contract.PublicName)
	}
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("ztapi_model_configs AS c").Select("c.public_name").
			Joins("LEFT JOIN ztapi_model_publication_snapshots AS p ON p.id = c.publication_snapshot_id").
			Joins("LEFT JOIN ztapi_model_price_sources AS s ON s.id = p.price_source_id").
			Where("c.published = ? AND c.public_name IN ? AND (p.id IS NULL OR s.id IS NULL OR p.price_policy IN ? OR s.price_policy IN ?)", true, names, policies, policies).
			Order("c.public_name").Scan(&result.ActiveModels).Error; err != nil {
			return err
		}
		var ids []int64
		if err := tx.Model(&ZTAPIModelPriceSource{}).Where("price_policy IN ?", policies).Pluck("id", &ids).Error; err != nil {
			return err
		}
		cashIDs := map[int64]bool{}
		for _, id := range ids {
			cashIDs[id] = true
		}
		var batch []ZTAPIRequestSettlement
		return tx.Select("id", "public_model", "price_snapshot_json", "status").
			Where("public_model IN ? AND status NOT IN ?", names, []string{ZTAPISettlementSettled, ZTAPISettlementReleased}).
			FindInBatches(&batch, 200, func(_ *gorm.DB, _ int) error {
				for _, row := range batch {
					var fields map[string]json.RawMessage
					blocked := common.RejectDuplicateJsonObjectMembers(strings.NewReader(row.PriceSnapshotJSON)) != nil ||
						common.Unmarshal([]byte(row.PriceSnapshotJSON), &fields) != nil
					policy, sourceID := "", int64(0)
					seen := map[string]bool{}
					for key, raw := range fields {
						key = strings.ReplaceAll(strings.ToLower(key), "_", "")
						if key != "pricepolicy" && key != "pricesourceid" {
							continue
						}
						if seen[key] {
							blocked = true
						}
						seen[key] = true
						if key == "pricepolicy" {
							if common.Unmarshal(raw, &policy) != nil {
								blocked = true
							}
							continue
						}
						// Decode the original numeric token separately, without float64
						// truncation of a persisted 64-bit source identity.
						if common.Unmarshal(raw, &sourceID) != nil {
							blocked = true
						}
					}
					if policy == "" || sourceID <= 0 || strings.HasPrefix(strings.ToLower(policy), "cash_") || cashIDs[sourceID] {
						blocked = true
					}
					if blocked {
						result.UnsettledRequestIDs = append(result.UnsettledRequestIDs, row.ID)
					}
				}
				return nil
			}).Error
	})
	if err == nil {
		result.CanDowngrade = len(result.ActiveModels) == 0 && len(result.UnsettledRequestIDs) == 0
	}
	return result, err
}
