package relay

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	debugbundle "github.com/debugbundle/debugbundle-go/v3"
	"github.com/debugbundle/debugbundle-go/v3/internal/evidence"
	"github.com/debugbundle/debugbundle-go/v3/transport"
)

func evidenceFixture(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile("../internal/evidence/testdata/evidence-wire-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Events []struct {
			ID    string         `json:"id"`
			Event map[string]any `json:"event"`
		}
	}
	if json.Unmarshal(data, &corpus) != nil {
		t.Fatal("bad corpus")
	}
	for _, fixture := range corpus.Events {
		if fixture.ID == "native_graph" {
			fixture.Event["sdk_name"] = browserSDKName
			return fixture.Event
		}
	}
	t.Fatal("missing fixture")
	return nil
}
func evidenceRequest(t *testing.T, events []map[string]any, discover bool) *http.Request {
	t.Helper()
	body, err := json.Marshal(map[string]any{"batch": events})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "https://app.example.test/debugbundle/browser", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://app.example.test")
	if discover {
		request.Header.Set("X-DebugBundle-Evidence", "1")
	}
	return request
}
func fullProof() *debugbundle.EvidenceCapabilities {
	return &debugbundle.EvidenceCapabilities{Version: 1, Features: append([]string{}, evidence.Features...)}
}
func TestRelayPreservesCanonicalEvidenceAndProtectedContext(t *testing.T) {
	directory := t.TempDir()
	handler := NewHandler(nil, Options{ProjectMode: debugbundle.ProjectModeLocalOnly, LocalEventsDir: directory, EvidenceCapabilities: fullProof()})
	event := evidenceFixture(t)
	event["context"] = map[string]any{"technical": "fixture", "password": "synthetic-secret"}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, evidenceRequest(t, []map[string]any{event}, true))
	if response.Code != 202 {
		t.Fatal(response.Code)
	}
	files, _ := filepath.Glob(filepath.Join(directory, "*.events.json"))
	if len(files) != 1 {
		t.Fatal("no local evidence")
	}
	data, _ := os.ReadFile(files[0])
	var written []map[string]any
	_ = json.Unmarshal(data, &written)
	if !evidence.Validate(written[0]) || written[0]["payload"].(map[string]any)["exception_evidence"] == nil {
		t.Fatal("canonical evidence lost")
	}
	context, _ := written[0]["context"].(map[string]any)
	if context["technical"] != "fixture" || context["password"] == "synthetic-secret" {
		t.Fatal("context lost or not protected")
	}
	var reply map[string]any
	_ = json.Unmarshal(response.Body.Bytes(), &reply)
	if len(evidence.Descriptor(reply["evidence_capabilities"]).Features) != 4 {
		t.Fatal("pinned reader proof not advertised")
	}
}
func TestRelayMalformedEvidenceIsIndexedAndNeverDowngradedAsLegacy(t *testing.T) {
	event := evidenceFixture(t)
	event["payload"].(map[string]any)["exception_evidence"].(map[string]any)["version"] = 2
	response := httptest.NewRecorder()
	NewHandler(nil, Options{}).ServeHTTP(response, evidenceRequest(t, []map[string]any{event}, true))
	var reply struct {
		Accepted int `json:"accepted"`
		Errors   []struct {
			Index  int    `json:"index"`
			Reason string `json:"reason"`
		} `json:"evidence_errors"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &reply)
	if response.Code != 202 || reply.Accepted != 0 || len(reply.Errors) != 1 || reply.Errors[0].Index != 0 || reply.Errors[0].Reason != "invalid_event" {
		t.Fatal("malformed evidence accepted as legacy")
	}
}
func TestRelayLegacyStatusOnlyAcceptanceLeavesDurableSpoolPending(t *testing.T) {
	spool := t.TempDir()
	forwarder := &recordingTransport{}
	handler := NewHandler(nil, Options{ProjectMode: debugbundle.ProjectModeConnected, ProjectToken: "synthetic", DurableWrite: true, SpoolDir: spool, Transport: forwarder})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, evidenceRequest(t, []map[string]any{evidenceFixture(t)}, true))
	files, _ := filepath.Glob(filepath.Join(spool, "*.events.json"))
	markers, _ := filepath.Glob(filepath.Join(spool, "*.delivered"))
	if response.Code != 202 || len(files) != 1 || len(markers) != 0 {
		t.Fatal("unverified acceptance marked spool delivered")
	}
}
func TestRelayMarkerDoesNotFollowOrTruncateExistingTargets(t *testing.T) {
	file := filepath.Join(t.TempDir(), "batch.events.json")
	target := filepath.Join(t.TempDir(), "keep")
	_ = os.WriteFile(target, []byte("retain"), 0600)
	if os.Symlink(target, file+relayDeliveredMarkerSuffix) != nil {
		t.Fatal("symlink fixture failed")
	}
	markSpoolFileDelivered(file)
	data, _ := os.ReadFile(target)
	if string(data) != "retain" {
		t.Fatal("marker followed or truncated target")
	}
}
func TestRelaySplitsServerCredentialBytes(t *testing.T) {
	calls, total := 0, 0
	token := strings.Repeat("synthetic", 700)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		body, _ := io.ReadAll(request.Body)
		if len(body) > evidence.MaxBodyBytes {
			t.Error("oversized native request")
		}
		var batch struct {
			Events []map[string]any `json:"events"`
		}
		_ = json.Unmarshal(body, &batch)
		total += len(batch.Events)
		for _, event := range batch.Events {
			if event["project_token"] != token {
				t.Error("server credential absent")
			}
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"accepted": len(batch.Events), "rejected": 0, "errors": []any{}, "evidence_capabilities": fullProof()})
	}))
	defer server.Close()
	handler := NewHandler(nil, Options{ProjectMode: debugbundle.ProjectModeConnected, ProjectToken: token, Endpoint: server.URL, EvidenceCapabilities: fullProof()})
	events := []map[string]any{}
	for index := 0; index < 50; index++ {
		event := evidenceFixture(t)
		event["event_id"] = fmt.Sprintf("00000000-0000-4000-8000-%012d", index+1)
		events = append(events, event)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, evidenceRequest(t, events, true))
	if response.Code != 202 || total != 50 || calls < 2 {
		t.Fatalf("native chunks missing: status=%d calls=%d total=%d", response.Code, calls, total)
	}
}

var _ transport.Sender = (*recordingTransport)(nil)

func TestRelayNativeACKDiscoversEvidenceForFollowingRequest(t *testing.T) {
	graphs := []bool{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var batch struct {
			Events []map[string]any `json:"events"`
		}
		_ = json.NewDecoder(request.Body).Decode(&batch)
		graphs = append(graphs, batch.Events[0]["payload"].(map[string]any)["exception_evidence"] != nil)
		_ = json.NewEncoder(writer).Encode(map[string]any{"accepted": 1, "rejected": 0, "errors": []any{}, "evidence_capabilities": fullProof()})
	}))
	defer server.Close()
	handler := NewHandler(nil, Options{ProjectMode: debugbundle.ProjectModeConnected, ProjectToken: "synthetic", Endpoint: server.URL})
	for index := 0; index < 2; index++ {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, evidenceRequest(t, []map[string]any{evidenceFixture(t)}, true))
		if response.Code != 202 {
			t.Fatal("native forwarding failed")
		}
	}
	if len(graphs) != 2 || graphs[0] || !graphs[1] {
		t.Fatal("unknown path did not discover support on existing ACK")
	}
}
func TestRelayPartialNativeACKKeepsSpoolPending(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		_, _ = io.WriteString(writer, `{"accepted":0,"rejected":1,"errors":[{"index":0,"reason":"rate_limited"}]}`)
	}))
	defer server.Close()
	spool := t.TempDir()
	handler := NewHandler(nil, Options{ProjectMode: debugbundle.ProjectModeConnected, ProjectToken: "synthetic", Endpoint: server.URL, DurableWrite: true, SpoolDir: spool, EvidenceCapabilities: fullProof()})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, evidenceRequest(t, []map[string]any{evidenceFixture(t)}, true))
	markers, _ := filepath.Glob(filepath.Join(spool, "*.delivered"))
	if response.Code != 202 || len(markers) != 0 {
		t.Fatal("partial acceptance marked spool complete")
	}
}
func TestRelayNativeDowngradeRequiresAllOriginalsAcknowledged(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		var batch struct {
			Events []map[string]any `json:"events"`
		}
		_ = json.NewDecoder(request.Body).Decode(&batch)
		if calls == 1 {
			if batch.Events[0]["payload"].(map[string]any)["exception_evidence"] == nil {
				t.Error("pinned graph missing")
			}
			_, _ = io.WriteString(writer, `{"accepted":0,"rejected":1,"errors":[{"index":0,"reason":"invalid_event"}]}`)
		} else {
			if batch.Events[0]["payload"].(map[string]any)["exception_evidence"] != nil {
				t.Error("downgrade retained unsupported graph")
			}
			_, _ = io.WriteString(writer, `{"accepted":1,"rejected":0,"errors":[]}`)
		}
	}))
	defer server.Close()
	spool := t.TempDir()
	handler := NewHandler(nil, Options{ProjectMode: debugbundle.ProjectModeConnected, ProjectToken: "synthetic", Endpoint: server.URL, DurableWrite: true, SpoolDir: spool, EvidenceCapabilities: fullProof()})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, evidenceRequest(t, []map[string]any{evidenceFixture(t)}, true))
	markers, _ := filepath.Glob(filepath.Join(spool, "*.delivered"))
	if response.Code != 202 || calls != 2 || len(markers) != 1 {
		t.Fatal("complete downgrade acceptance missing")
	}
}

func TestRelayProofLossFiltersAlreadyPreparedNativeTails(t *testing.T) {
	calls, total := 0, 0
	token := strings.Repeat("synthetic", 700)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		var batch struct {
			Events []map[string]any `json:"events"`
		}
		_ = json.NewDecoder(request.Body).Decode(&batch)
		total += len(batch.Events)
		for _, event := range batch.Events {
			if calls > 1 && event["payload"].(map[string]any)["exception_evidence"] != nil {
				t.Error("prepared tail retained revoked evidence")
			}
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"accepted": len(batch.Events), "rejected": 0, "errors": []any{}})
	}))
	defer server.Close()
	handler := NewHandler(nil, Options{ProjectMode: debugbundle.ProjectModeConnected, ProjectToken: token, Endpoint: server.URL, EvidenceCapabilities: fullProof()})
	events := []map[string]any{}
	for index := 0; index < 50; index++ {
		events = append(events, evidenceFixture(t))
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, evidenceRequest(t, events, true))
	if response.Code != 202 || calls < 2 || total != 50 {
		t.Fatal("tail continuation missing")
	}
}

func TestRelayBroadRedactFieldsPreservesClosedBrowserProtocolStructures(t *testing.T) {
	directory := t.TempDir()
	client := debugbundle.New(debugbundle.Config{
		ProjectToken:   "dbundle_proj_test",
		ProjectMode:    debugbundle.ProjectModeLocalOnly,
		LocalEventsDir: directory,
	}.WithRedactFields("tag", "reference", "reason", "file"))
	defer func() { _ = client.Close() }()

	handler := NewHandler(client, Options{EvidenceCapabilities: fullProof()})
	event := evidenceFixture(t)
	payload := event["payload"].(map[string]any)
	payload["browser_event"] = map[string]any{
		"kind":          "resource_error",
		"message":       "script load failed tag=SYNTHETIC_BE_MSG",
		"file_name":     "https://cdn.example.test/app.js?file=SYNTHETIC_BE_FILE&ok=1",
		"line_number":   10,
		"column_number": 2,
		"opaque":        false,
		"target": map[string]any{
			"tag_name":   "script",
			"source_url": "https://cdn.example.test/app.js?tag=SYNTHETIC_SRC&ok=1",
		},
	}
	payload["rejection_reason"] = map[string]any{
		"kind":    "error",
		"message": "rejected reason=SYNTHETIC_REJ",
	}
	payload["resource_observations"] = map[string]any{
		"version": 1,
		"items": []any{
			map[string]any{
				"kind": "timing", "observed_at": "2026-05-23T12:00:00Z", "source_url": "https://cdn.example.test/app.js",
				"availability": "observed", "response_status": 500, "duration_ms": 14, "transfer_size_bytes": 0,
			},
		},
	}
	event["context"] = map[string]any{
		"evidence_reference": map[string]any{
			"version":     1,
			"failure_ref": "11111111-1111-4111-8111-111111111111",
			"scope":       map[string]any{"kind": "page", "page_id": "22222222-2222-4222-8222-222222222222"},
		},
		"evidence_limits": map[string]any{"version": 1, "execution": "unsupported"},
		"payout": map[string]any{
			"tag":       "SYNTHETIC_TAG",
			"reference": "SYNTHETIC_REF",
			"reason":    "SYNTHETIC_REASON",
			"file":      "SYNTHETIC_FILE",
		},
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, evidenceRequest(t, []map[string]any{event}, true))
	if response.Code != http.StatusAccepted {
		t.Fatalf("expected accepted relay response, got %d body=%s", response.Code, response.Body.String())
	}
	files, _ := filepath.Glob(filepath.Join(directory, "*.events.json"))
	if len(files) != 1 {
		t.Fatalf("expected one local relay file, got %d", len(files))
	}
	data, _ := os.ReadFile(files[0])
	var written []map[string]any
	_ = json.Unmarshal(data, &written)
	if len(written) != 1 || !evidence.Validate(written[0]) {
		t.Fatalf("expected canonical preserved relayed event, got %s", data)
	}
	outPayload := written[0]["payload"].(map[string]any)
	outBrowserEvent := outPayload["browser_event"].(map[string]any)
	if outBrowserEvent["target"].(map[string]any)["tag_name"] != "script" {
		t.Fatalf("expected allowlisted browser_event.target.tag_name preserved, got %#v", outBrowserEvent["target"])
	}
	if outBrowserEvent["message"] != "script load failed tag=[REDACTED]" {
		t.Fatalf("expected nested browser_event message scrubbed, got %#v", outBrowserEvent["message"])
	}
	if outPayload["rejection_reason"].(map[string]any)["kind"] != "error" ||
		outPayload["rejection_reason"].(map[string]any)["message"] != "rejected reason=[REDACTED]" {
		t.Fatalf("expected rejection_reason preserved and scrubbed, got %#v", outPayload["rejection_reason"])
	}
	if outPayload["exception_evidence"] == nil || outPayload["resource_observations"] == nil {
		t.Fatalf("expected typed evidence preserved, got %#v", outPayload)
	}
	outContext := written[0]["context"].(map[string]any)
	if outContext["evidence_reference"].(map[string]any)["failure_ref"] != "11111111-1111-4111-8111-111111111111" ||
		outContext["evidence_limits"].(map[string]any)["execution"] != "unsupported" {
		t.Fatalf("expected protocol context preserved, got %#v", outContext)
	}
	payout := outContext["payout"].(map[string]any)
	for _, field := range []string{"tag", "reference", "reason", "file"} {
		if payout[field] != "[REDACTED]" {
			t.Fatalf("expected payout.%s redacted, got %#v", field, payout[field])
		}
	}
	if strings.Contains(string(data), "SYNTHETIC_") {
		t.Fatalf("secret leaked into relayed event: %s", data)
	}
}

func TestRelayRedactExactFieldsMasksExactKeysWhilePreservingSiblings(t *testing.T) {
	directory := t.TempDir()
	handler := NewHandler(nil, Options{
		ProjectMode:    debugbundle.ProjectModeLocalOnly,
		LocalEventsDir: directory,
	}.WithRedactFields("custom_broad").WithRedactExactFields("tag"))

	event := map[string]any{
		"schema_version": "2026-03-01",
		"event_id":       "00000000-0000-4000-8000-000000000777",
		"event_type":     "frontend_exception",
		"occurred_at":    "2026-05-23T12:00:00Z",
		"sdk_name":       browserSDKName,
		"sdk_version":    "1.2.3",
		"service":        map[string]any{"name": "checkout-web", "environment": "production"},
		"payload": map[string]any{
			"message": "failed tag=SYNTHETIC_MSG tag_name=script",
			"url":     "https://example.test/pay?tag=SYNTHETIC_URL&tag_name=script",
		},
		"context": map[string]any{
			"tag":               "SYNTHETIC_EXACT",
			"tag_name":          "script",
			"user_custom_broad": "SYNTHETIC_BROAD",
		},
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, evidenceRequest(t, []map[string]any{event}, false))
	if response.Code != http.StatusAccepted {
		t.Fatalf("expected accepted relay response, got %d body=%s", response.Code, response.Body.String())
	}
	files, _ := filepath.Glob(filepath.Join(directory, "*.events.json"))
	data, _ := os.ReadFile(files[0])
	var written []map[string]any
	_ = json.Unmarshal(data, &written)

	outPayload := written[0]["payload"].(map[string]any)
	outContext := written[0]["context"].(map[string]any)
	if outPayload["message"] != "failed tag=[REDACTED] tag_name=script" ||
		outPayload["url"] != "https://example.test/pay?tag=%5BREDACTED%5D&tag_name=script" {
		t.Fatalf("unexpected relay payload exact redaction: %#v", outPayload)
	}
	if outContext["tag"] != "[REDACTED]" || outContext["tag_name"] != "script" || outContext["user_custom_broad"] != "[REDACTED]" {
		t.Fatalf("unexpected relay context redaction: %#v", outContext)
	}
}
