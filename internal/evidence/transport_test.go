package evidence

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("fixture read failure") }
func TestNativeResponseBytesRequireIdentityUTF8AndFiniteLength(t *testing.T) {
	for _, fixture := range []struct {
		name, encoding, body string
		length               int64
		valid                bool
	}{{"valid", "identity", "{}", 2, true}, {"unknown-length", "", "{}", -1, true}, {"encoded", "gzip", "{}", 2, false}, {"advertised-large", "", "", MaxBodyBytes + 1, false}, {"stream-large", "", strings.Repeat("x", MaxBodyBytes+1), -1, false}, {"utf8", "", string([]byte{255}), -1, false}} {
		t.Run(fixture.name, func(t *testing.T) {
			response := &http.Response{Header: http.Header{}, ContentLength: fixture.length, Body: io.NopCloser(strings.NewReader(fixture.body))}
			response.Header.Set("Content-Encoding", fixture.encoding)
			_, err := ReadResponse(response)
			if (err == nil) != fixture.valid {
				t.Fatal("native response boundary mismatch")
			}
		})
	}
	if _, err := ReadResponse(&http.Response{Header: http.Header{}, ContentLength: -1, Body: io.NopCloser(failingReader{})}); err == nil {
		t.Fatal("read failure accepted")
	}
}
func TestCanonicalCompleteACKDoesNotMistakePartialOrLegacyAcceptance(t *testing.T) {
	for _, fixture := range []struct {
		body            string
		complete, valid bool
		invalid         int
	}{{`{"accepted":1,"rejected":0,"errors":[]}`, true, true, 0}, {`{"accepted":0,"rejected":1,"errors":[{"index":0,"reason":"invalid_event"}]}`, false, true, 1}, {`{"accepted":0,"rejected":1,"errors":[{"index":0,"reason":"rate_limited"}]}`, false, true, 0}, {`{}`, false, false, 0}, {`{`, false, false, 0}, {`{"accepted":1,"rejected":0,"errors":null}`, false, false, 0}, {`{"accepted":2,"rejected":0,"errors":[]}`, false, false, 0}, {`{"accepted":0,"rejected":1,"errors":[{"index":1,"reason":"invalid_event"}]}`, false, false, 0}, {`{"accepted":0,"rejected":1,"errors":[{"index":0,"reason":""}]}`, false, false, 0}, {strings.Repeat(" ", MaxBodyBytes+1), false, false, 0}} {
		complete, invalid, valid := CompleteAcknowledgement([]byte(fixture.body), 1)
		if complete != fixture.complete || valid != fixture.valid || len(invalid) != fixture.invalid {
			t.Fatal("ACK proof mismatch")
		}
	}
}
func TestNativeWirePrefixFitsActualEncoderAndNeverRestoresOptionalContent(t *testing.T) {
	small := json.RawMessage(`{"message":"fixture"}`)
	if Prefix([]json.RawMessage{small, small}) != 2 || Prefix([]json.RawMessage{json.RawMessage(`{`)}) != 0 {
		t.Fatal("prefix encoding mismatch")
	}
	large, _ := json.Marshal(map[string]any{"message": strings.Repeat("x", MaxBodyBytes)})
	if Prefix([]json.RawMessage{large}) != 0 {
		t.Fatal("oversized event included")
	}
	if _, ok := FitEvent(large); ok {
		t.Fatal("unfit primary accepted")
	}
	primary := map[string]any{"event_type": "frontend_exception", "payload": map[string]any{"message": "latest accepted primary", "exception_evidence": map[string]any{"large": strings.Repeat("x", MaxBodyBytes)}}, "context": map[string]any{"evidence_reference": map[string]any{"custom": "legacy"}}}
	encoded, _ := json.Marshal(primary)
	fitted, ok := FitEvent(encoded)
	if !ok {
		t.Fatal("optional metadata not reduced")
	}
	var result map[string]any
	_ = json.Unmarshal(fitted, &result)
	if result["payload"].(map[string]any)["message"] != "latest accepted primary" || result["context"].(map[string]any)["evidence_reference"] == nil {
		t.Fatal("primary redaction or legacy context lost")
	}
	if WithoutOptional(map[string]any{"event_type": "log_event"}, true) != nil || WithoutOptional(map[string]any{"event_type": "frontend_exception", "payload": map[string]any{}}, false) != nil {
		t.Fatal("unrelated optional reduction invented")
	}
	if _, ok := FitEvent(json.RawMessage(strings.Repeat("x", MaxBodyBytes+1))); ok {
		t.Fatal("malformed wire accepted")
	}
}
