package relay

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/debugbundle/debugbundle-go/v3/internal/evidence"
	"github.com/debugbundle/debugbundle-go/v3/transport"
)

func hasTypedEvidence(event map[string]any) bool {
	if event["event_type"] == "diagnostic_context" {
		return true
	}
	payload, _ := event["payload"].(map[string]any)
	for _, key := range []string{"exception_evidence", "execution", "resource_observations"} {
		if _, exists := payload[key]; exists {
			return true
		}
	}
	context, _ := event["context"].(map[string]any)
	return evidence.Reference(context["evidence_reference"])
}

// A held custom sender owns one existing worker. It cannot cause unbounded
// replacement goroutines; native HTTP uses context cancellation directly.
var relaySenderSlot = make(chan struct{}, 1)

type sendResult struct {
	response transport.Response
	err      error
}

func safeRelaySend(ctx context.Context, sender transport.Sender, request transport.Request) (transport.Response, error) {
	call := func() (response transport.Response, err error) {
		defer func() {
			if recover() != nil {
				err = errors.New("relay transport callback failed")
			}
		}()
		return sender.Send(ctx, request)
	}
	if _, native := sender.(*transport.HTTPTransport); native {
		return call()
	}
	select {
	case relaySenderSlot <- struct{}{}:
	default:
		return transport.Response{}, errors.New("relay transport busy")
	}
	result := make(chan sendResult, 1)
	go func() {
		defer func() { <-relaySenderSlot }()
		response, err := call()
		result <- sendResult{response, err}
	}()
	select {
	case completed := <-result:
		return completed.response, completed.err
	case <-ctx.Done():
		return transport.Response{}, ctx.Err()
	}
}
func (handler *handler) forwardAccepted(parent context.Context, events []json.RawMessage) (bool, bool) {
	sender := handler.forwardDeliveryTransport()
	if sender == nil {
		return false, false
	}
	if native, ok := sender.(*transport.HTTPTransport); ok {
		defer func() { _ = native.Close() }()
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	return handler.forwardChunks(ctx, sender, events, false)
}
func (handler *handler) forwardChunks(ctx context.Context, sender transport.Sender, events []json.RawMessage, retried bool) (bool, bool) {
	_, native := sender.(*transport.HTTPTransport)
	verified := true
	for len(events) > 0 {
		prepared, ok := handler.prepareForwardEvents(events, native)
		if !ok {
			return false, false
		}
		if ctx.Err() != nil {
			return false, false
		}
		count := len(prepared)
		if native {
			count = evidence.Prefix(prepared)
		}
		if count == 0 {
			return false, false
		}
		chunk := prepared[:count]
		response, err := safeRelaySend(ctx, sender, transport.Request{ProjectToken: handler.options.ProjectToken, Events: chunk})
		if err != nil || response.StatusCode < 200 || response.StatusCode >= 300 {
			return false, false
		}
		complete, invalid, valid := evidence.CompleteAcknowledgement(response.Body, count)
		lost := handler.evidence.ObserveBody(response.Body, native, valid)
		if !complete {
			if !native && len(response.Body) == 0 {
				verified = false
			} else if !retried && lost && valid && len(invalid) > 0 {
				var reply struct {
					Rejected int `json:"rejected"`
				}
				_ = json.Unmarshal(response.Body, &reply)
				if len(invalid) != reply.Rejected {
					return false, false
				}
				downgrade := []json.RawMessage{}
				for _, index := range invalid {
					var original map[string]any
					_ = json.Unmarshal(chunk[index], &original)
					if original["event_type"] != "frontend_exception" {
						return false, false
					}
					reduced := handler.evidence.Filter(original, true)
					if reduced == nil || reflect.DeepEqual(reduced, original) {
						return false, false
					}
					encoded, _ := json.Marshal(reduced)
					downgrade = append(downgrade, encoded)
				}
				ok, proof := handler.forwardChunks(ctx, sender, downgrade, true)
				if !ok || !proof {
					return false, false
				}
			} else {
				return false, false
			}
		}
		events = prepared[count:]
	}
	return true, verified
}

func (handler *handler) prepareForwardEvents(events []json.RawMessage, native bool) ([]json.RawMessage, bool) {
	prepared := make([]json.RawMessage, 0, len(events))
	for _, raw := range events {
		var event map[string]any
		if json.Unmarshal(raw, &event) != nil {
			return nil, false
		}
		event = handler.evidence.Filter(event, true)
		if event == nil {
			return nil, false
		}
		if native {
			event["project_token"] = handler.options.ProjectToken
		}
		encoded, err := json.Marshal(event)
		if err != nil {
			return nil, false
		}
		if native {
			var fits bool
			encoded, fits = evidence.FitEvent(encoded)
			if !fits {
				return nil, false
			}
		}
		prepared = append(prepared, encoded)
	}
	return prepared, true
}
