package model

import (
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
)

func TestZTAPIImageSalePricingProjectionRetainsRetailContract(t *testing.T) {
	_, _ = setupImageSalePublicationForTest(t)
	_, err := PublishZTAPIImageSale(7, approvedImageSalePublicationRequestForTest())
	require.NoError(t, err)
	publications, err := loadZTAPIActivePublications(DB)
	require.NoError(t, err)
	pricing := projectZTAPIPublicPricing(publications)
	require.Len(t, pricing, 1)
	require.NotNil(t, pricing[0].ImageSale)
	require.Equal(t, types.ZTAPIImageSaleBillingUnit, pricing[0].BillingUnit)
	require.Equal(t, "0.60", pricing[0].ImageSale.BasePrices["4K"])
}

func TestZTAPIImageSaleHealthUnpublishRetainsPolicyPointer(t *testing.T) {
	db, _ := setupImageSalePublicationForTest(t)
	next, err := PublishZTAPIImageSale(7, approvedImageSalePublicationRequestForTest())
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&ZTAPIHealthIncident{}))
	store := NewZTAPIHealthStore(db)
	err = db.Transaction(func(tx *gorm.DB) error {
		config, err := store.unpublishTx(tx, &ZTAPIHealthState{ModelID: 85}, 999)
		if err != nil {
			return err
		}
		require.False(t, config.Published)
		require.Equal(t, next.ID, config.PublicationSnapshotID)
		return nil
	})
	require.NoError(t, err)
}
