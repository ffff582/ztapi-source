package hostedsearch

import "context"

type Outcome string

const (
	OutcomeSucceeded Outcome = "succeeded"
	OutcomeEmpty     Outcome = "empty"
	OutcomeRejected  Outcome = "rejected"
	OutcomeUnknown   Outcome = "unknown"
)

type Source struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Summary string `json:"summary,omitempty"`
	Content string `json:"content,omitempty"`
}

type Result struct {
	Outcome           Outcome
	HTTPStatus        int
	UpstreamRequestID string
	ResponseSHA256    string
	Sources           []Source
	Title             string
	Content           string
}

type Provider interface {
	Search(context.Context, string, int) (Result, error)
	Fetch(context.Context, string) (Result, error)
}
