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
