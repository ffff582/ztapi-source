package middleware

import (
	"context"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

const (
	ZTAPIHealthProbeCaseHeader  = "X-ZTAPI-Health-Case-ID"
	ZTAPIHealthProbeLeaseHeader = "X-ZTAPI-Health-Lease-Token"
)

type ztapiHealthProbePinValidator func(context.Context, string, string, string, time.Time) (model.ZTAPIHealthVerificationPin, error)

func validZTAPIHealthProbePin(pin model.ZTAPIHealthVerificationPin, caseID, leaseToken, requestID string) bool {
	entryProtocol := strings.TrimSpace(pin.EntryProtocol)
	if entryProtocol == "" {
		entryProtocol = strings.TrimSpace(pin.Protocol)
	}
	if pin.CaseID != caseID || pin.LeaseToken != leaseToken || pin.ProbeRequestID != requestID ||
		pin.ModelID <= 0 || strings.TrimSpace(pin.PublicModel) == "" || pin.ChannelID <= 0 ||
		entryProtocol == "" || strings.TrimSpace(pin.Protocol) == "" || pin.Generation == 0 || len(pin.CredentialVersion) != 64 {
		return false
	}
	for _, character := range pin.CredentialVersion {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func literalZTAPIHealthLoopbackPeer(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func rejectZTAPIHealthProbePin(c *gin.Context) {
	abortWithOpenAiMessage(c, http.StatusForbidden, "Invalid internal verification route.", types.ErrorCodeAccessDenied)
}

func ztapiHealthProbePinMiddleware(probeUserID int, now func() time.Time, validate ztapiHealthProbePinValidator) gin.HandlerFunc {
	return func(c *gin.Context) {
		caseID := strings.TrimSpace(c.GetHeader(ZTAPIHealthProbeCaseHeader))
		leaseToken := strings.TrimSpace(c.GetHeader(ZTAPIHealthProbeLeaseHeader))
		if caseID == "" && leaseToken == "" {
			c.Next()
			return
		}

		// These two headers are never allowed to reach channel overrides or an
		// upstream request, regardless of whether validation succeeds.
		c.Request.Header.Del(ZTAPIHealthProbeCaseHeader)
		c.Request.Header.Del(ZTAPIHealthProbeLeaseHeader)
		if caseID == "" || leaseToken == "" || probeUserID <= 0 || validate == nil || now == nil ||
			!literalZTAPIHealthLoopbackPeer(c.Request.RemoteAddr) || c.GetInt("id") != probeUserID {
			rejectZTAPIHealthProbePin(c)
			return
		}

		requestID := strings.TrimSpace(c.GetHeader("X-Request-ID"))
		if requestID == "" || c.GetString(common.RequestIdKey) != requestID {
			rejectZTAPIHealthProbePin(c)
			return
		}
		pin, err := validate(c.Request.Context(), caseID, leaseToken, requestID, now().UTC())
		if err != nil || !validZTAPIHealthProbePin(pin, caseID, leaseToken, requestID) {
			rejectZTAPIHealthProbePin(c)
			return
		}

		common.SetContextKey(c, constant.ContextKeyTokenSpecificChannelId, strconv.Itoa(pin.ChannelID))
		common.SetContextKey(c, constant.ContextKeyZTAPIHealthCredentialPin, pin.CredentialVersion)
		entryProtocol := strings.TrimSpace(pin.EntryProtocol)
		if entryProtocol == "" {
			entryProtocol = strings.TrimSpace(pin.Protocol)
		}
		relaycommon.SetZTAPIHealthProbePin(c, &relaycommon.ZTAPIHealthProbePin{
			CaseID: pin.CaseID, LeaseToken: pin.LeaseToken, RequestID: pin.ProbeRequestID,
			ModelID: pin.ModelID, PublicModel: pin.PublicModel, ChannelID: pin.ChannelID,
			EntryProtocol: entryProtocol, Protocol: pin.Protocol, Stream: pin.Stream, CredentialVersion: pin.CredentialVersion,
			Generation: pin.Generation,
		})
		c.Next()
	}
}

func ZTAPIHealthProbePin() gin.HandlerFunc {
	probeUserID, err := strconv.Atoi(strings.TrimSpace(os.Getenv("ZTAPI_HEALTH_PROBE_USER_ID")))
	if err != nil {
		probeUserID = 0
	}
	return ztapiHealthProbePinMiddleware(probeUserID, time.Now, model.LoadZTAPIHealthVerificationDispatchPin)
}
