package redaction

import "testing"

type hostileMapKey struct{}

func (hostileMapKey) String() string {
	panic("application string renderer must not run")
}

func TestRedactionNeverInvokesApplicationMapKeyRenderer(t *testing.T) {
	redacted := New(nil).Redact(map[any]any{
		hostileMapKey{}: "private",
		"safe":          "retained",
	})
	fields := redacted.(map[string]any)
	if fields["safe"] != "retained" || len(fields) != 1 {
		t.Fatalf("unsupported map key leaked into redacted fields: %#v", fields)
	}
}

func TestRedactSegmentAwareSensitiveKeys(t *testing.T) {
	redactor := New(nil)
	value := redactor.Redact(map[string]any{
		"userPassword": "secret",
		"profile": map[string]any{
			"access_token": "token-value",
			"safe":         "ok",
		},
	})
	cast := value.(map[string]any)
	if cast["userPassword"] != "[REDACTED]" {
		t.Fatalf("expected userPassword to be redacted, got %#v", cast["userPassword"])
	}
	profile := cast["profile"].(map[string]any)
	if profile["access_token"] != "[REDACTED]" {
		t.Fatalf("expected access token to be redacted, got %#v", profile["access_token"])
	}
	if profile["safe"] != "ok" {
		t.Fatalf("expected safe value to be preserved, got %#v", profile["safe"])
	}
}

func TestRepeatedNativeCollectionsAreNotMisreportedAsCycles(t *testing.T) {
	shared := map[string]any{"password": "synthetic-private"}
	empty := []any{}
	input := map[string]any{"first": shared, "second": shared, "frames": empty, "relationships": empty}
	output := New(nil).Redact(input).(map[string]any)
	for _, key := range []string{"first", "second"} {
		fields, ok := output[key].(map[string]any)
		if !ok || fields["password"] != "[REDACTED]" {
			t.Fatal("repeated data lost or skipped privacy")
		}
	}
	for _, key := range []string{"frames", "relationships"} {
		if _, ok := output[key].([]any); !ok {
			t.Fatal("empty native array misreported as a cycle")
		}
	}
	cycle := map[string]any{}
	cycle["self"] = cycle
	if New(nil).Redact(cycle).(map[string]any)["self"] != "[Circular]" {
		t.Fatal("actual cycle no longer bounded")
	}
}
