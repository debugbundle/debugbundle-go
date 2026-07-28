package debugbundle

import "encoding/json"

var retryableIngestionReasons = map[string]struct{}{
	"rate_limited":             {},
	"monthly_quota_exceeded":   {},
	"analytics_quota_exceeded": {},
}

type ingestionAcknowledgementDecision struct {
	kind             string
	accepted         int
	retryableIndices []int
	terminalErrors   []ingestionAcknowledgementError
	reason           string
}

type ingestionAcknowledgementError struct {
	Index  int    `json:"index"`
	Reason string `json:"reason"`
}

func decideIngestionAcknowledgement(body json.RawMessage, batchLength int) ingestionAcknowledgementDecision {
	if len(body) == 0 {
		return ingestionAcknowledgementDecision{kind: "legacy"}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return ingestionAcknowledgementDecision{kind: "protocol_failure", reason: "invalid_json"}
	}
	_, acceptedExists := fields["accepted"]
	_, rejectedExists := fields["rejected"]
	_, errorsExist := fields["errors"]
	if !acceptedExists && !rejectedExists && !errorsExist {
		return ingestionAcknowledgementDecision{kind: "legacy"}
	}
	if !acceptedExists || !rejectedExists || !errorsExist {
		return ingestionAcknowledgementDecision{kind: "protocol_failure", reason: "inconsistent_counts"}
	}

	var acknowledgement struct {
		Accepted int                             `json:"accepted"`
		Rejected int                             `json:"rejected"`
		Errors   []ingestionAcknowledgementError `json:"errors"`
	}
	if err := json.Unmarshal(body, &acknowledgement); err != nil ||
		acknowledgement.Accepted < 0 ||
		acknowledgement.Rejected < 0 ||
		acknowledgement.Accepted+acknowledgement.Rejected != batchLength ||
		len(acknowledgement.Errors) != acknowledgement.Rejected {
		return ingestionAcknowledgementDecision{kind: "protocol_failure", reason: "inconsistent_counts"}
	}

	seen := make(map[int]struct{}, len(acknowledgement.Errors))
	decision := ingestionAcknowledgementDecision{
		kind:     "acknowledged",
		accepted: acknowledgement.Accepted,
	}
	for _, ingestionError := range acknowledgement.Errors {
		if ingestionError.Index < 0 ||
			ingestionError.Index >= batchLength ||
			ingestionError.Reason == "" {
			return ingestionAcknowledgementDecision{kind: "protocol_failure", reason: "invalid_error_index"}
		}
		if _, duplicate := seen[ingestionError.Index]; duplicate {
			return ingestionAcknowledgementDecision{kind: "protocol_failure", reason: "invalid_error_index"}
		}
		seen[ingestionError.Index] = struct{}{}
		if _, retryable := retryableIngestionReasons[ingestionError.Reason]; retryable {
			decision.retryableIndices = append(decision.retryableIndices, ingestionError.Index)
		} else {
			decision.terminalErrors = append(decision.terminalErrors, ingestionError)
		}
	}
	return decision
}
