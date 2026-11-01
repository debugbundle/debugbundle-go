package evidence

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

type serializerFixture struct{ calls *int }

func (value serializerFixture) MarshalJSON() ([]byte, error) {
	*value.calls++
	return []byte(`{}`), nil
}
func TestOwnedJSONCompatibilityAndIndependentRetention(t *testing.T) {
	type plain struct {
		Visible string `json:"renamed"`
		Hidden  string `json:"-"`
	}
	input := map[string]any{"dto": plain{Visible: "fixture", Hidden: "private"}, "time": time.Unix(0, 0).UTC(), "raw": json.RawMessage(`{"number":1}`), "array": [2]int{1, 2}, "bool": true, "nil": nil, "unsigned": uint(2)}
	copy, err := CopyJSON(input)
	if err != nil {
		t.Fatal(err)
	}
	input["bool"] = false
	if copy["bool"] != true || copy["dto"].(map[string]any)["renamed"] != "fixture" {
		t.Fatal("owned clone lost compatibility or shares input")
	}
	if _, leaked := copy["dto"].(map[string]any)["Hidden"]; leaked {
		t.Fatal("ignored field retained")
	}
}
func TestOwnedJSONRejectsForeignSerializersAndSeparatesBudgetFromInvalid(t *testing.T) {
	calls := 0
	if _, err := CopyJSON(map[string]any{"foreign": serializerFixture{&calls}}); !errors.Is(err, ErrUnavailable) || calls != 0 {
		t.Fatal("foreign serializer executed")
	}
	cycle := map[string]any{}
	cycle["cycle"] = cycle
	deep := any("end")
	for index := 0; index < 20; index++ {
		deep = map[string]any{"nested": deep}
	}
	for _, input := range []any{cycle, map[string]any{"deep": deep}, map[string]any{"many": make([]int, 5000)}, map[string]any{"text": strings.Repeat("x", 8*1024*1024)}, map[string]any{"escaped": strings.Repeat("\n", 4*1024*1024)}} {
		if _, err := CopyJSON(input); !errors.Is(err, ErrBudget) {
			t.Fatalf("quota classified as invalid: %v", err)
		}
	}
	for _, input := range []any{nil, []any{}, map[int]string{1: "invalid"}, map[string]any{"function": func() {}}, map[string]any{"nan": math.NaN()}, map[string]any{"raw": json.RawMessage(`{invalid`)}} {
		if _, err := CopyJSON(input); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid JSON not classified: %v", err)
		}
	}
}
