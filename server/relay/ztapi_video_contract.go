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
	// No public AIHub Seedance task contract is evidenced yet. Keep production
	// routing empty until an exact provider contract passes paid acceptance.
	return &ZTAPIVideoContractRegistry{contracts: map[string]types.ZTAPIVideoProtocolContract{}}
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
