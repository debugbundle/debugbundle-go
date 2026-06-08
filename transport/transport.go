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
}

type Sender interface {
	Send(context.Context, Request) (Response, error)
}
