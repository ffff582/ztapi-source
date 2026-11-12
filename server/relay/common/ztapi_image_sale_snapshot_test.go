package common

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestZTAPIImageSaleSnapshotCloneFreezesContract(t *testing.T) {
	raw := types.ZTAPIImageSaleContract20261109JSON()
	snapshot := &ZTAPIPublicationSnapshot{ImageSaleContractJSON: raw, Version: 28}
	clone := snapshot.Clone()
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	SetZTAPIPublicationSnapshot(context, snapshot)
	snapshot.ImageSaleContractJSON = "changed"
	require.Equal(t, raw, clone.ImageSaleContractJSON)
	require.Equal(t, raw, GetZTAPIPublicationSnapshot(context).ImageSaleContractJSON)
	first := GetZTAPIPublicationSnapshot(context)
	first.ImageSaleContractJSON = "also changed"
	require.Equal(t, raw, GetZTAPIPublicationSnapshot(context).ImageSaleContractJSON)
	require.Empty(t, (&ZTAPIPublicationSnapshot{}).Clone().ImageSaleContractJSON)
}
