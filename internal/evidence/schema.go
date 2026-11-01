// Package evidence implements the closed receiving contract for maintained SDK paths.
package evidence

import (
	_ "embed"
	"encoding/json"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

//go:embed event-envelope.schema.json
var schemaData []byte
var schemaOnce sync.Once
var rootSchema map[string]any
var schemaReady bool
var patterns = map[string]*regexp.Regexp{}
var uuidPattern = regexp.MustCompile(`^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$`)
var timestampPattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?Z$`)
var schemaKeywords = stringSet("$schema", "$id", "$comment", "$ref", "type", "properties", "required", "additionalProperties", "items", "anyOf", "enum", "const", "minItems", "maxItems", "minLength", "maxLength", "minimum", "maximum", "exclusiveMinimum", "pattern", "format")

func stringSet(items ...string) map[string]bool {
	result := map[string]bool{}
	for _, item := range items {
		result[item] = true
	}
	return result
}

func prepareSchema() {
	if json.Unmarshal(schemaData, &rootSchema) != nil {
		return
	}
	schemaReady = compileSchema(rootSchema, 0)
}
func compileSchema(value any, depth int) bool {
	if depth > 128 {
		return false
	}
	if _, ok := value.(bool); ok {
		return true
	}
	schema, ok := value.(map[string]any)
	if !ok {
		return false
	}
	for key, item := range schema {
		if !schemaKeywords[key] {
			return false
		}
		switch key {
		case "$ref":
			text, ok := item.(string)
			if !ok || !strings.HasPrefix(text, "#/") {
				return false
			}
		case "pattern":
			text, ok := item.(string)
			if !ok {
				return false
			}
			compiled, err := regexp.Compile(text)
			if err != nil {
				return false
			}
			patterns[text] = compiled
		case "format":
			if item != "date-time" && item != "uuid" {
				return false
			}
		case "type":
			allowed := stringSet("object", "array", "string", "number", "integer", "boolean", "null")
			if types, ok := item.([]any); ok {
				if len(types) == 0 {
					return false
				}
				for _, kind := range types {
					if !allowed[stringValue(kind)] {
						return false
					}
				}
			} else if !allowed[stringValue(item)] {
				return false
			}
		case "properties":
			fields, ok := item.(map[string]any)
			if !ok {
				return false
			}
			for _, child := range fields {
				if !compileSchema(child, depth+1) {
					return false
				}
			}
		case "items", "additionalProperties":
			if !compileSchema(item, depth+1) {
				return false
			}
		case "anyOf":
			children, ok := item.([]any)
			if !ok || len(children) == 0 {
				return false
			}
			for _, child := range children {
				if !compileSchema(child, depth+1) {
					return false
				}
			}
		}
	}
	return true
}
func stringValue(value any) string { text, _ := value.(string); return text }

type validationWork struct {
	steps     int
	exhausted bool
}

func (work *validationWork) validate(value, definition any, depth int) bool {
	work.steps++
	if work.steps > 100000 || depth > 64 {
		work.exhausted = true
		return false
	}
	if flag, ok := definition.(bool); ok {
		return flag
	}
	schema, ok := definition.(map[string]any)
	if !ok {
		return false
	}
	if ref, ok := schema["$ref"].(string); ok {
		var target any = rootSchema
		for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
			part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
			switch fields := target.(type) {
			case map[string]any:
				target = fields[part]
			case []any:
				index, err := strconv.Atoi(part)
				if err != nil || index < 0 || index >= len(fields) {
					return false
				}
				target = fields[index]
			default:
				return false
			}
		}
		return work.validate(value, target, depth+1)
	}
	if children, ok := schema["anyOf"].([]any); ok {
		matched := false
		for _, child := range children {
			if work.validate(value, child, depth+1) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if fixed, exists := schema["const"]; exists && !reflect.DeepEqual(fixed, value) {
		return false
	}
	if options, ok := schema["enum"].([]any); ok {
		found := false
		for _, option := range options {
			if reflect.DeepEqual(option, value) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if types, ok := schema["type"].([]any); ok {
		for _, kind := range types {
			branch := map[string]any{}
			for key, item := range schema {
				branch[key] = item
			}
			branch["type"] = kind
			if work.validate(value, branch, depth+1) {
				return true
			}
		}
		return false
	}
	kind := stringValue(schema["type"])
	switch kind {
	case "null":
		return value == nil
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "string":
		text, ok := value.(string)
		if !ok || !utf8.ValidString(text) {
			return false
		}
		length := 0
		for _, r := range text {
			length++
			if r > 65535 {
				length++
			}
		}
		if minimum, ok := schema["minLength"].(float64); ok && float64(length) < minimum {
			return false
		}
		if maximum, ok := schema["maxLength"].(float64); ok && float64(length) > maximum {
			return false
		}
		if pattern, ok := schema["pattern"].(string); ok && !patterns[pattern].MatchString(text) {
			return false
		}
		if schema["format"] == "uuid" && !uuidPattern.MatchString(text) {
			return false
		}
		if schema["format"] == "date-time" && !timestamp(text) {
			return false
		}
	case "number", "integer":
		number, ok := value.(float64)
		if !ok || math.IsNaN(number) || math.IsInf(number, 0) || kind == "integer" && number != math.Trunc(number) {
			return false
		}
		if minimum, ok := schema["minimum"].(float64); ok && number < minimum {
			return false
		}
		if maximum, ok := schema["maximum"].(float64); ok && number > maximum {
			return false
		}
		if minimum, ok := schema["exclusiveMinimum"].(float64); ok && number <= minimum {
			return false
		}
	case "array":
		items, ok := value.([]any)
		if !ok {
			return false
		}
		if minimum, ok := schema["minItems"].(float64); ok && float64(len(items)) < minimum {
			return false
		}
		if maximum, ok := schema["maxItems"].(float64); ok && float64(len(items)) > maximum {
			return false
		}
		if child, exists := schema["items"]; exists {
			for _, item := range items {
				if !work.validate(item, child, depth+1) {
					return false
				}
			}
		}
	case "object":
		fields, ok := value.(map[string]any)
		if !ok {
			return false
		}
		if required, ok := schema["required"].([]any); ok {
			for _, key := range required {
				if _, exists := fields[key.(string)]; !exists {
					return false
				}
			}
		}
		properties, _ := schema["properties"].(map[string]any)
		for key, item := range fields {
			if child, exists := properties[key]; exists {
				if !work.validate(item, child, depth+1) {
					return false
				}
				continue
			}
			if additional, exists := schema["additionalProperties"]; exists && !work.validate(item, additional, depth+1) {
				return false
			}
		}
	}
	return true
}
func timestamp(value string) bool {
	if !timestampPattern.MatchString(value) {
		return false
	}
	// UTC grammar and time.Parse together reject offsets and 24:00 consistently.
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

// Check preserves validation unavailability separately from an invalid envelope.
func Check(event map[string]any) (valid bool, failure error) {
	defer func() {
		if recover() != nil {
			valid = false
			failure = ErrUnavailable
		}
	}()
	schemaOnce.Do(prepareSchema)
	if !schemaReady {
		return false, ErrUnavailable
	}
	work := validationWork{}
	accepted := work.validate(event, rootSchema, 0)
	if work.exhausted {
		return false, ErrBudget
	}
	if !accepted {
		return false, nil
	}
	return semanticEvent(event), nil
}

// Validate is the fail-closed adapter form for normalized JSON.
func Validate(event map[string]any) bool { valid, err := Check(event); return err == nil && valid }
