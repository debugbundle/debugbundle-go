package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
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
	responseBody, readErr := io.ReadAll(io.LimitReader(httpResponse.Body, (1<<20)+1))
	if readErr != nil {
		return Response{}, readErr
	}
	if len(responseBody) > 1<<20 {
		return Response{}, errors.New("ingestion response exceeds size limit")
	}
	return Response{
		StatusCode: httpResponse.StatusCode,
		RetryAfter: boundedRetryAfter(httpResponse.Header.Get("Retry-After")),
		Body:       responseBody,
	}, nil
}

func (transport *HTTPTransport) Close() error {
	transport.client.CloseIdleConnections()
	return nil
}

func boundedRetryAfter(value string) time.Duration {
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil {
		date, dateErr := http.ParseTime(value)
		if dateErr != nil {
			return 0
		}
		seconds = time.Until(date).Seconds()
	}
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return 0
	}
	return time.Duration(min(maxRetryAfter.Seconds(), math.Max(0, seconds)) * float64(time.Second))
}
