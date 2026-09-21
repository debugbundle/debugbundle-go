package redaction

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

type panickingTelemetryMarshaler struct{}

func (panickingTelemetryMarshaler) MarshalJSON() ([]byte, error) {
	panic("application marshaler failed")
}

func TestPanickingApplicationMarshalerIsWithheld(t *testing.T) {
	if _, err := ProtectTelemetry(map[string]any{"message": panickingTelemetryMarshaler{}}, nil); !errors.Is(err, ErrUnsafeTelemetry) {
		t.Fatalf("application marshaler failure must withhold telemetry: %v", err)
	}
}

func TestPortablePrivacyConformance(t *testing.T) {
	body, err := os.ReadFile("../tests/fixtures/privacy-conformance.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Policy string `json:"policy"`
		Cases  []struct {
			ID       string `json:"id"`
			Input    any    `json:"input"`
			Expected any    `json:"expected"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(body, &corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.Policy != "telemetry-privacy-v1" {
		t.Fatal("privacy policy version drift")
	}
	for _, fixture := range corpus.Cases {
		t.Run(fixture.ID, func(t *testing.T) {
			result, err := ProtectTelemetry(fixture.Input, nil)
			if err != nil {
				t.Fatal(err)
			}
			actual, _ := json.Marshal(result)
			expected, _ := json.Marshal(fixture.Expected)
			if string(actual) != string(expected) {
				t.Fatalf("unexpected sanitation: %s, want %s", actual, expected)
			}
			second, err := ProtectTelemetry(result, nil)
			secondBytes, _ := json.Marshal(second)
			if err != nil || string(secondBytes) != string(actual) {
				t.Fatal("privacy pass is not idempotent")
			}
		})
	}
}

func TestMandatoryProtectionAndBudget(t *testing.T) {
	result, err := ProtectTelemetry(map[string]any{"password": "secret", "businessField": 1}, []string{"businessField"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result, map[string]any{"password": redactedMarker, "businessField": redactedMarker}) {
		t.Fatal("mandatory policy was replaced by custom fields")
	}
	tooMany := make([]any, 4097)
	if _, err := ProtectTelemetry(map[string]any{"many": tooMany}, nil); err != nil {
		// Oversized optional collections are masked and can remain a safe event.
		t.Fatal(err)
	}
}

func TestTelemetryAdversarialInputs(t *testing.T) {
	for _, fields := range [][]string{make([]string, 129), {""}, {strings.Repeat("x", 65)}} {
		if _, err := ProtectTelemetry(map[string]any{"safe": "ok"}, fields); !errors.Is(err, ErrUnsafeTelemetry) {
			t.Fatalf("invalid custom fields must fail closed: %v", err)
		}
	}
	for _, input := range []string{
		"-----BEGIN PRIVATE KEY----- incomplete",
		"password%3A%ZZ value",
		strings.Repeat("x", 16*1024) + " password=secret",
	} {
		result, err := ProtectTelemetry(map[string]any{"message": input}, nil)
		if err != nil {
			t.Fatal(err)
		}
		message := result.(map[string]any)["message"].(string)
		if strings.Contains(message, "secret") || strings.Contains(message, "incomplete") {
			t.Fatal("unsafe text was returned")
		}
	}
	result, err := ProtectTelemetry(map[string]any{"message": `{"accessToken":"secret","safe":true}`}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.(map[string]any)["message"].(string); got != `{"accessToken":"[REDACTED]","safe":true}` {
		t.Fatalf("structured string was not protected: %s", got)
	}
	result, err = ProtectTelemetry(map[string]any{"message": "customer_code=secret safe=ok"}, []string{"customer_code"})
	if err != nil || result.(map[string]any)["message"] != "customer_code=[REDACTED] safe=ok" {
		t.Fatalf("custom text labels must add to mandatory rules: %v, %v", result, err)
	}
	result, err = ProtectTelemetry(map[string]any{"message": "https://example.test/%ZZ?password=secret"}, nil)
	if err != nil || result.(map[string]any)["message"] != redactedMarker {
		t.Fatalf("malformed credential URL must be withheld: %v, %v", result, err)
	}
}

func TestProtocolIdentityValuesAreChecked(t *testing.T) {
	if !SafeEventIdentity(map[string]any{"correlation": map[string]any{"session_id": "session-123"}}, nil) {
		t.Fatal("safe identity lost")
	}
	if SafeEventIdentity(map[string]any{"correlation": map[string]any{"trace_id": "dbundle_proj_SYNTHETIC_SECRET"}}, nil) {
		t.Fatal("unsafe identity accepted")
	}
	if SafeEventIdentity(map[string]any{"sdk_version": "password=SYNTHETIC_SECRET"}, nil) {
		t.Fatal("unsafe metadata accepted")
	}
}

type callbackTelemetryMarshaler struct{ calls *int }

func (value callbackTelemetryMarshaler) MarshalJSON() ([]byte, error) {
	*value.calls++
	return []byte(`"safe"`), nil
}
func TestInputIsBoundedBeforeJSONEncoding(t *testing.T) {
	calls := 0
	if _, err := ProtectTelemetry(callbackTelemetryMarshaler{&calls}, nil); !errors.Is(err, ErrUnsafeTelemetry) || calls != 0 {
		t.Fatal("application serializer executed")
	}
	if _, err := ProtectTelemetry(strings.Repeat("x", maxTelemetryBytes+1), nil); err == nil {
		t.Fatal("unbounded input accepted")
	}
	if _, err := ProtectTelemetry(make([]any, 65537), nil); err == nil {
		t.Fatal("unbounded collection accepted")
	}
}

func TestBoundedInputRetainsPlainTypedValuesAndRejectsUnscannableGraphs(t *testing.T) {
	type evidence struct {
		Route    string  `json:"route"`
		Optional *string `json:"optional,omitempty"`
		Ignored  string  `json:"-"`
		hidden   string
	}
	route := "/checkout"
	for _, input := range []any{
		nil, &route, []int{1, 2}, [2]string{"safe", "route"},
		evidence{Route: route, Ignored: strings.Repeat("x", maxTelemetryBytes+1)},
		json.RawMessage(`{"password":"secret","route":"/checkout"}`),
	} {
		if _, err := ProtectTelemetry(input, nil); err != nil {
			t.Fatalf("plain value rejected: %T %v", input, err)
		}
	}
	circular := map[string]any{}
	circular["self"] = circular
	for _, input := range []any{
		circular,
		map[int]string{1: "unsafe map keys"},
		map[string]any{"nested": strings.Repeat("x", maxTelemetryBytes+1)},
		[]any{strings.Repeat("x", maxTelemetryBytes+1)},
		json.RawMessage(strings.Repeat(" ", maxTelemetryBytes+1)),
	} {
		if _, err := ProtectTelemetry(input, nil); err == nil {
			t.Fatalf("unscannable value accepted: %T", input)
		}
	}
	if SafeEventIdentity(map[string]any{"correlation": []any{}}, nil) {
		t.Fatal("invalid correlation accepted")
	}
	if SafeEventIdentity(map[string]any{"sdk_name": 42}, nil) {
		t.Fatal("invalid metadata accepted")
	}
	if SafeEventIdentity(map[string]any{"sdk_name": "safe"}, []string{""}) {
		t.Fatal("invalid options accepted")
	}
}
