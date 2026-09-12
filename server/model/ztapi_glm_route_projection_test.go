package model

import (
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestZTAPIGLMFamilyInferenceUsesOpenAICompatibleAdapter(t *testing.T) {
	for _, source := range []string{"glm-4.7", "glm-5", "glm-5.1", "glm-5.2"} {
		family, supported := InferZTAPIModelFamily(source)
		require.True(t, supported, source)
		require.Equal(t, ZTAPIModelFamilyOpenAI, family, source)
	}
	for _, source := range []string{"glm", "glm5.2", "not-glm-5.2"} {
		family, supported := InferZTAPIModelFamily(source)
		require.False(t, supported, source)
		require.Empty(t, family, source)
	}
}

func TestZTAPIGLMCanRebuildTrustedRoutesAfterCircuitUnpublish(t *testing.T) {
	t.Setenv(ztapiUpstreamMasterKeyEnv, ztapiChannelSecretTestMasterKey)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "routes.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	previousDB, previousSQLite, previousMySQL := DB, common.UsingSQLite, common.UsingMySQL
	DB, common.UsingSQLite, common.UsingMySQL = db, true, false
	t.Cleanup(func() {
		DB, common.UsingSQLite, common.UsingMySQL = previousDB, previousSQLite, previousMySQL
		require.NoError(t, sqlDB.Close())
	})
	require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}, &ZTAPIModelConfig{}))

	baseURL := "https://approved-upstream.example.com/hub/v1"
	require.NoError(t, db.Create(&Channel{
		Id: 1, Type: constant.ChannelTypeOpenAI, BaseURL: &baseURL,
		Status: common.ChannelStatusEnabled, ZTAPIManaged: true,
		ZTAPIFamily: ZTAPIModelFamilyOpenAI, Key: "local-route-fixture-key",
	}).Error)
	require.NoError(t, db.Create(&Ability{Group: "default", Model: "glm-5.2", ChannelId: 1, Enabled: true}).Error)

	family, supported, err := ResolveZTAPIModelFamilyFromRoutes("glm-5.2")
	require.NoError(t, err)
	require.True(t, supported)
	require.Equal(t, ZTAPIModelFamilyOpenAI, family)

	ids, err := GetZTAPITrustedRouteChannelIDs("glm-5.2", family, []string{"default"})
	require.NoError(t, err)
	require.Equal(t, []int{1}, ids)
}
