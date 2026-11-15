package middleware

import (
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
)

func TestZTAPIImageSaleSnapshotFromRuntimePreservesContract(t *testing.T) {
	raw := types.ZTAPIImageSaleContract20261109JSON()
	publication := model.ZTAPIRuntimePublication{ModelConfigID: 85, Version: 28,
		SourceModel: "gpt-image-2", PublicName: "zt-gp-image-2", ImageSaleContractJSON: raw}
	snapshot := ztapiPublicationSnapshotFromRuntime(publication, "default")
	publication.ImageSaleContractJSON = "changed"
	require.Equal(t, raw, snapshot.ImageSaleContractJSON)
	require.Equal(t, uint64(28), snapshot.Version)
	require.Equal(t, "default", snapshot.AuthorizedGroup)
	require.Empty(t, ztapiPublicationSnapshotFromRuntime(model.ZTAPIRuntimePublication{}, "default").ImageSaleContractJSON)
}
