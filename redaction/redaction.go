package redaction

import (
	"fmt"
	"reflect"
	"strings"
	"unicode"
)

const redactedMarker = "[REDACTED]"

var DefaultSensitiveFields = []string{
	"password",
	"secret",
	"token",
	"api_key",
	"apikey",
	"access_token",
	"refresh_token",
	"private_key",
	"passwd",
	"card_number",
	"cvv",
	"cvc",
	"pin",
	"expiry",
	"phone",
	"bearer",
	"session_id",
	"otp",
	"verification_code",
	"authorization",
	"cookie",
	"ssn",
	"credit_card",
}

type Redactor struct {
	sensitive map[string]struct{}
	maxDepth  int
	maxItems  int
	maxString int
}

func New(fields []string) *Redactor {
	if len(fields) == 0 {
		fields = DefaultSensitiveFields
	}
	sensitive := map[string]struct{}{}
	for _, field := range fields {
		sensitive[strings.ToLower(strings.TrimSpace(field))] = struct{}{}
	}
	return &Redactor{
		sensitive: sensitive,
		maxDepth:  6,
		maxItems:  50,
		maxString: 2048,
	}
}

func (redactor *Redactor) Redact(value any) any {
	return redactor.redactValue(reflect.ValueOf(value), 0, "", map[visitKey]struct{}{})
}

type visitKey struct {
	kind reflect.Kind
	ptr  uintptr
}

func (redactor *Redactor) redactValue(value reflect.Value, depth int, key string, visited map[visitKey]struct{}) any {
	if !value.IsValid() {
		return nil
	}
	if depth > redactor.maxDepth {
		return "[TruncatedDepth]"
	}
	for value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}
		if value.Kind() == reflect.Pointer {
			identifier := visitKey{kind: value.Kind(), ptr: value.Pointer()}
			if identifier.ptr != 0 {
				if _, exists := visited[identifier]; exists {
					return "[Circular]"
				}
				visited[identifier] = struct{}{}
			}
		}
		value = value.Elem()
	}

	if redactor.isSensitive(key) {
		return redactedMarker
	}

	switch value.Kind() {
	case reflect.String:
		text := value.String()
		if len(text) > redactor.maxString {
			return text[:redactor.maxString] + "...[truncated]"
		}
		return text
	case reflect.Bool:
		return value.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return value.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return value.Uint()
	case reflect.Float32, reflect.Float64:
		return value.Float()
	case reflect.Slice, reflect.Array:
		identifier := visitKey{kind: value.Kind(), ptr: value.Pointer()}
		if identifier.ptr != 0 {
			if _, exists := visited[identifier]; exists {
				return "[Circular]"
			}
			visited[identifier] = struct{}{}
		}
		limit := value.Len()
		if limit > redactor.maxItems {
			limit = redactor.maxItems
		}
		result := make([]any, 0, limit)
		for index := 0; index < limit; index++ {
			result = append(result, redactor.redactValue(value.Index(index), depth+1, key, visited))
		}
		if value.Len() > limit {
			result = append(result, fmt.Sprintf("...[truncated %d items]", value.Len()-limit))
		}
		return result
	case reflect.Map:
		identifier := visitKey{kind: value.Kind(), ptr: value.Pointer()}
		if identifier.ptr != 0 {
			if _, exists := visited[identifier]; exists {
				return "[Circular]"
			}
			visited[identifier] = struct{}{}
		}
		result := map[string]any{}
		iter := value.MapRange()
		count := 0
		for iter.Next() {
			if count >= redactor.maxItems {
				result["_truncated"] = fmt.Sprintf("%d additional entries omitted", value.Len()-count)
				break
			}
			mapKey := fmt.Sprint(iter.Key().Interface())
			result[mapKey] = redactor.redactValue(iter.Value(), depth+1, mapKey, visited)
			count++
		}
		return result
	case reflect.Struct:
		result := map[string]any{}
		count := 0
		for index := 0; index < value.NumField(); index++ {
			field := value.Type().Field(index)
			if !field.IsExported() {
				continue
			}
			if count >= redactor.maxItems {
				result["_truncated"] = fmt.Sprintf("%d additional fields omitted", value.NumField()-count)
				break
			}
			fieldName := field.Name
			if jsonTag := field.Tag.Get("json"); jsonTag != "" {
				parts := strings.Split(jsonTag, ",")
				if parts[0] != "" && parts[0] != "-" {
					fieldName = parts[0]
				}
			}
			result[fieldName] = redactor.redactValue(value.Field(index), depth+1, fieldName, visited)
			count++
		}
		return result
	default:
		return fmt.Sprint(value.Interface())
	}
}

func (redactor *Redactor) isSensitive(key string) bool {
	if strings.TrimSpace(key) == "" {
		return false
	}
	for _, segment := range splitSegments(key) {
		if _, exists := redactor.sensitive[segment]; exists {
			return true
		}
	}
	return false
}

func splitSegments(value string) []string {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return nil
	}
	segments := make([]string, 0, 4)
	current := strings.Builder{}
	for index, runeValue := range normalized {
		if index > 0 && unicode.IsUpper(runeValue) && current.Len() > 0 {
			segments = append(segments, strings.ToLower(current.String()))
			current.Reset()
		}
		if unicode.IsLetter(runeValue) || unicode.IsDigit(runeValue) {
			current.WriteRune(runeValue)
			continue
		}
		if current.Len() > 0 {
			segments = append(segments, strings.ToLower(current.String()))
			current.Reset()
		}
	}
	if current.Len() > 0 {
		segments = append(segments, strings.ToLower(current.String()))
	}
	return segments
}
