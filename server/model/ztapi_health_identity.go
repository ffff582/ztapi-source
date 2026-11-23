package model

import (
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrZTAPIHealthIdentityLocked = errors.New("health identity is locked after request admission; create a separate catalog entry for a replacement identity")

// Model IDs that have served health-tracked requests retain their identity.
// Price/group edits and same-identity evidence updates remain independent.
func guardZTAPIHealthIdentityChangeTx(tx *gorm.DB, current *ZTAPIModelConfig, source, publicName, protocol, provider string) error {
	if current.SourceModel == source && current.PublicNameValue() == publicName && current.Protocol == protocol && current.ProviderFamily == provider {
		return nil
	}
	var state *ZTAPIHealthState
	var err error
	if ZTAPIHealthEnabled() {
		state, err = lockZTAPIHealthState(tx, current.ID)
	} else {
		state = &ZTAPIHealthState{}
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(state, "model_id = ?", current.ID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) || ztapiHealthLegacySchema(err) {
			return nil
		}
	}
	if err != nil {
		return err
	}
	if state.Open || state.IncidentID != 0 || state.CompletionSequence != 0 {
		return ErrZTAPIHealthIdentityLocked
	}
	// A prior catalog read may establish an old MySQL RR snapshot. Read the
	// latest admission under its lock after acquiring the shared health lock.
	var admission ZTAPIHealthRequest
	err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("execution_id").Where("model_id = ?", current.ID).Take(&admission).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return ErrZTAPIHealthIdentityLocked
}
