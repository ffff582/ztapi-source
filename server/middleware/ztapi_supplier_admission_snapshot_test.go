package middleware

import (
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
)

func TestSupplierAdmissionSnapshotPreservesFrozenPolicy(t *testing.T) {
	publication := model.ZTAPIRuntimePublication{PricePolicy: string(model.ZTAPIPricePolicySupplierAdmission20261019)}
	snapshot := ztapiPublicationSnapshotFromRuntime(publication, "default")
	require.Equal(t, publication.PricePolicy, snapshot.PricePolicy)
	require.Equal(t, publication.PricePolicy, snapshot.Clone().PricePolicy)
}
