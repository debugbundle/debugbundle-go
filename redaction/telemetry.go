package redaction

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxTelemetryBytes = 256 * 1024

var ErrUnsafeTelemetry = errors.New("unsafe_input")
var ErrTelemetryBudget = errors.New("budget_exceeded")

var telemetryHeaders = regexp.MustCompile(`(?i)\b(Authorization|Proxy-Authorization|Cookie|Set-Cookie)\s*:\s*[^\r\n]*`)
var telemetryBearer = regexp.MustCompile(`(?i)\b(Bearer|Basic)\s+[A-Za-z0-9._~+/-]{6,}`)
var telemetryToken = regexp.MustCompile(`\bdbundle_(?:proj|mem|probe|agent)_[A-Za-z0-9_-]+\b`)
var telemetryPem = regexp.MustCompile(`(?i)-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----[\s\S]*?-----END (?:RSA |EC |OPENSSH )?PRIVATE KEY-----`)
var telemetryPemStart = regexp.MustCompile(`(?i)-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----`)
var telemetryUrl = regexp.MustCompile(`(?i)\bhttps?://[^\s<>"']+`)
var telemetryCard = regexp.MustCompile(`(?:[0-9][ -]?){12,18}[0-9]`)
var telemetryMalformed = regexp.MustCompile(`(?i)(?:password|token|secret|authorization|cookie)["']?\s*[:=]`)
var telemetryEscapedLabel = regexp.MustCompile(`(?i)(?:password|token|secret|authorization|cookie)%3[ad]`)
var telemetryCamel = regexp.MustCompile(`([a-z0-9])([A-Z])`)
var telemetrySeparators = regexp.MustCompile(`[^a-z0-9]+`)

type telemetryWork struct {
	nodes      int
	textBytes  int
	fields     map[string]struct{}
	extra      []string
	assignment *regexp.Regexp
}

// ProtectTelemetry enforces mandatory credential rules in addition to caller fields.
// It returns JSON-safe data and never returns the original input on budget or parse failure.
func ProtectTelemetry(value any, additionalFields []string) (protected any, failure error) {
	// Application-defined MarshalJSON methods can panic. Never let them crash the host.
	defer func() {
		if recover() != nil {
			protected = nil
			failure = ErrUnsafeTelemetry
		}
	}()
	if len(additionalFields) > 128 {
		return nil, ErrUnsafeTelemetry
	}
	fields := map[string]struct{}{}
	labels := append([]string{}, DefaultSensitiveFields...)
	labels = append(labels, "client_secret", "x_api_key", "accessToken", "refreshToken", "privateKey", "clientSecret")
	for _, field := range DefaultSensitiveFields {
		fields[canonicalKey(field)] = struct{}{}
	}
	for _, field := range additionalFields {
		if len(field) == 0 || len(field) > 64 || strings.TrimSpace(field) == "" {
			return nil, ErrUnsafeTelemetry
		}
		fields[canonicalKey(field)] = struct{}{}
	}
	assignment := regexp.MustCompile(`(?i)\b(` + strings.Join(labels, "|") + `)\b(["']?\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^\s&,;]+)`)
	work := telemetryWork{fields: fields, extra: additionalFields, assignment: assignment}
	if err := checkTelemetryInput(value); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > maxTelemetryBytes {
		return nil, ErrUnsafeTelemetry
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, ErrUnsafeTelemetry
	}
	result, err := work.visit(decoded, 0, true)
	if err != nil {
		return nil, err
	}
	output, err := json.Marshal(result)
	if err != nil || len(output) > maxTelemetryBytes {
		return nil, ErrTelemetryBudget
	}
	return result, nil
}

func canonicalKey(value string) string {
	var result strings.Builder
	for _, letter := range strings.ToLower(value) {
		if unicode.IsLetter(letter) || unicode.IsDigit(letter) {
			result.WriteRune(letter)
		}
	}
	return result.String()
}

func (work *telemetryWork) sensitiveKey(key string) bool {
	parts := telemetrySeparators.Split(strings.ToLower(telemetryCamel.ReplaceAllString(strings.TrimSpace(key), "${1}_${2}")), -1)
	for start := 0; start < len(parts); start++ {
		combined := ""
		for _, part := range parts[start:] {
			combined += part
			if _, found := work.fields[combined]; found {
				return true
			}
		}
	}
	return false
}

func (work *telemetryWork) visit(value any, depth int, structured bool) (any, error) {
	work.nodes++
	if work.nodes > 4096 {
		return nil, ErrTelemetryBudget
	}
	if depth > 16 {
		return redactedMarker, nil
	}
	switch current := value.(type) {
	case string:
		if !utf8.ValidString(current) {
			return nil, ErrUnsafeTelemetry
		}
		if len(current) > 16*1024 {
			return redactedMarker, nil
		}
		work.textBytes += len(current)
		if work.textBytes > maxTelemetryBytes {
			return nil, ErrTelemetryBudget
		}
		if structured && (strings.HasPrefix(current, "{") || strings.HasPrefix(current, "[")) {
			var nested any
			decoder := json.NewDecoder(strings.NewReader(current))
			decoder.UseNumber()
			if decoder.Decode(&nested) == nil {
				if _, object := nested.(map[string]any); object {
					return work.structuredString(nested)
				}
				if _, array := nested.([]any); array {
					return work.structuredString(nested)
				}
			}
		}
		cleaned := work.scrubText(current)
		if cleaned == current && (strings.HasPrefix(current, "{") || strings.HasPrefix(current, "[")) && telemetryMalformed.MatchString(current) {
			return redactedMarker, nil
		}
		return cleaned, nil
	case map[string]any:
		if len(current) > 256 {
			return redactedMarker, nil
		}
		output := make(map[string]any, len(current))
		for key, nested := range current {
			if len(key) > 128 {
				continue
			}
			work.textBytes += len(key)
			if work.textBytes > maxTelemetryBytes {
				return nil, ErrTelemetryBudget
			}
			if work.scrubText(key) != key {
				continue
			}
			if work.sensitiveKey(key) {
				output[key] = redactedMarker
				continue
			}
			cleaned, err := work.visit(nested, depth+1, structured)
			if err != nil {
				return nil, err
			}
			output[key] = cleaned
		}
		return output, nil
	case []any:
		if len(current) > 256 {
			return redactedMarker, nil
		}
		output := make([]any, len(current))
		for index, nested := range current {
			cleaned, err := work.visit(nested, depth+1, structured)
			if err != nil {
				return nil, err
			}
			output[index] = cleaned
		}
		return output, nil
	case json.Number:
		// Downstream capture policy consumes response_status as an int. The
		// JSON clone must preserve integral values as numbers with that type.
		if integer, err := strconv.ParseInt(string(current), 10, 0); err == nil {
			return int(integer), nil
		}
		fraction, err := strconv.ParseFloat(string(current), 64)
		if err != nil || math.IsInf(fraction, 0) || math.IsNaN(fraction) {
			return nil, ErrUnsafeTelemetry
		}
		return fraction, nil
	default:
		return value, nil
	}
}

func (work *telemetryWork) structuredString(value any) (any, error) {
	cleaned, err := work.visit(value, 0, false)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(cleaned)
	if err != nil {
		return nil, ErrUnsafeTelemetry
	}
	return string(encoded), nil
}

func validCard(candidate string) bool {
	digits := make([]int, 0, 19)
	for _, letter := range candidate {
		if letter >= '0' && letter <= '9' {
			digits = append(digits, int(letter-'0'))
		}
	}
	if len(digits) < 13 || len(digits) > 19 {
		return false
	}
	same := true
	for _, digit := range digits[1:] {
		if digit != digits[0] {
			same = false
			break
		}
	}
	if same {
		return false
	}
	sum := 0
	for index := len(digits) - 1; index >= 0; index-- {
		digit := digits[index]
		if (len(digits)-index)%2 == 0 {
			digit *= 2
			if digit > 9 {
				digit -= 9
			}
		}
		sum += digit
	}
	return sum%10 == 0
}

func (work *telemetryWork) scrubText(input string) string {
	return work.scrubTextValue(input, true)
}

func (work *telemetryWork) scrubTextValue(input string, scanURLs bool) string {
	if telemetryPemStart.MatchString(input) && !telemetryPem.MatchString(input) {
		return redactedMarker
	}
	output := input
	if telemetryEscapedLabel.MatchString(output) {
		decoded, err := url.PathUnescape(output)
		if err != nil {
			return redactedMarker
		}
		output = decoded
	}
	output = telemetryPem.ReplaceAllString(output, redactedMarker)
	output = telemetryHeaders.ReplaceAllString(output, `${1}: [REDACTED]`)
	output = telemetryBearer.ReplaceAllString(output, `${1} [REDACTED]`)
	output = telemetryToken.ReplaceAllString(output, redactedMarker)
	output = work.assignment.ReplaceAllString(output, `${1}${2}[REDACTED]`)
	for _, field := range work.extra {
		pattern, err := regexp.Compile(`(?i)\b(` + regexp.QuoteMeta(field) + `)\b(["']?\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^\s&,;]+)`)
		if err == nil {
			output = pattern.ReplaceAllString(output, `${1}${2}[REDACTED]`)
		}
	}
	cardRanges := telemetryCard.FindAllStringIndex(output, -1)
	for index := len(cardRanges) - 1; index >= 0; index-- {
		start, end := cardRanges[index][0], cardRanges[index][1]
		if (start > 0 && identifierByte(output[start-1])) || (end < len(output) && identifierByte(output[end])) {
			continue
		}
		if validCard(output[start:end]) {
			output = output[:start] + redactedMarker + output[end:]
		}
	}
	if !scanURLs {
		return output
	}
	return telemetryUrl.ReplaceAllStringFunc(output, func(candidate string) string {
		trimmed := strings.TrimRight(candidate, ").,;")
		return work.scrubURL(trimmed) + candidate[len(trimmed):]
	})
}

func identifierByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' || value == '_' || value == '-'
}

func (work *telemetryWork) scrubURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return redactedMarker
	}
	if parsed.User != nil {
		parsed.User = url.User("REDACTED")
	}
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	query := make([]string, 0)
	if parsed.RawQuery != "" {
		for _, pair := range strings.Split(parsed.RawQuery, "&") {
			parts := strings.SplitN(pair, "=", 2)
			key, keyError := url.QueryUnescape(parts[0])
			if keyError != nil {
				return redactedMarker
			}
			value := ""
			if len(parts) == 2 {
				value, err = url.QueryUnescape(parts[1])
				if err != nil {
					return redactedMarker
				}
			}
			if len(key) > 128 || work.scrubTextValue(key, false) != key {
				continue
			}
			if work.sensitiveKey(key) || work.scrubTextValue(value, false) != value {
				value = redactedMarker
			}
			query = append(query, url.QueryEscape(key)+"="+url.QueryEscape(value))
		}
	}
	parsed.RawQuery = strings.Join(query, "&")
	parsed.Fragment = ""
	return parsed.String()
}

// SafeEventIdentity scans scalar identities without masking protocol field names.
func SafeEventIdentity(event map[string]any, extra []string) bool {
	values := []any{event["schema_version"], event["sdk_name"], event["sdk_version"]}
	if correlation := event["correlation"]; correlation != nil {
		fields, ok := correlation.(map[string]any)
		if !ok || len(fields) > 8 {
			return false
		}
		for _, value := range fields {
			values = append(values, value)
		}
	}
	for _, value := range values {
		if value == nil {
			continue
		}
		scalar, ok := value.(string)
		if !ok {
			return false
		}
		cleaned, err := ProtectTelemetry(scalar, extra)
		if err != nil || cleaned != scalar {
			return false
		}
	}
	return true
}
