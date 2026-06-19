package debugbundle

import (
	cryptorand "crypto/rand"
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

var (
	processStartedAt     = time.Now()
	eventIDFallbackCount atomic.Uint64
)

type ResponseInfo struct {
	StatusCode int
	Duration   time.Duration
	Headers    map[string]string
	Route      string
}

type EventEnvelope struct {
	SchemaVersion string            `json:"schema_version"`
	EventID       string            `json:"event_id"`
	EventType     string            `json:"event_type"`
	ProjectToken  string            `json:"project_token,omitempty"`
	SDKName       string            `json:"sdk_name"`
	SDKVersion    string            `json:"sdk_version"`
	Service       ServiceDescriptor `json:"service"`
	OccurredAt    string            `json:"occurred_at"`
	Correlation   map[string]any    `json:"correlation,omitempty"`
	Context       map[string]any    `json:"context,omitempty"`
	Payload       map[string]any    `json:"payload"`
}

type ServiceDescriptor struct {
	Name        string `json:"name"`
	Runtime     string `json:"runtime,omitempty"`
	Framework   string `json:"framework,omitempty"`
	Environment string `json:"environment"`
}

func buildRuntimeFacts() map[string]any {
	memory := runtime.MemStats{}
	runtime.ReadMemStats(&memory)
	cwd, _ := os.Getwd()
	hostname, _ := os.Hostname()
	facts := map[string]any{
		"version":    runtime.Version(),
		"platform":   runtime.GOOS,
		"arch":       runtime.GOARCH,
		"pid":        os.Getpid(),
		"uptime_sec": time.Since(processStartedAt).Seconds(),
		"memory": map[string]any{
			"rss":        nil,
			"heap_total": memory.HeapSys,
			"heap_used":  memory.HeapAlloc,
			"external":   memory.OtherSys,
			"peak":       memory.Sys,
		},
		"framework_extras": map[string]any{
			"goroutines": runtime.NumGoroutine(),
		},
	}
	if cwd != "" {
		facts["cwd"] = cwd
	}
	if hostname != "" {
		facts["hostname"] = hostname
	}
	return facts
}

func emptyRequestPayload() map[string]any {
	return map[string]any{
		"method":  "UNKNOWN",
		"path":    "/",
		"query":   map[string]any{},
		"headers": map[string]any{},
	}
}

func emptyResponsePayload() map[string]any {
	return map[string]any{
		"status_code": 0,
	}
}

func requestPayload(request *http.Request, response ResponseInfo) map[string]any {
	if request == nil {
		return map[string]any{
			"method":          "UNKNOWN",
			"path":            "/",
			"query":           map[string]any{},
			"headers":         map[string]any{},
			"response_status": response.StatusCode,
			"duration_ms":     response.Duration.Milliseconds(),
		}
	}
	payload := map[string]any{
		"method":          request.Method,
		"path":            request.URL.Path,
		"query":           queryValues(request),
		"headers":         map[string]any{},
		"response_status": response.StatusCode,
		"duration_ms":     response.Duration.Milliseconds(),
	}
	if response.Route != "" {
		payload["route_template"] = response.Route
	}
	headers := map[string]any{}
	for _, key := range []string{"User-Agent", "Content-Type", "Accept", "X-Request-Id", "X-Correlation-Id", "X-DebugBundle-Trace-Id"} {
		if value := strings.TrimSpace(request.Header.Get(key)); value != "" {
			headers[strings.ToLower(key)] = value
		}
	}
	payload["headers"] = headers
	if len(response.Headers) > 0 {
		payload["response_headers"] = response.Headers
	}
	return payload
}

func queryValues(request *http.Request) map[string]any {
	values := map[string]any{}
	for key, entries := range request.URL.Query() {
		if len(entries) == 1 {
			values[key] = entries[0]
			continue
		}
		copied := append([]string{}, entries...)
		values[key] = copied
	}
	return values
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func newEventID() string {
	bytes := make([]byte, 16)
	if _, err := cryptorand.Read(bytes); err != nil {
		fallback := fmt.Sprintf("%d:%d", time.Now().UnixNano(), eventIDFallbackCount.Add(1))
		sum := sha256.Sum256([]byte(fallback))
		copy(bytes, sum[:16])
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16])
}
