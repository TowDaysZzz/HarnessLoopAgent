package ragclient

import (
	"fmt"
	"time"
)

type APIError struct {
	HTTPStatus int
	Code       int
	Message    string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("RAG API error: http_status=%d code=%d message=%q", e.HTTPStatus, e.Code, e.Message)
}

func (e *APIError) Temporary() bool {
	return e.HTTPStatus == 429 || e.HTTPStatus >= 500
}

type ResponseTooLargeError struct {
	Size  int
	Limit int
}

func (e *ResponseTooLargeError) Error() string {
	return fmt.Sprintf("RAG response is too large: size=%d limit=%d", e.Size, e.Limit)
}
