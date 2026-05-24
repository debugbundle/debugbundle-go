package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

const maxRetryAfter = 5 * time.Minute

type HTTPTransport struct {
	endpoint string
	client   *http.Client
}

func NewHTTPTransport(endpoint string, timeout time.Duration) *HTTPTransport {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &HTTPTransport{
		endpoint: endpoint,
		client:   &http.Client{Timeout: timeout},
	}
}

func (transport *HTTPTransport) Send(ctx context.Context, request Request) (Response, error) {
	body, err := json.Marshal(map[string]any{"events": request.Events})
	if err != nil {
		return Response{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, transport.endpoint, bytes.NewReader(body))
	if err != nil {
		return Response{}, err
	}
	httpRequest.Header.Set("Authorization", "Bearer "+request.ProjectToken)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpResponse, err := transport.client.Do(httpRequest)
	if err != nil {
		return Response{}, err
	}
	defer func() { _ = httpResponse.Body.Close() }()
	return Response{
		StatusCode: httpResponse.StatusCode,
		RetryAfter: boundedRetryAfter(httpResponse.Header.Get("Retry-After")),
	}, nil
}

func (transport *HTTPTransport) Close() error {
	transport.client.CloseIdleConnections()
	return nil
}

func boundedRetryAfter(value string) time.Duration {
	if value == "" {
		return 0
	}
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0
	}
	duration := time.Duration(seconds * float64(time.Second))
	if duration < 0 {
		return 0
	}
	if duration > maxRetryAfter {
		return maxRetryAfter
	}
	return duration
}
