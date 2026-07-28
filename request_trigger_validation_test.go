package debugbundle

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/url"
	"testing"
	"time"
)

func signedTriggerToken(payload, key string) string {
	segment := base64.RawURLEncoding.EncodeToString([]byte(payload))
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(segment))
	return triggerTokenPrefix + segment + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestRequestTriggerValidationRejectsMalformedInputs(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	validPayload := `{"activation_id":"activation-1","label_pattern":"orders.*","service":"orders","environment":"test","trigger_expires_at":"2031-01-01T00:00:00Z"}`
	validToken := signedTriggerToken(validPayload, "secret")
	request := &http.Request{URL: &url.URL{}, Header: http.Header{}}
	request.Header.Set(triggerTokenHeader, validToken)
	if directives := resolveRequestTriggerDirectives(request, "secret", now); len(directives) != 1 || directives[0].ID != "activation-1" {
		t.Fatalf("expected valid trigger directive, got %#v", directives)
	}
	request.Header.Del(triggerTokenHeader)
	request.URL.RawQuery = "_debug_probe=" + url.QueryEscape(validToken)
	if extractRequestTriggerToken(request) != validToken {
		t.Fatal("expected query trigger fallback")
	}

	cases := []struct {
		name  string
		token string
		key   string
	}{
		{name: "missing key", token: validToken},
		{name: "wrong prefix", token: "other"},
		{name: "missing separator", token: triggerTokenPrefix + "payload", key: "secret"},
		{name: "empty payload", token: triggerTokenPrefix + ".signature", key: "secret"},
		{name: "empty signature", token: triggerTokenPrefix + "payload.", key: "secret"},
		{name: "bad base64 signature", token: triggerTokenPrefix + "payload.!", key: "secret"},
		{name: "wrong signature", token: signedTriggerToken(validPayload, "other"), key: "secret"},
		{name: "bad payload base64", token: signedTriggerToken("not-json", "secret"), key: "secret"},
		{name: "missing fields", token: signedTriggerToken(`{"activation_id":"one"}`, "secret"), key: "secret"},
		{name: "bad expiry", token: signedTriggerToken(`{"activation_id":"one","label_pattern":"*","service":"orders","environment":"test","trigger_expires_at":"bad"}`, "secret"), key: "secret"},
		{name: "expired", token: signedTriggerToken(`{"activation_id":"one","label_pattern":"*","service":"orders","environment":"test","trigger_expires_at":"2029-01-01T00:00:00Z"}`, "secret"), key: "secret"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			candidate := &http.Request{URL: &url.URL{}, Header: http.Header{}}
			candidate.Header.Set(triggerTokenHeader, testCase.token)
			if directives := resolveRequestTriggerDirectives(candidate, testCase.key, now); len(directives) != 0 {
				t.Fatalf("expected malformed trigger rejection, got %#v", directives)
			}
		})
	}
	if directives := resolveRequestTriggerDirectives(nil, "secret", now); directives != nil {
		t.Fatal("expected nil request to degrade safely")
	}
	//nolint:staticcheck // Nil-context handling is an intentional SDK safety guarantee.
	if extractRequestTriggerToken(nil) != "" || requestProbeDirectivesFromContext(nil) != nil {
		t.Fatal("expected nil request context helpers to degrade safely")
	}
}
