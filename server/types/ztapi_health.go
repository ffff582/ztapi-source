package types

const (
	ZTAPIHealthOperationImageGenerate = "image_generate"
	ZTAPIHealthOperationVideoSubmit   = "video_submit"
	ZTAPIHealthOperationVideoFetch    = "video_fetch"
)

// ZTAPIHealthTicket is server-created admission evidence, never client input.
type ZTAPIHealthTicket struct {
	ExecutionID   string
	RequestID     string
	ModelID       int
	ConfigVersion uint64
	Generation    uint64
	PublicModel   string
	Modality      string
	Operation     string
	EntryProtocol string
	Stream        bool
	Source        string
	StartedAt     int64
}

// Only structured protocol metadata belongs here, not prompts or response bodies.
type ZTAPIHealthOutcome struct {
	Result              string
	Reason              string
	Operation           string
	ChannelID           int
	CredentialVersion   string
	UpstreamProtocol    string
	HTTPStatus          int
	UpstreamRequestID   string
	UpstreamTaskID      string
	ProviderErrorCode   string
	LatencyMilliseconds int64
	ResultValid         bool
	FinishReasons       []string
	TerminalStatus      string
	HasText             bool
	HasTool             bool
	HasMedia            bool
	Dispatched          bool
	TransportComplete   bool
	ClientCancelled     bool
	Attempts            []ZTAPIHealthAttempt
}

type ZTAPIHealthAttempt struct {
	Index             int
	ChannelID         int
	CredentialVersion string
	Protocol          string
	HTTPStatus        int
	UpstreamRequestID string
	ProviderErrorCode string
	Dispatched        bool
	Result            string
	Reason            string
}

// ClassifyZTAPIHealthSignal separates customer observations from diagnostic
// evidence. Real traffic can request verification, but it cannot itself create
// a verified failure.
func ClassifyZTAPIHealthSignal(source string, outcome ZTAPIHealthOutcome) ZTAPIHealthOutcome {
	if source != "real" {
		return outcome
	}
	if outcome.ClientCancelled {
		outcome.Result = "excluded"
		outcome.Reason = "client_cancelled"
	} else if ztapiHealthCustomerExclusionReason(outcome.Reason) {
		outcome.Result = "excluded"
	} else if outcome.Result == "failure" {
		outcome.Result = "suspected"
	}
	for index := range outcome.Attempts {
		attempt := &outcome.Attempts[index]
		if outcome.Result == "excluded" && ztapiHealthCustomerExclusionReason(outcome.Reason) {
			attempt.Result = "excluded"
			attempt.Reason = outcome.Reason
			continue
		}
		if attempt.Result == "failure" {
			attempt.Result = "suspected"
		}
		if attempt.Result == "unknown" && attempt.Reason == "length_without_visible_output" {
			attempt.Result = "excluded"
		}
	}
	return outcome
}

func ztapiHealthCustomerExclusionReason(reason string) bool {
	switch reason {
	case "invalid_input", "safety_refusal", "client_cancelled", "customer_timeout", "length_without_visible_output":
		return true
	default:
		return false
	}
}
