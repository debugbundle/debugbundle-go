package debugbundle

import "time"

var balancedImmediateRequestStatuses = map[int]struct{}{408: {}, 423: {}, 424: {}, 425: {}, 429: {}}
var investigativeImmediateRequestStatuses = map[int]struct{}{408: {}, 409: {}, 423: {}, 424: {}, 425: {}, 429: {}}
var balancedAnomalyStatuses = map[int]struct{}{400: {}, 401: {}, 403: {}, 404: {}, 409: {}, 410: {}, 422: {}}
var investigativeAnomalyStatuses = map[int]struct{}{400: {}, 401: {}, 403: {}, 404: {}, 409: {}, 410: {}, 422: {}}

func shouldCaptureRequestByPolicy(statusCode int, policy CapturePolicy) bool {
	if statusCode == 0 {
		return policy.CaptureRequestEvents == CaptureRequestEventsAll
	}
	if isImmediateRequestIncidentStatus(statusCode, policy) {
		return true
	}
	switch policy.CaptureRequestEvents {
	case CaptureRequestEventsOff:
		return false
	case CaptureRequestEventsFailuresOnly:
		return isRequestAnomalyCandidateStatus(statusCode, policy.Preset)
	case CaptureRequestEventsFiltered:
		return false
	case CaptureRequestEventsAll:
		return true
	default:
		return false
	}
}

func isImmediateRequestIncidentStatus(statusCode int, policy CapturePolicy) bool {
	if statusCode >= 500 {
		return true
	}
	for _, candidate := range policy.ImmediateClientErrorStatuses {
		if candidate == statusCode {
			return true
		}
	}
	switch policy.Preset {
	case "investigative":
		_, ok := investigativeImmediateRequestStatuses[statusCode]
		return ok
	case "balanced":
		_, ok := balancedImmediateRequestStatuses[statusCode]
		return ok
	default:
		return false
	}
}

func isRequestAnomalyCandidateStatus(statusCode int, preset string) bool {
	if statusCode < 400 || statusCode >= 500 {
		return false
	}
	switch preset {
	case "investigative":
		_, ok := investigativeAnomalyStatuses[statusCode]
		return ok
	case "balanced":
		_, ok := balancedAnomalyStatuses[statusCode]
		return ok
	default:
		return false
	}
}

func (directive RemoteProbeDirective) Matches(label, service, environment string, now time.Time) bool {
	if !directive.ExpiresAt.After(now) {
		return false
	}
	if directive.Service != "*" && directive.Service != service {
		return false
	}
	if directive.Environment != "*" && directive.Environment != environment {
		return false
	}
	if directive.LabelPattern == "*" {
		return true
	}
	if stringsHasSuffix(directive.LabelPattern, ".*") {
		prefix := stringsTrimSuffix(directive.LabelPattern, ".*")
		return label == prefix || stringsHasPrefix(label, prefix+".")
	}
	return directive.LabelPattern == label
}
