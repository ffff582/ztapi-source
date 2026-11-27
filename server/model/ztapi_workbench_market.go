package model

import (
	"context"
	"slices"
	"time"

	"gorm.io/gorm"
)

const workbenchSignalFreshness = 15 * time.Minute

// ZTAPIWorkbenchModelSignals reads existing platform health evidence. It never
// issues a probe and never affects catalog permissions or admission.
func ZTAPIWorkbenchModelSignals(ctx context.Context) map[string]string {
	if DB == nil || !ZTAPIHealthEnabled() {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
	defer cancel()
	publications, err := ListZTAPIActiveRuntimePublications()
	if err != nil {
		return nil
	}
	return workbenchModelSignals(ctx, DB, publications, time.Now())
}

func workbenchModelSignals(ctx context.Context, db *gorm.DB, publications []ZTAPIRuntimePublication, now time.Time) map[string]string {
	ids := make([]int, 0, len(publications))
	byID := make(map[int]ZTAPIRuntimePublication, len(publications))
	for _, pub := range publications {
		ids = append(ids, pub.ModelConfigID)
		byID[pub.ModelConfigID] = pub
	}
	if len(ids) == 0 {
		return nil
	}
	var states []ZTAPIHealthState
	if err := db.WithContext(ctx).Select("model_id", "open").Where("model_id IN ?", ids).Find(&states).Error; err != nil {
		return nil
	}
	signals := make(map[string]string)
	for _, state := range states {
		if state.Open {
			signals[byID[state.ModelID].PublicName] = "unavailable"
		}
	}
	var routes []struct {
		ModelID       int
		ConfigVersion uint64
		ChannelID     int
		PublicModel   string
		LastSuccess   int64
		LastProblem   int64
	}
	if err := db.WithContext(ctx).Table("ztapi_health_events AS e").
		Select(`e.model_id, e.config_version, e.channel_id, e.public_model,
			MAX(CASE WHEN e.result = 'success' AND e.result_valid = ? THEN e.completed_at ELSE 0 END) AS last_success,
			MAX(CASE WHEN e.result IN ('failure', 'suspected') THEN e.completed_at ELSE 0 END) AS last_problem`, true).
		Joins("JOIN ztapi_health_states s ON s.model_id = e.model_id AND s.generation = e.generation").
		Where("e.model_id IN ? AND e.completed_at BETWEEN ? AND ?", ids, now.Add(-workbenchSignalFreshness).Unix(), now.Unix()).
		Where("e.stale_generation = ? AND e.source IN ? AND e.result IN ?", false, []string{"real", "probe"}, []string{"success", "suspected", "failure"}).
		Group("e.model_id, e.config_version, e.channel_id, e.public_model").Scan(&routes).Error; err != nil {
		return signals
	}
	for _, route := range routes {
		pub := byID[route.ModelID]
		if pub.Version == route.ConfigVersion && pub.PublicName == route.PublicModel && route.LastSuccess > route.LastProblem &&
			signals[pub.PublicName] == "" && slices.Contains(pub.AllowedChannelIDs, route.ChannelID) {
			signals[pub.PublicName] = "available"
		}
	}
	return signals
}
