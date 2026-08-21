package routing

import "time"

// RouteEventData is intentionally allow-listed. Authentication material and the
// original user input have no representation in persisted route events.
type RouteEventData struct {
	Intent        DomainIntent `json:"intent"`
	Complexity    Complexity   `json:"complexity"`
	Confidence    float64      `json:"confidence"`
	Reason        string       `json:"reason"`
	Deterministic bool         `json:"deterministic"`
	NeedsRAG      bool         `json:"needs_rag"`
	NeedsModel    bool         `json:"needs_model"`
}

func NewRouteEventData(decision RouteDecision) RouteEventData {
	return RouteEventData{Intent: decision.Intent, Complexity: decision.Complexity, Confidence: decision.Confidence, Reason: decision.Reason, Deterministic: decision.Deterministic, NeedsRAG: decision.NeedsRAG, NeedsModel: decision.NeedsModel}
}

type ExecutorEventData struct {
	Intent     DomainIntent `json:"intent"`
	Complexity Complexity   `json:"complexity"`
	Handler    string       `json:"handler"`
	Status     string       `json:"status"`
	ErrorCode  string       `json:"error_code,omitempty"`
	DurationMS int64        `json:"duration_ms"`
}

func NewExecutorEventData(decision RouteDecision, handler, status, errorCode string, duration time.Duration) ExecutorEventData {
	return ExecutorEventData{Intent: decision.Intent, Complexity: decision.Complexity, Handler: handler, Status: status, ErrorCode: errorCode, DurationMS: duration.Milliseconds()}
}
