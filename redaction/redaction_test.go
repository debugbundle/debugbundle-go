package redaction

import "testing"

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
