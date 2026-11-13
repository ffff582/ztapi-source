package relay

import (
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/types"
)

var ErrZTAPIVideoContractUnavailable = errors.New("verified video protocol contract is unavailable")

type ZTAPIVideoContractRegistry struct {
	contracts map[string]types.ZTAPIVideoProtocolContract
}

func NewZTAPIVideoContractRegistry(rawByPublicName map[string]string) (*ZTAPIVideoContractRegistry, error) {
	registry := &ZTAPIVideoContractRegistry{contracts: make(map[string]types.ZTAPIVideoProtocolContract, len(rawByPublicName))}
	for publicName, raw := range rawByPublicName {
		if publicName == "" || publicName != strings.TrimSpace(publicName) {
			return nil, ErrZTAPIVideoContractUnavailable
		}
		contract, _, err := types.ParseZTAPIVideoProtocolContract(raw)
		if err != nil {
			return nil, err
		}
		registry.contracts[publicName] = contract.Clone()
	}
	return registry, nil
}

func DefaultZTAPIVideoContractRegistry() *ZTAPIVideoContractRegistry {
	contracts := make(map[string]types.ZTAPIVideoProtocolContract, 3)
	for publicName, providerModel := range map[string]string{
		"zt-seedance-2.0":      "doubao-seedance-2.0",
		"zt-seedance-2.0-fast": "doubao-seedance-2-0-fast",
		"zt-seedance-2.0-mini": "doubao-seedance-2-0-mini",
	} {
		contract, _, err := types.BuildZTAPISeedanceProtocolContract(providerModel)
		if err == nil {
			contracts[publicName] = contract
		}
	}
	return &ZTAPIVideoContractRegistry{contracts: contracts}
}

func (registry *ZTAPIVideoContractRegistry) Resolve(publicName string) (types.ZTAPIVideoProtocolContract, error) {
	if registry == nil || publicName == "" || publicName != strings.TrimSpace(publicName) {
		return types.ZTAPIVideoProtocolContract{}, ErrZTAPIVideoContractUnavailable
	}
	contract, ok := registry.contracts[publicName]
	if !ok {
		return types.ZTAPIVideoProtocolContract{}, ErrZTAPIVideoContractUnavailable
	}
	return contract.Clone(), nil
}
