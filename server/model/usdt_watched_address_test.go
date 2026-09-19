package model

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupUSDTWatchedAddressTest(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "watched-address.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&USDTWatchedReceivingAddress{}))
	previous := DB
	DB = db
	t.Cleanup(func() {
		DB = previous
		// Windows cannot remove the temporary directory while the file is open.
		if pool, err := db.DB(); err == nil {
			_ = pool.Close()
		}
	})
	return db
}

// An address that fails its own checksum would be watched forever and credit
// nobody, so a mistyped one is refused rather than stored.
func TestValidateTronBase58AddressAcceptsOnlyAChecksummedMainNetAddress(t *testing.T) {
	require.NoError(t, ValidateTronBase58Address("TTn3KVXkxSi9eHnpdLBFL1PpncMZmm6Tpu"))
	require.NoError(t, ValidateTronBase58Address("  TLrsoJgfpKZrACoG73r95PAcCyr6cF4DwX  "))

	for name, address := range map[string]string{
		"empty":            "",
		"too short":        "TTn3KVXkxSi9eHnpdLBFL1PpncMZmm6Tp",
		"wrong prefix":     "ATn3KVXkxSi9eHnpdLBFL1PpncMZmm6Tpu",
		"not base58":       "TTn3KVXkxSi9eHnpdLBFL1PpncMZmm60pu",
		"checksum changed": "TTn3KVXkxSi9eHnpdLBFL1PpncMZmm6Tpv",
	} {
		require.ErrorIs(t, ValidateTronBase58Address(address), ErrUSDTWatchedAddressInvalid, name)
	}
}

func TestCreateUSDTWatchedReceivingAddressRefusesDuplicatesAndBadInput(t *testing.T) {
	setupUSDTWatchedAddressTest(t)
	now := time.Date(2026, time.September, 19, 10, 0, 0, 0, time.UTC)

	record, err := CreateUSDTWatchedReceivingAddress("TTn3KVXkxSi9eHnpdLBFL1PpncMZmm6Tpu", " retired ", 7, now)
	require.NoError(t, err)
	require.True(t, record.Enabled)
	require.Equal(t, "retired", record.Label)
	require.Equal(t, 7, record.OperatorID)

	_, err = CreateUSDTWatchedReceivingAddress("TTn3KVXkxSi9eHnpdLBFL1PpncMZmm6Tpu", "again", 7, now)
	require.ErrorIs(t, err, ErrUSDTWatchedAddressDuplicate)

	_, err = CreateUSDTWatchedReceivingAddress("TTn3KVXkxSi9eHnpdLBFL1PpncMZmm6Tpv", "mistyped", 7, now)
	require.ErrorIs(t, err, ErrUSDTWatchedAddressInvalid)

	_, err = CreateUSDTWatchedReceivingAddress("TLrsoJgfpKZrACoG73r95PAcCyr6cF4DwX", "no operator", 0, now)
	require.ErrorIs(t, err, ErrUSDTWatchedAddressInvalid)
}

// Every enabled address is one more upstream query per poll, so the set is
// capped rather than allowed to exhaust the provider's rate limit.
func TestEnabledUSDTWatchedAddressesAreCapped(t *testing.T) {
	db := setupUSDTWatchedAddressTest(t)
	now := time.Date(2026, time.September, 19, 10, 0, 0, 0, time.UTC)

	for index := 0; index < MaxEnabledUSDTWatchedAddresses; index++ {
		require.NoError(t, db.Create(&USDTWatchedReceivingAddress{
			Address: fmt.Sprintf("TSeeded%028d", index), Enabled: true,
			OperatorID: 7, CreatedAt: now.Unix(), UpdatedAt: now.Unix(),
		}).Error)
	}

	_, err := CreateUSDTWatchedReceivingAddress("TTn3KVXkxSi9eHnpdLBFL1PpncMZmm6Tpu", "one too many", 7, now)
	require.ErrorIs(t, err, ErrUSDTWatchedAddressLimit)

	var first USDTWatchedReceivingAddress
	require.NoError(t, db.Order("id ASC").First(&first).Error)
	_, err = SetUSDTWatchedReceivingAddressEnabled(first.ID, false, 7, now)
	require.NoError(t, err)

	record, err := CreateUSDTWatchedReceivingAddress("TTn3KVXkxSi9eHnpdLBFL1PpncMZmm6Tpu", "now there is room", 7, now)
	require.NoError(t, err)
	require.True(t, record.Enabled)

	_, err = SetUSDTWatchedReceivingAddressEnabled(first.ID, true, 7, now)
	require.ErrorIs(t, err, ErrUSDTWatchedAddressLimit)
}

func TestListEnabledUSDTWatchedAddressesReturnsOnlyEnabledOnes(t *testing.T) {
	setupUSDTWatchedAddressTest(t)
	now := time.Date(2026, time.September, 19, 10, 0, 0, 0, time.UTC)

	kept, err := CreateUSDTWatchedReceivingAddress("TTn3KVXkxSi9eHnpdLBFL1PpncMZmm6Tpu", "kept", 7, now)
	require.NoError(t, err)
	disabled, err := CreateUSDTWatchedReceivingAddress("TLrsoJgfpKZrACoG73r95PAcCyr6cF4DwX", "disabled", 7, now)
	require.NoError(t, err)
	_, err = SetUSDTWatchedReceivingAddressEnabled(disabled.ID, false, 9, now.Add(time.Minute))
	require.NoError(t, err)

	addresses, err := ListEnabledUSDTWatchedAddresses()
	require.NoError(t, err)
	require.Equal(t, []string{kept.Address}, addresses)

	all, err := ListUSDTWatchedReceivingAddresses()
	require.NoError(t, err)
	require.Len(t, all, 2)
	require.False(t, all[1].Enabled)
	require.Equal(t, 9, all[1].OperatorID)
}
