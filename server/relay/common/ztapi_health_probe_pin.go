package common

import (
	"errors"

	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

const ztapiHealthProbePinKey = "ztapi_health_probe_pin"

// ZTAPIHealthProbePin is server-only routing authority. CredentialVersion is
// an irreversible SHA-256 fingerprint, never an upstream secret.
type ZTAPIHealthProbePin struct {
	CaseID            string
	LeaseToken        string
	RequestID         string
	ModelID           int
	PublicModel       string
	ChannelID         int
	EntryProtocol     string
	Protocol          string
	Stream            bool
	CredentialVersion string
	Generation        uint64
}

type ZTAPIHealthProbeRouteCheck struct {
	CaseID            string
	LeaseToken        string
	ProbeRequestID    string
	ModelID           int
	ChannelID         int
	Protocol          string
	Stream            bool
	CredentialVersion string
	Generation        uint64
}

func SetZTAPIHealthProbePin(c *gin.Context, pin *ZTAPIHealthProbePin) {
	if c == nil || pin == nil {
		return
	}
	copy := *pin
	c.Set(ztapiHealthProbePinKey, &copy)
}

func GetZTAPIHealthProbePin(c *gin.Context) *ZTAPIHealthProbePin {
	if c == nil {
		return nil
	}
	value, exists := c.Get(ztapiHealthProbePinKey)
	if !exists {
		return nil
	}
	pin, ok := value.(*ZTAPIHealthProbePin)
	if !ok || pin == nil {
		return nil
	}
	copy := *pin
	return &copy
}

func validateZTAPIHealthProbeTicket(pin *ZTAPIHealthProbePin, ticket *types.ZTAPIHealthTicket) error {
	if pin == nil {
		return nil
	}
	if ticket == nil || ticket.ModelID != pin.ModelID || ticket.PublicModel != pin.PublicModel ||
		ticket.Stream != pin.Stream || ticket.Generation != pin.Generation || ticket.Source != "probe" ||
		ticket.RequestID != pin.RequestID || ticket.EntryProtocol != pin.EntryProtocol {
		return errors.New("ZTAPI health probe admission does not match its dispatch pin")
	}
	return nil
}
