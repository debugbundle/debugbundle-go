package transport

import (
	"context"
	"encoding/json"
	"time"
)

type Request struct {
	ProjectToken string
	Events       []json.RawMessage
}

type Response struct {
	StatusCode      int
	RetryAfter      time.Duration
	WrittenFilePath string
	// Body is optional so existing custom transports retain status-only success semantics.
	Body json.RawMessage
}

type Sender interface {
	Send(context.Context, Request) (Response, error)
}
