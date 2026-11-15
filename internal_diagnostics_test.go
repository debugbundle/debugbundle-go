package debugbundle

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

type auditDiagnosticError struct{}

func (auditDiagnosticError) Error() string { panic("synthetic Error method failure") }

type auditDiagnosticFetcher struct{ failure error }

func (f auditDiagnosticFetcher) Fetch(context.Context, RemoteConfigRequest) (RemoteConfigResponse, error) {
	return RemoteConfigResponse{}, f.failure
}

func TestInternalDiagnosticPanicIsContained(t *testing.T) {
	if os.Getenv("DEBUGBUNDLE_DIAGNOSTIC_AUDIT_CHILD") == "1" {
		client := New(Config{ProjectToken: "dbundle_proj_test", Transport: &recordingTransport{},
			RemoteConfigFetcher: auditDiagnosticFetcher{failure: auditDiagnosticError{}}})
		defer client.Close()
		select {
		case <-client.remoteConfigReady:
		case <-time.After(time.Second):
			t.Fatal("config did not finish")
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestInternalDiagnosticPanicIsContained$")
	command.Env = append(os.Environ(), "DEBUGBUNDLE_DIAGNOSTIC_AUDIT_CHILD=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("SDK diagnostic crashed host child: %v\n%s", err, output)
	}
}

func TestInternalDiagnosticDataIsBounded(t *testing.T) {
	client := New(Config{ProjectToken: "dbundle_proj_test", Transport: &recordingTransport{},
		RemoteConfigFetcher: auditDiagnosticFetcher{failure: errors.New("token=audit-private-value " + strings.Repeat("x", 1<<20))}})
	defer client.Close()
	select {
	case <-client.remoteConfigReady:
	case <-time.After(time.Second):
		t.Fatal("config did not finish")
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.diagnostics) != 1 {
		t.Fatalf("unexpected diagnostic count %d", len(client.diagnostics))
	}
	value := client.diagnostics[0]
	if len(value) > 16384 || strings.Contains(value, "audit-private-value") {
		t.Fatalf("diagnostic retained %d bytes; raw synthetic credential=%t", len(value), strings.Contains(value, "audit-private-value"))
	}
}
