package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestZTAPIHealthIdentityCannotChangeAfterAdmission(t *testing.T) {
	s, c, _ := healthFixture(t)
	oldDB := DB
	DB = s.DB
	t.Cleanup(func() { DB = oldDB; InvalidateZTAPIAliasCache() })
	require.NoError(t, s.DB.AutoMigrate(&ZTAPIModelIdentity{}, &Ability{}, &Channel{}))
	healthAdmit(t, s, c, "identity-in-flight")
	alias := "zt-replacement-identity"
	next := c
	next.PublicName = &alias
	next.Published = false
	_, err := UpdateZTAPIModelConfigAndBilling(&next, c.Version, nil)
	require.ErrorContains(t, err, "health identity is locked")
	_, _, err = UpdateZTAPIModelIdentity(ZTAPIModelIdentityUpdate{ModelConfigID: c.ID, ExpectedVersion: c.Version, SourceModel: c.SourceModel, PublicName: alias, Protocol: ZTAPIProtocolOpenAICompatible, ProviderFamily: ZTAPIProviderOpenAI, SourceReference: "fixture", OperatorID: 1, Reason: "fixture replacement"})
	require.ErrorContains(t, err, "health identity is locked")
	t.Setenv("ZTAPI_HEALTH_ENABLED", "false")
	next = c
	next.SourceModel = "other-upstream"
	next.Published = false
	_, err = UpdateZTAPIModelConfigAndBilling(&next, c.Version, nil)
	require.ErrorContains(t, err, "health identity is locked")
	var actual ZTAPIModelConfig
	require.NoError(t, s.DB.First(&actual, c.ID).Error)
	require.Equal(t, c.Version, actual.Version)
	require.Equal(t, c.SourceModel, actual.SourceModel)
	// Price-only changes and unpublishing do not replace the observed identity.
	next = c
	next.Published = false
	next.InputPricePerMillion = 99
	changed, err := UpdateZTAPIModelConfigAndBilling(&next, c.Version, nil)
	require.NoError(t, err)
	require.Equal(t, 99.0, changed.InputPricePerMillion)
}

func TestZTAPIHealthIdentityCanMapBeforeFirstAdmission(t *testing.T) {
	s, c, _ := healthFixture(t)
	oldDB := DB
	DB = s.DB
	t.Cleanup(func() { DB = oldDB; InvalidateZTAPIAliasCache() })
	require.NoError(t, s.DB.AutoMigrate(&Ability{}, &Channel{}))
	alias := "zt-first-mapping"
	next := c
	next.PublicName = &alias
	next.Published = false
	updated, err := UpdateZTAPIModelConfigAndBilling(&next, c.Version, nil)
	require.NoError(t, err)
	require.Equal(t, alias, updated.PublicNameValue())
	var count int64
	require.NoError(t, s.DB.Model(&ZTAPIHealthRequest{}).Count(&count).Error)
	require.Zero(t, count)
}
