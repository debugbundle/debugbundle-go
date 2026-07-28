package redaction

import (
	"strings"
	"testing"
)

type redactionFixture struct {
	Name     string `json:"name"`
	Password string `json:"password"`
	hidden   string
}

func TestRedactHandlesPrimitiveCompositeBoundsAndCycles(t *testing.T) {
	redactor := New([]string{"secret"})
	longText := strings.Repeat("x", 2050)
	cycle := map[string]any{}
	cycle["self"] = cycle
	values := map[string]any{
		"nil":     nil,
		"bool":    true,
		"int":     int32(-2),
		"uint":    uint64(3),
		"float":   1.5,
		"long":    longText,
		"array":   [2]string{"a", "b"},
		"struct":  redactionFixture{Name: "safe", Password: "visible only with custom fields", hidden: "skip"},
		"pointer": &redactionFixture{Name: "pointer", Password: "visible"},
		"secret":  "remove",
		"cycle":   cycle,
		"other":   make(chan int),
	}
	result := redactor.Redact(values).(map[string]any)
	if result["secret"] != redactedMarker {
		t.Fatalf("expected custom secret redaction, got %#v", result["secret"])
	}
	if !strings.HasSuffix(result["long"].(string), "...[truncated]") {
		t.Fatalf("expected bounded string, got %#v", result["long"])
	}
	if result["cycle"].(map[string]any)["self"] != "[Circular]" {
		t.Fatalf("expected cycle marker, got %#v", result["cycle"])
	}
	structValue := result["struct"].(map[string]any)
	if structValue["name"] != "safe" {
		t.Fatalf("expected exported tagged field, got %#v", structValue)
	}
}

func TestRedactBoundsDepthCollectionsAndMapEntries(t *testing.T) {
	redactor := New(nil)
	deep := map[string]any{"value": "leaf"}
	for index := 0; index < 8; index++ {
		deep = map[string]any{"child": deep}
	}
	items := make([]int, 55)
	entries := map[string]any{}
	for index := 0; index < 55; index++ {
		items[index] = index
		entries[string(rune('a'+index))] = index
	}
	result := redactor.Redact(map[string]any{
		"deep":    deep,
		"items":   items,
		"entries": entries,
	}).(map[string]any)
	if len(result["items"].([]any)) != 51 {
		t.Fatalf("expected 50 items plus truncation marker")
	}
	if _, exists := result["entries"].(map[string]any)["_truncated"]; !exists {
		t.Fatalf("expected map truncation marker")
	}
	if !strings.Contains(strings.ToLower(strings.Join(splitSegments("HTTP_accessToken.value"), ",")), "token") {
		t.Fatalf("expected segmented sensitive key normalization")
	}
	if redactor.Redact(deep) == nil {
		t.Fatalf("expected a bounded deep value")
	}
}
