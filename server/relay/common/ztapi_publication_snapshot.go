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

package common

import (
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

const ztapiPublicationSnapshotContextKey = "ztapi_publication_snapshot"

// ZTAPIPublicationSnapshot is the authorization and billing authority captured
// once for an alias request. It must not be refreshed during the request.
type ZTAPIPublicationSnapshot struct {
	PublicationID          int
	Version                uint64
	PublicName             string
	SourceModel            string
	Modality               string
	PriceSourceID          int64
	PriceSourceVersion     uint64
	BillingDimensions      []string
	SaleUSD                map[string]string
	MediaPriceContractJSON string
	AllowedGroups          []string
	AllowedChannelIDs      []int
	AuthorizedGroup        string
	InputPricePerMillion   float64
	OutputPricePerMillion  float64
	CacheReadRatio         float64
	CacheCreationRatio     float64
	CacheCreation5mRatio   float64
	CacheCreation1hRatio   float64
	ImageRatio             float64
	AudioRatio             float64
	AudioCompletionRatio   float64
	ImageProtocolContract  *types.ZTAPIImageProtocolContract `json:"-"`
	VideoProtocolContract  *types.ZTAPIVideoProtocolContract `json:"-"`
}

func (snapshot *ZTAPIPublicationSnapshot) Clone() *ZTAPIPublicationSnapshot {
	if snapshot == nil {
		return nil
	}
	clone := *snapshot
	clone.AllowedGroups = append([]string(nil), snapshot.AllowedGroups...)
	clone.AllowedChannelIDs = append([]int(nil), snapshot.AllowedChannelIDs...)
	clone.BillingDimensions = append([]string(nil), snapshot.BillingDimensions...)
	if snapshot.SaleUSD != nil {
		clone.SaleUSD = make(map[string]string, len(snapshot.SaleUSD))
		for dimension, price := range snapshot.SaleUSD {
			clone.SaleUSD[dimension] = price
		}
	}
	if snapshot.ImageProtocolContract != nil {
		contract := snapshot.ImageProtocolContract.Clone()
		clone.ImageProtocolContract = &contract
	}
	if snapshot.VideoProtocolContract != nil {
		contract := snapshot.VideoProtocolContract.Clone()
		clone.VideoProtocolContract = &contract
	}
	return &clone
}

func SetZTAPIPublicationSnapshot(c *gin.Context, snapshot *ZTAPIPublicationSnapshot) {
	if c == nil || snapshot == nil {
		return
	}
	c.Set(ztapiPublicationSnapshotContextKey, snapshot.Clone())
}

func GetZTAPIPublicationSnapshot(c *gin.Context) *ZTAPIPublicationSnapshot {
	if c == nil {
		return nil
	}
	value, exists := c.Get(ztapiPublicationSnapshotContextKey)
	if !exists {
		return nil
	}
	snapshot, ok := value.(*ZTAPIPublicationSnapshot)
	if !ok {
		return nil
	}
	return snapshot.Clone()
}
