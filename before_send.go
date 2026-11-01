package debugbundle

import (
	"encoding/json"
	"errors"

	"github.com/debugbundle/debugbundle-go/v3/internal/evidence"
)

type BeforeSendFunc func(EventEnvelope) *EventEnvelope

// Invalid/throwing hooks retain the protected original. Unavailable or exhausted
// validation never restores content the application may have deliberately removed.
func applyBeforeSend(event EventEnvelope, hook BeforeSendFunc) (*EventEnvelope, string) {
	if hook == nil {
		return &event, ""
	}
	owned, err := evidence.CopyJSON(event)
	if err != nil {
		return &event, "before_send_clone_failed"
	}
	clone, err := envelopeFromOwned(owned)
	if err != nil {
		return &event, "before_send_clone_failed"
	}
	result, panicked := invokeBeforeSend(hook, *clone)
	if panicked {
		return &event, "before_send_failed"
	}
	if result == nil {
		return nil, ""
	}
	owned, err = evidence.CopyJSON(*result)
	if errors.Is(err, evidence.ErrBudget) || errors.Is(err, evidence.ErrUnavailable) {
		return nil, "before_send_validation_unavailable"
	}
	valid, validationErr := evidence.Check(owned)
	if validationErr != nil {
		return nil, "before_send_validation_unavailable"
	}
	if err != nil || !valid {
		return &event, "before_send_invalid_event"
	}
	accepted, err := envelopeFromOwned(owned)
	if err != nil {
		return nil, "before_send_validation_unavailable"
	}
	return accepted, ""
}
func envelopeFromOwned(owned map[string]any) (*EventEnvelope, error) {
	encoded, err := json.Marshal(owned)
	if err != nil {
		return nil, err
	}
	var event EventEnvelope
	if err = json.Unmarshal(encoded, &event); err != nil {
		return nil, err
	}
	return &event, nil
}
func invokeBeforeSend(hook BeforeSendFunc, event EventEnvelope) (result *EventEnvelope, panicked bool) {
	defer func() {
		if recover() != nil {
			result = nil
			panicked = true
		}
	}()
	return hook(event), false
}
func validBeforeSendEvent(event EventEnvelope) bool {
	owned, err := evidence.CopyJSON(event)
	return err == nil && evidence.Validate(owned)
}
