package model

import (
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func ztapiHealthOpenModelIDs(db *gorm.DB) ([]int, error) {
	var ids []int
	err := db.Model(&ZTAPIHealthState{}).Where(clause.Eq{Column: "open", Value: true}).Pluck("model_id", &ids).Error
	if ztapiHealthLegacySchema(err) {
		return nil, nil
	}
	return ids, err
}

// Check durable state on every cache read, including instrumentation-disabled
// workers. A cached publication cannot outlive an independently committed trip.
func filterZTAPIHealthAliasCache() error {
	ztapiAliasCache.RLock()
	epoch := ztapiAliasCache.epoch
	needsAuthority := false
	for _, p := range ztapiAliasCache.aliases {
		if p.SnapshotID > 0 && p.Version > 0 {
			needsAuthority = true
			break
		}
	}
	ztapiAliasCache.RUnlock()
	authority := map[int]ztapiHealthPublication{}
	if needsAuthority {
		var err error
		authority, err = ztapiHealthPublicationAuthority(DB)
		if err != nil {
			return err
		}
	}
	ztapiAliasCache.Lock()
	defer ztapiAliasCache.Unlock()
	if epoch != ztapiAliasCache.epoch {
		return ErrZTAPIModelVersionConflict
	}
	filterZTAPIHealthAliasCacheLocked(authority)
	return nil
}

type ztapiHealthPublication struct {
	ModelConfigID  int
	SnapshotID     int64
	Version        uint64
	PublicName     string
	SourceModel    string
	Protocol       string
	ProviderFamily string
}

// One statement observes publication, snapshot and breaker authority together.
// Public names, like sale prices, belong to the approved snapshot. Legitimate
// identity edits invalidate it by changing the configuration version.
func ztapiHealthPublicationAuthority(db *gorm.DB) (map[int]ztapiHealthPublication, error) {
	if db == nil {
		return nil, errors.New("ZTAPI database is not initialized")
	}
	query := func(health bool) *gorm.DB {
		q := db.Table("ztapi_model_configs AS c").
			Select("c.id AS model_config_id, c.publication_snapshot_id AS snapshot_id, c.version, p.public_name, c.source_model, c.protocol, c.provider_family").
			Joins("JOIN ztapi_model_publication_snapshots AS p ON p.id = c.publication_snapshot_id AND p.model_config_id = c.id AND p.model_version = c.version").
			Where("c.published = ? AND c.source_model = p.source_model AND c.protocol = p.protocol AND c.provider_family = p.provider_family", true)
		if health {
			q = q.Joins("LEFT JOIN ztapi_health_states AS h ON h.model_id = c.id").Where("h.model_id IS NULL OR h.open = ?", false)
		}
		return q
	}
	var publications []ztapiHealthPublication
	err := query(true).Scan(&publications).Error
	if ztapiHealthLegacySchema(err) {
		// Pre-P5 installations still enforce publication authority.
		err = query(false).Scan(&publications).Error
		if err != nil && isZTAPITableMissingError(err) {
			return map[int]ztapiHealthPublication{}, nil
		}
	}
	if err != nil {
		return nil, err
	}
	authority := make(map[int]ztapiHealthPublication, len(publications))
	for _, publication := range publications {
		authority[publication.ModelConfigID] = publication
	}
	return authority, nil
}

func filterZTAPIHealthAliasCacheLocked(authority map[int]ztapiHealthPublication) {
	valid := func(p ztapiPublishedModel) bool {
		current, ok := authority[p.ModelConfigID]
		return ok && current.SnapshotID == p.SnapshotID && current.Version == p.Version &&
			current.PublicName == p.PublicName && current.SourceModel == p.SourceModel &&
			current.Protocol == p.Protocol && current.ProviderFamily == p.ProviderFamily
	}
	for name, p := range ztapiAliasCache.aliases {
		if !valid(p) {
			delete(ztapiAliasCache.aliases, name)
		}
	}
	for name, p := range ztapiAliasCache.sources {
		if p.PublicName != "" && !valid(p) {
			ztapiAliasCache.sources[name] = ztapiPublishedModel{ModelConfigID: p.ModelConfigID, SourceModel: p.SourceModel}
		}
	}
}

func ztapiHealthPublicationOpenTx(tx *gorm.DB, modelID int) (bool, error) {
	var state ZTAPIHealthState
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&state, "model_id = ?", modelID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) || ztapiHealthLegacySchema(err) {
		return false, nil
	}
	return state.Open, err
}
