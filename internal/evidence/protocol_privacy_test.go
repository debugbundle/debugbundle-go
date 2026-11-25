package evidence

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/debugbundle/debugbundle-go/v3/redaction"
)

func TestResourceObservationProtocolRejectsInvalidEvidence(t *testing.T) {
	for _, items := range []any{nil, []any{}, []any{"not an observation"}} {
		observations := map[string]any{"version": 1, "items": items}
		if IsValidProtocolPayloadField("frontend_exception", "resource_observations", observations) {
			t.Fatal("malformed observation list qualified for protocol field preservation")
		}
	}

	cases := []struct {
		name  string
		field string
		value any
	}{
		{"missing observation kind", "kind", nil},
		{"unknown observation kind", "kind", "unknown"},
		{"invalid observation timestamp", "observed_at", "yesterday"},
		{"unsafe observation URL", "source_url", "javascript:alert(1)"},
		{"invalid observation availability", "availability", "pending"},
		{"invalid HTTP response status", "response_status", 600},
		{"negative transfer size", "transfer_size_bytes", -1},
		{"negative duration", "duration_ms", -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item := map[string]any{
				"kind": "timing", "observed_at": "2026-05-23T12:00:00Z",
				"source_url": "https://cdn.example.test/asset.js", "availability": "observed",
				"response_status": 200, "duration_ms": 15, "transfer_size_bytes": 10,
			}
			item[tc.field] = tc.value
			observations := map[string]any{"version": 1, "items": []any{item}}
			if IsValidProtocolPayloadField("frontend_exception", "resource_observations", observations) {
				t.Fatal("invalid observation qualified for protocol field preservation")
			}
		})
	}
}

func TestProtectEventFieldsPreservesFrontendProtocolStructuresAndScrubsFreeText(t *testing.T) {
	options := redaction.TelemetryOptions{
		AdditionalFields: []string{"tag", "reference", "reason", "file"},
	}
	payload := map[string]any{
		"name":    "TypeError",
		"message": "failed tag=SYNTHETIC_MSG",
		"stack":   "at run (https://alice:secret@app.example.test/app.js?token=SYNTHETIC:10:2)",
		"browser": map[string]any{"name": "Chrome", "version": "126"},
		"browser_event": map[string]any{
			"kind":          "resource_error",
			"message":       "load failed reason=SYNTHETIC_BE",
			"file_name":     "https://cdn.example.test/app.js?tag=SYNTHETIC_FILE&ok=1",
			"line_number":   12,
			"column_number": 4,
			"opaque":        false,
			"target": map[string]any{
				"tag_name":   "SCRIPT",
				"source_url": "https://cdn.example.test/app.js?tag=SYNTHETIC_SRC&ok=1",
				"attributes": map[string]any{
					"rel":               "preload reference=SYNTHETIC_REL",
					"as":                "script",
					"type":              "module",
					"media":             "all",
					"cross_origin":      "anonymous",
					"async":             true,
					"defer":             false,
					"integrity_present": true,
				},
			},
			"page": map[string]any{
				"url":              "https://app.example.test/pay?reference=SYNTHETIC_PAGE&ok=1",
				"referrer":         "https://app.example.test/cart?reason=SYNTHETIC_REF",
				"ready_state":      "complete",
				"visibility_state": "visible",
			},
		},
		"rejection_reason": map[string]any{
			"kind":    "error",
			"name":    "PaymentError reason=SYNTHETIC_REJ_NAME",
			"message": "rejected tag=SYNTHETIC_REJ_MSG",
			"preview": "preview file=SYNTHETIC_REJ_PREV",
		},
		"resource_observations": map[string]any{
			"version": 1,
			"items": []any{
				map[string]any{
					"kind":                "timing",
					"observed_at":         "2026-05-23T12:00:00Z",
					"source_url":          "https://cdn.example.test/tag=SYNTHETIC_TIMING/app.js",
					"availability":        "observed",
					"response_status":     404,
					"duration_ms":         12.5,
					"transfer_size_bytes": 0,
				},
				map[string]any{
					"kind":                 "csp",
					"observed_at":          "2026-05-23T12:00:01Z",
					"source_url":           "/assets/app.js",
					"effective_directive":  "script-src",
					"disposition":          "enforce",
					"document_status_code": 200,
				},
			},
		},
		"exception_evidence": map[string]any{
			"version":            1,
			"runtime":            "browser",
			"root_id":            0,
			"truncated":          false,
			"unavailable_reason": nil,
			"nodes": []any{
				map[string]any{
					"id":             0,
					"name":           "TypeError",
					"message":        "failed reference=SYNTHETIC_NODE_MSG",
					"stack":          "at submit (https://user:pass@app.example.test/chunk.js?token=SECRET:42:7)\nat bad (https://user:pass@host:bad/x.js:1:1)",
					"trace_origin":   "throw",
					"parsing_status": "parsed",
					"frames": []any{
						map[string]any{
							"function":       "submit reason=SYNTHETIC_FN",
							"file":           "https://user:pass@app.example.test/chunk.js?tag=SYNTHETIC_FRAME",
							"line":           42,
							"column":         7,
							"classification": "application",
						},
					},
					"relationships": []any{},
				},
			},
		},
	}
	contextMap := map[string]any{
		"evidence_reference": map[string]any{
			"version":     1,
			"failure_ref": "11111111-1111-4111-8111-111111111111",
			"scope":       map[string]any{"kind": "page", "page_id": "22222222-2222-4222-8222-222222222222"},
		},
		"evidence_limits": map[string]any{
			"version":               1,
			"resource_observations": "budget_exceeded",
		},
		"payout": map[string]any{
			"tag":       "SYNTHETIC_TAG",
			"reference": "SYNTHETIC_REF",
			"reason":    "SYNTHETIC_REASON",
			"file":      "SYNTHETIC_FILE",
		},
	}
	service := map[string]any{"name": "checkout-web", "environment": "production"}

	outPayload, outService, outContext, ok := ProtectEventFields("frontend_exception", payload, service, contextMap, options)
	if !ok {
		t.Fatal("expected valid frontend protocol structures to survive broad redaction")
	}
	if outService["name"] != "checkout-web" {
		t.Fatalf("unexpected service: %#v", outService)
	}
	browserEvent := outPayload["browser_event"].(map[string]any)
	target := browserEvent["target"].(map[string]any)
	if target["tag_name"] != "SCRIPT" {
		t.Fatalf("expected allowlisted HTML tag_name to be preserved, got %#v", target["tag_name"])
	}
	if target["source_url"] != "https://cdn.example.test/app.js?tag=%5BREDACTED%5D&ok=1" {
		t.Fatalf("expected source_url query to be scrubbed, got %#v", target["source_url"])
	}
	if browserEvent["page"].(map[string]any)["ready_state"] != "complete" {
		t.Fatalf("expected page ready_state preserved, got %#v", browserEvent["page"])
	}
	rejection := outPayload["rejection_reason"].(map[string]any)
	if rejection["kind"] != "error" || rejection["message"] != "rejected tag=[REDACTED]" {
		t.Fatalf("unexpected rejection_reason: %#v", rejection)
	}
	graphOut := outPayload["exception_evidence"].(map[string]any)
	rootNode := graphOut["nodes"].([]any)[0].(map[string]any)
	if !strings.Contains(rootNode["stack"].(string), "https://app.example.test/chunk.js:42:7)") ||
		!strings.Contains(rootNode["stack"].(string), "[unavailable-url]:1:1)") {
		t.Fatalf("expected browser stack URLs to be sanitized, got %q", rootNode["stack"])
	}
	frame := rootNode["frames"].([]any)[0].(map[string]any)
	if frame["file"] != "https://app.example.test/chunk.js" || frame["classification"] != "application" {
		t.Fatalf("unexpected frame: %#v", frame)
	}
	if outContext["evidence_reference"].(map[string]any)["failure_ref"] != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("evidence_reference lost: %#v", outContext["evidence_reference"])
	}
	if outContext["evidence_limits"].(map[string]any)["resource_observations"] != "budget_exceeded" {
		t.Fatalf("evidence_limits lost: %#v", outContext["evidence_limits"])
	}
	payout := outContext["payout"].(map[string]any)
	for _, field := range []string{"tag", "reference", "reason", "file"} {
		if payout[field] != "[REDACTED]" {
			t.Fatalf("expected payout.%s redacted, got %#v", field, payout[field])
		}
	}
	encoded, _ := json.Marshal(map[string]any{"payload": outPayload, "context": outContext})
	if strings.Contains(string(encoded), "SYNTHETIC_") || strings.Contains(string(encoded), "user:pass") {
		t.Fatalf("secret leaked into protected output: %s", encoded)
	}
}

func TestProtectEventFieldsNonAllowlistedTagDiagnosticExecutionAndBudget(t *testing.T) {
	// Non-allowlisted HTML tag_name is redacted under broad "tag" and kept under exact "tag".
	makeBrowserEvent := func(tagName string) map[string]any {
		return map[string]any{
			"browser_event": map[string]any{
				"kind": "resource_error", "message": nil, "file_name": nil,
				"line_number": nil, "column_number": nil, "opaque": false,
				"target": map[string]any{"tag_name": tagName, "source_url": nil},
			},
		}
	}
	service := map[string]any{"name": "web", "environment": "prod"}
	broadOut, _, _, ok := ProtectEventFields("frontend_exception", makeBrowserEvent("custom-widget"), service, nil, redaction.TelemetryOptions{
		AdditionalFields: []string{"tag"},
	})
	if !ok || broadOut["browser_event"].(map[string]any)["target"].(map[string]any)["tag_name"] != "[REDACTED]" {
		t.Fatalf("non-allowlisted tag_name must be redacted under broad tag: %#v", broadOut)
	}
	exactOut, _, _, ok := ProtectEventFields("frontend_exception", makeBrowserEvent("custom-widget"), service, nil, redaction.TelemetryOptions{
		ExactFields: []string{"tag"},
	})
	if !ok || exactOut["browser_event"].(map[string]any)["target"].(map[string]any)["tag_name"] != "custom-widget" {
		t.Fatalf("non-allowlisted tag_name should remain under exact tag: %#v", exactOut)
	}

	// Execution and error_suppressed queue_pressure preservation.
	execOut, _, _, ok := ProtectEventFields("backend_exception", map[string]any{
		"execution": map[string]any{
			"version": 1, "kind": "job", "name": "payout reason=SYNTHETIC_EXEC",
			"elapsed_ms": 15.2, "limit_ms": 1000,
		},
	}, service, nil, redaction.TelemetryOptions{AdditionalFields: []string{"reason", "name"}})
	if !ok || execOut["execution"].(map[string]any)["name"] != "payout reason=[REDACTED]" {
		t.Fatalf("unexpected execution output: %#v", execOut)
	}

	suppOut, _, _, ok := ProtectEventFields("error_suppressed", map[string]any{
		"fingerprint": "sdk:queue-pressure", "reason": "queue_pressure",
	}, service, nil, redaction.TelemetryOptions{AdditionalFields: []string{"reason"}})
	if !ok || suppOut["reason"] != "queue_pressure" {
		t.Fatalf("queue_pressure reason lost: %#v", suppOut)
	}

	// Diagnostic context recovery + resource_observation preservation and invalid rejection.
	recoveryPayload := map[string]any{
		"version":     1,
		"kind":        "recovery",
		"failure_ref": "11111111-1111-4111-8111-111111111111",
		"scope":       map[string]any{"kind": "operation", "operation_id": "22222222-2222-4222-8222-222222222222"},
		"attempt_id":  "33333333-3333-4333-8333-333333333333",
		"state":       "recovery_observed",
		"operation":   "retry reason=SYNTHETIC_OP",
		"status_code": 200,
		"duration_ms": 12.5,
	}
	if !IsValidDiagnosticPayload(recoveryPayload) {
		t.Fatal("expected valid recovery diagnostic payload")
	}
	recOut, _, _, ok := ProtectEventFields("diagnostic_context", recoveryPayload, service, nil, redaction.TelemetryOptions{
		AdditionalFields: []string{"reason", "reference", "state", "operation"},
	})
	if !ok || recOut["state"] != "recovery_observed" || recOut["operation"] != "retry reason=[REDACTED]" {
		t.Fatalf("unexpected diagnostic recovery output: %#v", recOut)
	}

	resObsDiagnostic := map[string]any{
		"version":         1,
		"kind":            "resource_observation",
		"failure_ref":     "11111111-1111-4111-8111-111111111111",
		"scope":           map[string]any{"kind": "page", "page_id": "22222222-2222-4222-8222-222222222222"},
		"source_event_id": "44444444-4444-4444-8444-444444444444",
		"observations": map[string]any{
			"version": 1,
			"items": []any{
				map[string]any{
					"kind": "timing", "observed_at": "2026-05-23T12:00:00Z", "source_url": "https://cdn.example.test/a.js",
					"availability": "observed", "response_status": 200, "duration_ms": 5, "transfer_size_bytes": 100,
				},
			},
		},
	}
	resOut, _, _, ok := ProtectEventFields("diagnostic_context", resObsDiagnostic, service, nil, redaction.TelemetryOptions{
		AdditionalFields: []string{"reference", "observations", "source"},
	})
	if !ok || resOut["source_event_id"] != "44444444-4444-4444-8444-444444444444" {
		t.Fatalf("unexpected resource_observation diagnostic output: %#v", resOut)
	}
	if _, _, _, ok := ProtectEventFields("diagnostic_context", map[string]any{"kind": "unknown"}, service, nil, redaction.TelemetryOptions{}); ok {
		t.Fatal("invalid diagnostic_context must fail closed")
	}

	// Pre-redactor protocol field helpers.
	if !IsValidProtocolContextField("evidence_reference", map[string]any{
		"version": 1, "failure_ref": "11111111-1111-4111-8111-111111111111",
		"scope": map[string]any{"kind": "operation", "operation_id": "22222222-2222-4222-8222-222222222222"},
	}) {
		t.Fatal("expected valid evidence_reference")
	}
	if !IsValidProtocolContextField("evidence_limits", map[string]any{"version": 1, "execution": "unsafe"}) {
		t.Fatal("expected valid evidence_limits")
	}
	if IsValidProtocolContextField("evidence_limits", map[string]any{"version": 1, "execution": "bogus"}) ||
		IsValidProtocolContextField("payout", map[string]any{"tag": "x"}) {
		t.Fatal("invalid context field accepted as protocol field")
	}
	if !IsValidProtocolPayloadField("error_suppressed", "reason", "queue_pressure") ||
		IsValidProtocolPayloadField("error_suppressed", "reason", "other") ||
		IsValidProtocolPayloadField("log_event", "other", "x") {
		t.Fatal("unexpected payload protocol check result")
	}
	if !IsValidProtocolPayloadField("frontend_exception", "browser_event", makeBrowserEvent("script")["browser_event"]) ||
		!IsValidProtocolPayloadField("frontend_exception", "rejection_reason", map[string]any{"kind": "string", "preview": "x"}) ||
		!IsValidProtocolPayloadField("frontend_exception", "resource_observations", resObsDiagnostic["observations"]) ||
		!IsValidProtocolPayloadField("backend_exception", "execution", map[string]any{"version": 1, "kind": "request", "name": nil, "elapsed_ms": 1, "limit_ms": nil}) {
		t.Fatal("valid protocol payload field rejected")
	}

	// Invalid extra key in browser_event or rejection_reason falls through to normal redaction (not preserved).
	badBrowser := makeBrowserEvent("script")["browser_event"].(map[string]any)
	badBrowser["extra"] = "SYNTHETIC_EXTRA"
	badOut, _, _, ok := ProtectEventFields("frontend_exception", map[string]any{
		"browser_event":    badBrowser,
		"rejection_reason": map[string]any{"kind": "error", "extra": "SYNTHETIC_BAD"},
	}, service, nil, redaction.TelemetryOptions{AdditionalFields: []string{"tag", "reason"}})
	if !ok {
		t.Fatal("fallback redaction for invalid optional blocks failed")
	}
	if badOut["browser_event"].(map[string]any)["target"].(map[string]any)["tag_name"] != "[REDACTED]" {
		t.Fatalf("invalid browser_event must not be exempted from redaction: %#v", badOut["browser_event"])
	}
	if badOut["rejection_reason"] != "[REDACTED]" {
		t.Fatalf("invalid rejection_reason must not be exempted from redaction: %#v", badOut["rejection_reason"])
	}

	// Combined assembled size > 256 KiB fails closed even when non-preserved fields alone fit.
	largeContext := map[string]any{}
	for index := 0; index < 16; index++ {
		largeContext["k"+string(rune('a'+index))] = strings.Repeat("v", 15*1024)
	}
	if _, _, _, ok := ProtectEventFields("backend_exception", map[string]any{
		"exception_evidence": map[string]any{
			"version": 1,
			"runtime": "go",
			"root_id": 0,
			"nodes": []any{
				map[string]any{
					"id":             0,
					"name":           "Error",
					"message":        "boom",
					"stack":          strings.Repeat("s", 12*1024),
					"trace_origin":   "capture",
					"parsing_status": "parsed",
					"frames":         []any{},
					"relationships":  []any{},
				},
			},
			"truncated":          false,
			"unavailable_reason": nil,
		},
		"message": strings.Repeat("m", 10*1024),
	}, service, largeContext, redaction.TelemetryOptions{}); ok {
		t.Fatal("oversized assembled event must fail closed")
	}
}
