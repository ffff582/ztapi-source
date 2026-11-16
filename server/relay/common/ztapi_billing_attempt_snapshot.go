package common

import (
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

// Copy structured wire metadata without sealing the health outcome early.
func SnapshotZTAPIBillingAttempts(c *gin.Context) []types.ZTAPIHealthAttempt {
	session := GetZTAPIHealthSession(c)
	if session == nil || session.collector == nil {
		return nil
	}
	collector := session.collector
	collector.mu.Lock()
	defer collector.mu.Unlock()
	result := make([]types.ZTAPIHealthAttempt, 0, len(collector.attempts))
	for _, attempt := range collector.attempts {
		result = append(result, attempt.summary)
	}
	return result
}
