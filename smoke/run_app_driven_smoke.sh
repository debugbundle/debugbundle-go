#!/bin/sh

set -eu

SOURCE="local"
VERSION=""
MODULE_PATH="github.com/debugbundle/debugbundle-go"
REPO_ROOT=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)

while [ "$#" -gt 0 ]; do
	case "$1" in
		--source)
			SOURCE="$2"
			shift 2
			;;
		--version)
			VERSION="$2"
			shift 2
			;;
		*)
			echo "unknown argument: $1" >&2
			exit 1
			;;
	esac
done

REQUIRE_VERSION="v0.0.0"
REPLACE_DIRECTIVE="replace ${MODULE_PATH} => ${REPO_ROOT}"

case "$SOURCE" in
	local)
		;;
	published)
		if [ -z "$VERSION" ]; then
			echo "--version is required when --source published is used" >&2
			exit 1
		fi
		case "$VERSION" in
			v*)
				REQUIRE_VERSION="$VERSION"
				;;
			*)
				REQUIRE_VERSION="v$VERSION"
				;;
		esac
		REPLACE_DIRECTIVE=""
		export GOPROXY="${GOPROXY:-https://proxy.golang.org,direct}"
		export GOSUMDB="${GOSUMDB:-sum.golang.org}"
		;;
	*)
		echo "unsupported --source value: $SOURCE" >&2
		exit 1
		;;
esac

TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT INT TERM

APP_DIR="$TMPDIR/app"
mkdir -p "$APP_DIR"

cat >"$APP_DIR/go.mod" <<EOF
module debugbundle-go-smoke

go 1.21

require ${MODULE_PATH} ${REQUIRE_VERSION}
${REPLACE_DIRECTIVE}
EOF

cat >"$APP_DIR/main.go" <<'EOF'
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	debugbundle "github.com/debugbundle/debugbundle-go"
	"github.com/debugbundle/debugbundle-go/debugbundlehttp"
	"github.com/debugbundle/debugbundle-go/relay"
)

type ingestionBatch struct {
	Authorization string
	Events        []map[string]any
}

func main() {
	const projectToken = "dbundle_proj_smoke"
	const backendTraceID = "trace-smoke-backend"
	const backendRequestID = "req-smoke-backend"
	const browserTraceID = "trace-smoke-browser"

	var (
		mu            sync.Mutex
		batches       []ingestionBatch
		handlerErrors []string
	)

	recordHandlerError := func(format string, args ...any) {
		mu.Lock()
		handlerErrors = append(handlerErrors, fmt.Sprintf(format, args...))
		mu.Unlock()
	}

	mockMux := http.NewServeMux()
	mockMux.HandleFunc("/v1/sdk/config", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			recordHandlerError("expected GET /v1/sdk/config, got %s", request.Method)
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if got := request.Header.Get("Authorization"); got != "Bearer "+projectToken {
			recordHandlerError("expected config auth header %q, got %q", "Bearer "+projectToken, got)
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"probes_enabled":true,"remote_probes_enabled":false,"active_probes":[],"poll_interval_ms":60000,"capture_policy":{"preset":"balanced","capture_logs":"warning","capture_request_events":"failures_only","capture_breadcrumbs":"exception_only","capture_probe_events":"buffer_only"}}`)
	})
	mockMux.HandleFunc("/v1/events", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			recordHandlerError("expected POST /v1/events, got %s", request.Method)
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var payload struct {
			Events []map[string]any `json:"events"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			recordHandlerError("decode ingestion payload: %v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		batches = append(batches, ingestionBatch{
			Authorization: request.Header.Get("Authorization"),
			Events:        payload.Events,
		})
		mu.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"accepted": len(payload.Events),
			"rejected": 0,
			"errors":   []any{},
		})
	})

	mockServer := httptest.NewServer(mockMux)
	defer mockServer.Close()

	client := debugbundle.New(debugbundle.Config{
		ProjectToken: projectToken,
		Service:      "smoke-api",
		Environment:  "test",
		Endpoint:     mockServer.URL + "/v1/events",
	})
	defer client.Close()

	appMux := http.NewServeMux()
	appMux.HandleFunc("/checkout", func(writer http.ResponseWriter, request *http.Request) {
		client.CaptureException(request.Context(), errors.New("go smoke backend exception"))
		http.Error(writer, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
	})
	appMux.Handle("/debugbundle/browser", debugbundlehttp.RelayHandler(client, relay.Options{}))

	appServer := httptest.NewServer(debugbundlehttp.Middleware(client, debugbundlehttp.Options{})(appMux))
	defer appServer.Close()

	request, err := http.NewRequest(http.MethodGet, appServer.URL+"/checkout", nil)
	if err != nil {
		failf("build backend request: %v", err)
	}
	request.Header.Set("X-DebugBundle-Trace-Id", backendTraceID)
	request.Header.Set("X-Request-Id", backendRequestID)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		failf("run backend request: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusInternalServerError {
		failf("expected backend response status %d, got %d", http.StatusInternalServerError, response.StatusCode)
	}

	relayPayload := map[string]any{
		"batch": []map[string]any{
			{
				"schema_version": "2026-03-01",
				"event_id":       "00000000-0000-4000-8000-000000000111",
				"event_type":     "frontend_exception",
				"occurred_at":    "2026-05-26T12:00:00Z",
				"sdk_name":       "spoofed-browser-sdk",
				"sdk_version":    "9.9.9",
				"project_token":  "browser-should-not-pass-through",
				"service": map[string]any{
					"name":        "smoke-web",
					"runtime":     "browser",
					"environment": "test",
				},
				"correlation": map[string]any{
					"trace_id":           browserTraceID,
					"request_id":         "browser-request-should-pass",
					"project_token":      "ignore-me",
					"organization_id":    "ignore-me",
					"unexpected_payload": "ignore-me",
				},
				"payload": map[string]any{
					"message":       "go smoke browser exception",
					"authorization": "Bearer should-be-stripped",
					"headers": map[string]any{
						"authorization": "Bearer should-be-stripped",
					},
				},
			},
		},
	}
	encodedRelayPayload, err := json.Marshal(relayPayload)
	if err != nil {
		failf("marshal relay payload: %v", err)
	}
	relayRequest, err := http.NewRequest(http.MethodPost, appServer.URL+"/debugbundle/browser", bytes.NewReader(encodedRelayPayload))
	if err != nil {
		failf("build relay request: %v", err)
	}
	relayRequest.Header.Set("Content-Type", "application/json")
	relayRequest.Header.Set("Origin", appServer.URL)
	relayResponse, err := http.DefaultClient.Do(relayRequest)
	if err != nil {
		failf("run relay request: %v", err)
	}
	_ = relayResponse.Body.Close()
	if relayResponse.StatusCode != http.StatusAccepted {
		failf("expected relay response status %d, got %d", http.StatusAccepted, relayResponse.StatusCode)
	}

	if err := client.Flush(context.Background()); err != nil {
		failf("flush client: %v", err)
	}

	if status := client.Status(); status != debugbundle.StatusHealthy {
		failf("expected healthy client status after smoke flush, got %s", status)
	}
	if client.LastEventAt() == nil {
		failf("expected last_event_at to be recorded after successful smoke flush")
	}

	mu.Lock()
	captured := append([]ingestionBatch(nil), batches...)
	mu.Unlock()

	if len(captured) < 2 {
		failf("expected at least two ingestion batches, got %d", len(captured))
	}
	if len(handlerErrors) > 0 {
		failf("mock ingestion assertions failed: %s", strings.Join(handlerErrors, "; "))
	}

	var (
		backendExceptionFound bool
		requestEventFound     bool
		frontendFound         bool
	)

	for _, batch := range captured {
		if batch.Authorization != "Bearer "+projectToken {
			failf("expected ingestion auth header %q, got %q", "Bearer "+projectToken, batch.Authorization)
		}
		for _, event := range batch.Events {
			eventType, _ := event["event_type"].(string)
			switch eventType {
			case "backend_exception":
				backendExceptionFound = true
				assertString(event, "sdk_name", "@debugbundle/sdk-go")
				assertString(event, "sdk_version", debugbundle.Version)
				assertService(event, "smoke-api", "test")
				assertCorrelation(event, backendTraceID, backendRequestID)
				assertProjectToken(event, projectToken)
			case "request_event":
				requestEventFound = true
				assertString(event, "sdk_name", "@debugbundle/sdk-go")
				assertService(event, "smoke-api", "test")
				assertCorrelation(event, backendTraceID, backendRequestID)
				payload, ok := event["payload"].(map[string]any)
				if !ok {
					failf("expected request_event payload to be an object")
				}
				if payload["response_status"] != float64(http.StatusInternalServerError) {
					failf("expected request_event response_status %d, got %#v", http.StatusInternalServerError, payload["response_status"])
				}
			case "frontend_exception":
				frontendFound = true
				assertString(event, "sdk_name", "@debugbundle/sdk-browser")
				assertString(event, "sdk_version", "9.9.9")
				assertService(event, "smoke-web", "test")
				assertCorrelation(event, browserTraceID, "browser-request-should-pass")
				assertNoSmuggledCorrelation(event, "project_token", "organization_id", "unexpected_payload")
				if _, ok := event["project_token"]; ok {
					failf("expected relay-forwarded browser event to omit project_token")
				}
				payload, ok := event["payload"].(map[string]any)
				if !ok {
					failf("expected frontend_exception payload to be an object")
				}
				if _, ok := payload["authorization"]; ok {
					failf("expected relay forwarding to strip top-level authorization from browser payload")
				}
				headers, _ := payload["headers"].(map[string]any)
				if _, ok := headers["authorization"]; ok {
					failf("expected relay forwarding to strip authorization from browser payload")
				}
			}
		}
	}

	if !backendExceptionFound {
		failf("expected backend_exception event in smoke ingestion batches")
	}
	if !requestEventFound {
		failf("expected request_event event in smoke ingestion batches")
	}
	if !frontendFound {
		failf("expected frontend_exception relay event in smoke ingestion batches")
	}

	fmt.Println("Go smoke passed: clean-install app emitted backend and relay events through the public SDK surface.")
}

func assertProjectToken(event map[string]any, expected string) {
	value, ok := event["project_token"].(string)
	if !ok || value != expected {
		failf("expected project_token %q, got %#v", expected, event["project_token"])
	}
}

func assertService(event map[string]any, expectedName string, expectedEnvironment string) {
	service, ok := event["service"].(map[string]any)
	if !ok {
		failf("expected service block in event %#v", event)
	}
	if name, _ := service["name"].(string); name != expectedName {
		failf("expected service.name %q, got %#v", expectedName, service["name"])
	}
	if environment, _ := service["environment"].(string); environment != expectedEnvironment {
		failf("expected service.environment %q, got %#v", expectedEnvironment, service["environment"])
	}
}

func assertCorrelation(event map[string]any, expectedTraceID string, expectedRequestID string) {
	correlation, ok := event["correlation"].(map[string]any)
	if !ok {
		failf("expected correlation block in event %#v", event)
	}
	if traceID, _ := correlation["trace_id"].(string); traceID != expectedTraceID {
		failf("expected correlation.trace_id %q, got %#v", expectedTraceID, correlation["trace_id"])
	}
	if expectedRequestID != "" {
		if requestID, _ := correlation["request_id"].(string); requestID != expectedRequestID {
			failf("expected correlation.request_id %q, got %#v", expectedRequestID, correlation["request_id"])
		}
	}
}

func assertString(event map[string]any, key string, expected string) {
	value, _ := event[key].(string)
	if value != expected {
		failf("expected %s %q, got %#v", key, expected, event[key])
	}
}

func assertNoSmuggledCorrelation(event map[string]any, keys ...string) {
	correlation, ok := event["correlation"].(map[string]any)
	if !ok {
		failf("expected correlation block in event %#v", event)
	}
	for _, key := range keys {
		if _, ok := correlation[key]; ok {
			failf("expected relay correlation to drop %q, got %#v", key, correlation[key])
		}
	}
}

func failf(format string, args ...any) {
	panic(fmt.Sprintf(format, args...))
}

func init() {
	if strings.TrimSpace(debugbundle.Version) == "" {
		failf("expected sdk version to be non-empty")
	}
}
EOF

cd "$APP_DIR"
go mod tidy
go run .
