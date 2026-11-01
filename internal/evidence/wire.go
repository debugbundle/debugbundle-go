package evidence

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

var Features = []string{"exception_evidence_v1", "resource_observations_v1", "recovery_v1", "evidence_provenance_v1"}

func object(value any) map[string]any { result, _ := value.(map[string]any); return result }
func fresh(value any) bool {
	text, ok := value.(string)
	return ok && uuidPattern.MatchString(text) && text[14] == '4' && strings.ContainsRune("89aAbB", rune(text[19]))
}
func scope(value any) bool {
	fields := object(value)
	if len(fields) != 2 {
		return false
	}
	switch fields["kind"] {
	case "page":
		return fresh(fields["page_id"])
	case "operation":
		return fresh(fields["operation_id"])
	}
	return false
}
func Reference(value any) bool {
	fields := object(value)
	return len(fields) == 3 && fields["version"] == float64(1) && fresh(fields["failure_ref"]) && scope(fields["scope"])
}
func text(value any, maximum int) bool {
	if value == nil {
		return true
	}
	s, ok := value.(string)
	return ok && len(s) <= maximum
}

func semanticEvent(event map[string]any) bool {
	payload := object(event["payload"])
	kind := event["event_type"]
	if kind == "frontend_exception" {
		mobile := stringSet("@debugbundle/sdk-android", "@debugbundle/sdk-swift", "@debugbundle/sdk-react-native")[stringValue(event["sdk_name"])]
		field := "browser"
		if mobile {
			field = "device"
		}
		if object(payload[field]) == nil {
			return false
		}
	}
	if kind == "diagnostic_context" {
		if !fresh(payload["failure_ref"]) || !scope(payload["scope"]) {
			return false
		}
		if payload["kind"] == "recovery" {
			if !fresh(payload["attempt_id"]) || !text(payload["operation"], 64) {
				return false
			}
		} else if !resources(payload["observations"]) {
			return false
		}
		if object(payload["scope"])["kind"] == "page" {
			session, ok := object(event["correlation"])["session_id"].(string)
			if !ok || strings.TrimFunc(session, func(r rune) bool { return unicode.IsSpace(r) || r == 0xFEFF }) == "" {
				return false
			}
		}
		return true
	}
	additions := map[string]any{}
	for _, field := range []string{"exception_evidence", "execution", "resource_observations"} {
		value, exists := payload[field]
		if !exists {
			continue
		}
		if (kind != "backend_exception" || field == "resource_observations") && (kind != "frontend_exception" || field == "execution") {
			return false
		}
		switch field {
		case "exception_evidence":
			if !graph(value) {
				return false
			}
		case "execution":
			if !text(object(value)["name"], 128) {
				return false
			}
		case "resource_observations":
			if !resources(value) {
				return false
			}
		}
		additions[field] = value
	}
	if len(additions) == 0 {
		return true
	}
	reference := object(event["context"])["evidence_reference"]
	if Reference(reference) {
		additions["evidence_reference"] = reference
	}
	return Size(additions) <= 32768
}

func graph(value any) bool {
	fields := object(value)
	nodes := fields["nodes"].([]any)
	adjacency := map[float64][]float64{}
	for _, raw := range nodes {
		node := object(raw)
		id := node["id"].(float64)
		if _, exists := adjacency[id]; exists {
			return false
		}
		if !text(node["name"], 256) || !text(node["message"], 2048) || !text(node["stack"], 16384) {
			return false
		}
		for _, rawFrame := range node["frames"].([]any) {
			frame := object(rawFrame)
			if !text(frame["function"], 256) || !text(frame["file"], 2048) {
				return false
			}
		}
		adjacency[id] = nil
	}
	for _, raw := range nodes {
		node := object(raw)
		id := node["id"].(float64)
		edges := map[string]bool{}
		causal := 0
		for _, rawEdge := range node["relationships"].([]any) {
			edge := object(rawEdge)
			target := edge["target_id"].(float64)
			kind := edge["kind"].(string)
			key := kind + ":" + strconv.Itoa(int(target))
			if _, exists := adjacency[target]; !exists || edges[key] {
				return false
			}
			edges[key] = true
			if kind == "cause" || kind == "context" {
				causal++
			}
			adjacency[id] = append(adjacency[id], target)
		}
		if causal > 1 {
			return false
		}
	}
	reached := map[float64]bool{}
	active := map[float64]bool{}
	var visit func(float64, int) bool
	visit = func(id float64, depth int) bool {
		children, exists := adjacency[id]
		if depth > 4 || !exists || active[id] {
			return false
		}
		active[id] = true
		reached[id] = true
		for _, child := range children {
			if !visit(child, depth+1) {
				return false
			}
		}
		delete(active, id)
		return true
	}
	return visit(fields["root_id"].(float64), 0) && len(reached) == len(nodes) && Size(value) <= 32768
}
func resources(value any) bool {
	for _, raw := range object(value)["items"].([]any) {
		item := object(raw)
		if source := item["source_url"]; source != nil && !resourceURL(source.(string)) {
			return false
		}
	}
	return Size(value) <= 32768
}
func resourceURL(value string) bool {
	if len(value) == 0 || len(value) > 2048 || strings.HasPrefix(value, "//") {
		return false
	}
	for _, char := range value {
		if char <= 0x20 || char == 0x7F || strings.ContainsRune(`\?#`, char) {
			return false
		}
	}
	if strings.HasPrefix(value, "/") {
		return true
	}
	lower := strings.ToLower(value)
	start := 7
	if strings.HasPrefix(lower, "https://") {
		start = 8
	} else if !strings.HasPrefix(lower, "http://") {
		return false
	}
	// Parse only the authority: native URL parsers reject valid opaque percent/Unicode paths.
	authority := strings.SplitN(value[start:], "/", 2)[0]
	parsed, err := url.Parse(value[:start] + authority + "/")
	if err != nil || parsed.Hostname() == "" {
		return false
	}
	if parsed.User != nil {
		password, _ := parsed.User.Password()
		if parsed.User.Username() != "" || password != "" {
			return false
		}
	}
	if strings.HasPrefix(parsed.Host, "[") && net.ParseIP(parsed.Hostname()) == nil {
		return false
	}
	if port := parsed.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 0 || number > 65535 {
			return false
		}
	}
	return true
}
