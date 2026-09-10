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
	Protocol          string
	HTTPStatus        int
	UpstreamRequestID string
	ProviderErrorCode string
	Dispatched        bool
}
