package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	debugbundle "github.com/debugbundle/debugbundle-go"
)

type relayComplianceFixtureSuite struct {
	Version int                     `json:"version"`
	Cases   []relayComplianceCase   `json:"cases"`
}

type relayComplianceCase struct {
	ID                    string                               `json:"id"`
	Kind                  string                               `json:"kind"`
	RelayOptions          relayComplianceOptions               `json:"relayOptions"`
	Request               relayComplianceRequest               `json:"request"`
	Requests              []relayComplianceSequenceRequest     `json:"requests"`
	Expected              relayComplianceExpected              `json:"expected"`
	ExpectedEventFile     []map[string]any                     `json:"expectedEventFile"`
	ExpectedForwardRequest *relayComplianceForwardRequest      `json:"expectedForwardRequest"`
	ExpectedDeliveredMarker bool                               `json:"expectedDeliveredMarker"`
}

type relayComplianceOptions struct {
	ProjectMode        string `json:"projectMode"`
	ProjectToken       string `json:"projectToken"`
	DurableWrite       *bool  `json:"durableWrite"`
	Endpoint           string `json:"endpoint"`
	Service            string `json:"service"`
	Environment        string `json:"environment"`
	RateLimitPerMinute int    `json:"rateLimitPerMinute"`
}

type relayComplianceRequest struct {
	Method        string                        `json:"method"`
	Headers       map[string]string             `json:"headers"`
	IPAddress     string                        `json:"ipAddress"`
	BodyJSON      any                           `json:"bodyJson"`
	BodyText      string                        `json:"bodyText"`
	BodyGenerator *relayComplianceBodyGenerator `json:"bodyGenerator"`
}

type relayComplianceBodyGenerator struct {
	Kind   string `json:"kind"`
	Char   string `json:"char"`
	Length int    `json:"length"`
}

type relayComplianceSequenceRequest struct {
	AtMilliseconds int                    `json:"atMs"`
	Request        relayComplianceRequest `json:"request"`
	ExpectedStatus int                    `json:"expectedStatus"`
}

type relayComplianceExpected struct {
	Status   int      `json:"status"`
	Accepted int      `json:"accepted"`
	Rejected int      `json:"rejected"`
	Errors   []string `json:"errors"`
}

type relayComplianceForwardRequest struct {
	Events []map[string]any `json:"events"`
}

func TestRelayComplianceHandlerFixtures(t *testing.T) {
	for _, fixtureID := range []string{"valid-browser-batch", "mixed-valid-invalid-batch", "credential-smuggling-payload", "wrong-origin-request", "missing-origin-request", "oversized-body"} {
		fixture := relayComplianceFixture(t, fixtureID)
		t.Run(fixture.ID, func(t *testing.T) {
			options := relayOptionsFromFixture(fixture.RelayOptions)
			eventsDir := ""
			if len(fixture.ExpectedEventFile) > 0 {
				options.ProjectMode = debugbundle.ProjectModeLocalOnly
				eventsDir = t.TempDir()
				options.LocalEventsDir = eventsDir
			}
			handler := NewHandler(nil, options)
			request := relayRequestFromFixture(t, fixture.Request)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			assertRelayFixtureResponse(t, response, fixture.Expected)
			if len(fixture.ExpectedEventFile) > 0 {
				written := readRelayBatchFile(t, eventsDir)
				if !reflect.DeepEqual(written, fixture.ExpectedEventFile) {
					t.Fatalf("expected relay event file %#v, got %#v", fixture.ExpectedEventFile, written)
				}
			}
		})
	}
}

func TestRelayComplianceRateLimitSequence(t *testing.T) {
	fixture := relayComplianceFixture(t, "rate-limit-sequence")
	relayHandler := NewHandler(nil, relayOptionsFromFixture(fixture.RelayOptions)).(*handler)
	baseTime := time.Date(2026, time.May, 19, 10, 0, 0, 0, time.UTC)
	currentTime := baseTime
	relayHandler.now = func() time.Time {
		return currentTime
	}
	for _, step := range fixture.Requests {
		currentTime = baseTime.Add(time.Duration(step.AtMilliseconds) * time.Millisecond)
		response := httptest.NewRecorder()
		relayHandler.ServeHTTP(response, relayRequestFromFixture(t, step.Request))
		if response.Code != step.ExpectedStatus {
			t.Fatalf("expected status %d at %dms, got %d body=%s", step.ExpectedStatus, step.AtMilliseconds, response.Code, response.Body.String())
		}
	}
}

func TestRelayComplianceLocalOnlyWrite(t *testing.T) {
	fixture := relayComplianceFixture(t, "local-only-write")
	eventsDir := t.TempDir()
	options := relayOptionsFromFixture(fixture.RelayOptions)
	options.LocalEventsDir = eventsDir
	handler := NewHandler(nil, options)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, relayRequestFromFixture(t, fixture.Request))
	assertRelayFixtureResponse(t, response, fixture.Expected)
	written := readRelayBatchFile(t, eventsDir)
	if !reflect.DeepEqual(written, fixture.ExpectedEventFile) {
		t.Fatalf("expected relay event file %#v, got %#v", fixture.ExpectedEventFile, written)
	}
}

func TestRelayComplianceConnectedDurableSpool(t *testing.T) {
	fixture := relayComplianceFixture(t, "connected-durable-spool")
	spoolDir := t.TempDir()
	forwarder := &recordingTransport{}
	options := relayOptionsFromFixture(fixture.RelayOptions)
	options.SpoolDir = spoolDir
	options.Transport = forwarder
	handler := NewHandler(nil, options)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, relayRequestFromFixture(t, fixture.Request))
	assertRelayFixtureResponse(t, response, fixture.Expected)
	written := readRelayBatchFile(t, spoolDir)
	if !reflect.DeepEqual(written, fixture.ExpectedEventFile) {
		t.Fatalf("expected spool file %#v, got %#v", fixture.ExpectedEventFile, written)
	}
	if fixture.ExpectedDeliveredMarker {
		spoolFiles, err := filepath.Glob(filepath.Join(spoolDir, "*.events.json"))
		if err != nil {
			t.Fatalf("glob spool files: %v", err)
		}
		if len(spoolFiles) != 1 {
			t.Fatalf("expected one spool file, got %d", len(spoolFiles))
		}
		if _, err := os.Stat(spoolFiles[0] + relayDeliveredMarkerSuffix); err != nil {
			t.Fatalf("expected delivered marker file: %v", err)
		}
	}
	if len(forwarder.requests) != 1 {
		t.Fatalf("expected one forward request, got %#v", forwarder.requests)
	}
	if forwarder.requests[0].ProjectToken != fixture.RelayOptions.ProjectToken {
		t.Fatalf("expected server-side project token %q, got %#v", fixture.RelayOptions.ProjectToken, forwarder.requests[0].ProjectToken)
	}
}

func TestRelayComplianceConnectedCloudForwarding(t *testing.T) {
	fixture := relayComplianceFixture(t, "connected-cloud-forwarding")
	forwarder := &recordingTransport{}
	options := relayOptionsFromFixture(fixture.RelayOptions)
	options.Transport = forwarder
	handler := NewHandler(nil, options)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, relayRequestFromFixture(t, fixture.Request))
	assertRelayFixtureResponse(t, response, fixture.Expected)
	if len(forwarder.requests) != 1 {
		t.Fatalf("expected one forward request, got %#v", forwarder.requests)
	}
	if fixture.ExpectedForwardRequest == nil || len(fixture.ExpectedForwardRequest.Events) != 1 {
		t.Fatalf("expected one forward fixture event, got %#v", fixture.ExpectedForwardRequest)
	}
	expectedForwardedEvent := cloneRelayJSONMapSlice(t, fixture.ExpectedForwardRequest.Events)
	expectedProjectToken, _ := expectedForwardedEvent[0]["project_token"].(string)
	delete(expectedForwardedEvent[0], "project_token")
	if forwarder.requests[0].ProjectToken != expectedProjectToken {
		t.Fatalf("expected forward request project token %q, got %#v", expectedProjectToken, forwarder.requests[0].ProjectToken)
	}
	var forwardedEvent map[string]any
	if err := json.Unmarshal(forwarder.requests[0].Events[0], &forwardedEvent); err != nil {
		t.Fatalf("unmarshal forwarded event: %v", err)
	}
	if !reflect.DeepEqual([]map[string]any{forwardedEvent}, expectedForwardedEvent) {
		t.Fatalf("expected forwarded events %#v, got %#v", expectedForwardedEvent, []map[string]any{forwardedEvent})
	}
}

func relayComplianceFixture(t *testing.T, fixtureID string) relayComplianceCase {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", "relay-compliance.json"))
	if err != nil {
		t.Fatalf("read relay compliance fixtures: %v", err)
	}
	var suite relayComplianceFixtureSuite
	if err := json.Unmarshal(content, &suite); err != nil {
		t.Fatalf("unmarshal relay compliance fixtures: %v", err)
	}
	for _, fixture := range suite.Cases {
		if fixture.ID == fixtureID {
			return fixture
		}
	}
	t.Fatalf("missing relay compliance fixture %q", fixtureID)
	return relayComplianceCase{}
}

func relayOptionsFromFixture(input relayComplianceOptions) Options {
	options := Options{
		ProjectToken:       input.ProjectToken,
		Endpoint:           input.Endpoint,
		Service:            input.Service,
		Environment:        input.Environment,
		RateLimitPerMinute: input.RateLimitPerMinute,
	}
	switch input.ProjectMode {
	case "local-only":
		options.ProjectMode = debugbundle.ProjectModeLocalOnly
	case "connected":
		options.ProjectMode = debugbundle.ProjectModeConnected
	}
	if input.DurableWrite != nil {
		options.DurableWrite = *input.DurableWrite
	}
	return options
}

func relayRequestFromFixture(t *testing.T, fixture relayComplianceRequest) *http.Request {
	t.Helper()
	method := fixture.Method
	if method == "" {
		method = http.MethodPost
	}
	host := fixture.Headers["host"]
	if strings.TrimSpace(host) == "" {
		host = "app.example.com"
	}
	body := fixture.BodyText
	if body == "" {
		switch {
		case fixture.BodyGenerator != nil && fixture.BodyGenerator.Kind == "repeat":
			body = strings.Repeat(fixture.BodyGenerator.Char, fixture.BodyGenerator.Length)
		default:
			payload := fixture.BodyJSON
			if payload == nil {
				payload = map[string]any{"batch": []any{}}
			}
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("marshal relay fixture request: %v", err)
			}
			body = string(encoded)
		}
	}
	request := httptest.NewRequest(method, "https://"+host+"/debugbundle/browser", strings.NewReader(body))
	request.Host = host
	for name, value := range fixture.Headers {
		if strings.EqualFold(name, "host") {
			request.Host = value
			continue
		}
		if value != "" {
			request.Header.Set(name, value)
		}
	}
	if fixture.IPAddress != "" {
		request.RemoteAddr = fixture.IPAddress
	}
	return request
}

func assertRelayFixtureResponse(t *testing.T, response *httptest.ResponseRecorder, expected relayComplianceExpected) {
	t.Helper()
	if response.Code != expected.Status {
		t.Fatalf("expected status %d, got %d body=%s", expected.Status, response.Code, response.Body.String())
	}
	if response.Body.Len() == 0 {
		return
	}
	var body responseBody
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal relay response body: %v", err)
	}
	if body.Accepted != expected.Accepted || body.Rejected != expected.Rejected || !reflect.DeepEqual(body.Errors, expected.Errors) {
		t.Fatalf("expected body accepted=%d rejected=%d errors=%#v, got accepted=%d rejected=%d errors=%#v", expected.Accepted, expected.Rejected, expected.Errors, body.Accepted, body.Rejected, body.Errors)
	}
}

func readRelayBatchFile(t *testing.T, directory string) []map[string]any {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(directory, "*.events.json"))
	if err != nil {
		t.Fatalf("glob relay batch files: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected one relay batch file, got %d", len(files))
	}
	content, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("read relay batch file: %v", err)
	}
	var written []map[string]any
	if err := json.Unmarshal(content, &written); err != nil {
		t.Fatalf("unmarshal relay batch file: %v", err)
	}
	return written
}

func cloneRelayJSONMapSlice(t *testing.T, input []map[string]any) []map[string]any {
	t.Helper()
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal relay json clone: %v", err)
	}
	var cloned []map[string]any
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		t.Fatalf("unmarshal relay json clone: %v", err)
	}
	return cloned
}