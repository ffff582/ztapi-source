package model

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

const ztapiChannelSecretTestMasterKey = "ztapi-channel-secret-test-master-key-0123456789"

type ztapiChannelSecretStorageRow struct {
	ID                 int    `gorm:"column:id"`
	Key                string `gorm:"column:key"`
	ZTAPIKeyCiphertext string `gorm:"column:ztapi_key_ciphertext"`
}

func TestZTAPIChannelCiphertextSchemaIsMySQLCompatible(t *testing.T) {
	db, err := gorm.Open(mysql.New(mysql.Config{
		DSN:                       "gorm:gorm@tcp(localhost:9910)/gorm?charset=utf8&parseTime=True&loc=Local",
		SkipInitializeWithVersion: true,
	}), &gorm.Config{DisableAutomaticPing: true})
	require.NoError(t, err)

	statement := &gorm.Statement{DB: db}
	require.NoError(t, statement.Parse(&Channel{}))
	field := statement.Schema.LookUpField("ZTAPIKeyCiphertext")
	require.NotNil(t, field)
	dataType := strings.ToLower(db.Migrator().FullDataTypeOf(field).SQL)
	require.Contains(t, dataType, "varchar(4096)")
	require.NotContains(t, dataType, "text")
}

func setupZTAPIChannelSecretTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv(ztapiUpstreamMasterKeyEnv, ztapiChannelSecretTestMasterKey)
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Channel{}))
	return db
}

func TestZTAPIChannelSecretEncryptsManagedCredentialAtRest(t *testing.T) {
	db := setupZTAPIChannelSecretTestDB(t)
	channel := Channel{
		Name: "encrypted upstream", Type: 1, Key: "upstream-secret-value",
		Status: common.ChannelStatusEnabled, ZTAPIManaged: true,
	}
	require.NoError(t, db.Create(&channel).Error)

	var stored ztapiChannelSecretStorageRow
	require.NoError(t, db.Table("channels").Where("id = ?", channel.Id).Take(&stored).Error)
	require.Empty(t, stored.Key)
	require.True(t, strings.HasPrefix(stored.ZTAPIKeyCiphertext, ztapiChannelKeyCiphertextPrefix))
	require.NotContains(t, stored.ZTAPIKeyCiphertext, "upstream-secret-value")

	var loaded Channel
	require.NoError(t, db.First(&loaded, channel.Id).Error)
	require.Equal(t, "upstream-secret-value", loaded.Key)
}

func TestZTAPIChannelSecretDoesNotMaterializeOmittedKey(t *testing.T) {
	db := setupZTAPIChannelSecretTestDB(t)
	channel := Channel{
		Name: "masked upstream", Type: 1, Key: "masked-secret-value",
		Status: common.ChannelStatusEnabled, ZTAPIManaged: true,
	}
	require.NoError(t, db.Create(&channel).Error)

	var loaded Channel
	require.NoError(t, db.Omit("key").First(&loaded, channel.Id).Error)
	require.Empty(t, loaded.Key)
	require.NotEmpty(t, loaded.ZTAPIKeyCiphertext)
}

func TestZTAPIChannelSecretAllowsDisabledBlankPlaceholder(t *testing.T) {
	db := setupZTAPIChannelSecretTestDB(t)
	t.Setenv(ztapiUpstreamMasterKeyEnv, "")
	channel := Channel{
		Name: "disabled placeholder", Type: 1,
		Status: common.ChannelStatusManuallyDisabled, ZTAPIManaged: true,
	}
	require.NoError(t, db.Create(&channel).Error)

	var stored ztapiChannelSecretStorageRow
	require.NoError(t, db.Table("channels").Where("id = ?", channel.Id).Take(&stored).Error)
	require.Empty(t, stored.Key)
	require.Empty(t, stored.ZTAPIKeyCiphertext)
}

func TestZTAPIChannelSecretRejectsEnabledBlankPlaceholder(t *testing.T) {
	db := setupZTAPIChannelSecretTestDB(t)
	channel := Channel{
		Name: "enabled blank placeholder", Type: 1,
		Status: common.ChannelStatusEnabled, ZTAPIManaged: true,
	}
	require.Error(t, db.Create(&channel).Error)
}

func TestZTAPIChannelSecretRejectsWrongMasterKey(t *testing.T) {
	db := setupZTAPIChannelSecretTestDB(t)
	channel := Channel{
		Name: "wrong key upstream", Type: 1, Key: "decrypt-me",
		Status: common.ChannelStatusEnabled, ZTAPIManaged: true,
	}
	require.NoError(t, db.Create(&channel).Error)

	t.Setenv(ztapiUpstreamMasterKeyEnv, "different-ztapi-master-key-01234567890123456789")
	var loaded Channel
	require.Error(t, db.First(&loaded, channel.Id).Error)
}

func TestZTAPIChannelSecretMigratesLegacyManagedPlaintext(t *testing.T) {
	db := setupZTAPIChannelSecretTestDB(t)
	legacy := Channel{
		Name: "legacy upstream", Type: 1, Key: "legacy-plaintext-secret",
		Status: common.ChannelStatusEnabled,
	}
	require.NoError(t, db.Create(&legacy).Error)
	require.NoError(t, db.Model(&Channel{}).Where("id = ?", legacy.Id).Update("ztapi_managed", true).Error)

	require.NoError(t, migrateZTAPIChannelSecretsDB(db))

	var stored ztapiChannelSecretStorageRow
	require.NoError(t, db.Table("channels").Where("id = ?", legacy.Id).Take(&stored).Error)
	require.Empty(t, stored.Key)
	require.True(t, strings.HasPrefix(stored.ZTAPIKeyCiphertext, ztapiChannelKeyCiphertextPrefix))

	var loaded Channel
	require.NoError(t, db.First(&loaded, legacy.Id).Error)
	require.Equal(t, "legacy-plaintext-secret", loaded.Key)
}
