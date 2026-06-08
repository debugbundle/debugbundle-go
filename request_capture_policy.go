package debugbundle

import (
	"net/url"
	"strings"
	"time"
)

var balancedImmediateRequestStatuses = map[int]struct{}{408: {}, 423: {}, 424: {}, 425: {}, 429: {}}
var investigativeImmediateRequestStatuses = map[int]struct{}{408: {}, 409: {}, 423: {}, 424: {}, 425: {}, 429: {}}

func shouldCaptureRequestByPolicy(statusCode int, requestPath string, httpMethod string, policy CapturePolicy) bool {
	if statusCode == 0 {
		return policy.CaptureRequestEvents == CaptureRequestEventsAll
	}
	if isImmediateRequestIncidentStatus(statusCode, requestPath, httpMethod, policy) {
		return true
	}
	switch policy.CaptureRequestEvents {
	case CaptureRequestEventsOff:
		return false
	case CaptureRequestEventsFailuresOnly:
		return statusCode >= 500
	case CaptureRequestEventsFiltered:
		return false
	case CaptureRequestEventsAll:
		return true
	default:
		return false
	}
}

func isImmediateRequestIncidentStatus(statusCode int, requestPath string, httpMethod string, policy CapturePolicy) bool {
	if statusCode >= 500 {
		return true
	}
	for _, candidate := range policy.ImmediateClientErrorStatuses {
		if candidate == statusCode {
			return true
		}
	}
	if matchesImmediateClientErrorPathRule(statusCode, requestPath, httpMethod, policy.ImmediateClientErrorPathRules) {
		return true
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

func matchesImmediateClientErrorPathRule(statusCode int, requestPath string, httpMethod string, rules []ImmediateClientErrorPathRule) bool {
	if statusCode < 400 || statusCode > 499 || requestPath == "" {
		return false
	}
	normalizedPath := normalizeRequestPathForPolicy(requestPath)
	normalizedMethod := strings.ToUpper(strings.TrimSpace(httpMethod))
	for _, rule := range rules {
		if rule.StatusCode != statusCode {
			continue
		}
		if len(rule.Methods) > 0 {
			matchesMethod := false
			for _, method := range rule.Methods {
				if method == normalizedMethod {
					matchesMethod = true
					break
				}
			}
			if !matchesMethod {
				continue
			}
		}
		if strings.HasSuffix(rule.PathPattern, "*") {
			if strings.HasPrefix(normalizedPath, strings.TrimSuffix(rule.PathPattern, "*")) {
				return true
			}
			continue
		}
		if normalizedPath == rule.PathPattern {
			return true
		}
	}
	return false
}

func normalizeRequestPathForPolicy(value string) string {
	parsed, err := url.Parse(value)
	if err == nil && parsed.Path != "" {
		return parsed.Path
	}
	withoutQuery := strings.SplitN(value, "?", 2)[0]
	withoutFragment := strings.SplitN(withoutQuery, "#", 2)[0]
	if strings.HasPrefix(withoutFragment, "/") && withoutFragment != "" {
		return withoutFragment
	}
	return "/"
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
