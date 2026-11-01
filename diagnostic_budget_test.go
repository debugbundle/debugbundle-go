package debugbundle

import (
	"fmt"
	"testing"
	"time"
)

func budgetRecovery(failure, attempt, scope string) map[string]any {
	return map[string]any{"failure_ref": failure, "scope": map[string]any{"kind": "operation", "operation_id": scope}, "kind": "recovery", "attempt_id": attempt}
}
func TestDiagnosticBudgetExpiryNeverRenewsOfflinePending(t *testing.T) {
	budget := newDiagnosticCaptureBudget()
	now := time.Now()
	payload := budgetRecovery("failure", "attempt", "scope")
	for index := 0; index < 20; index++ {
		if !budget.reserve(fmt.Sprint(index), payload, 100, now) {
			t.Fatal("initial observation not reserved")
		}
	}
	if budget.reserve("late", payload, 100, now.Add(6*time.Minute)) {
		t.Fatal("offline expiry reset window")
	}
	for index := 0; index < 20; index++ {
		budget.release(fmt.Sprint(index))
	}
	if !budget.reserve("renewed", payload, 100, now.Add(6*time.Minute)) {
		t.Fatal("completed expired window not pruned")
	}
}
func TestDiagnosticBudgetAttemptsWindowsAmbiguityAndBytes(t *testing.T) {
	now := time.Now()
	budget := newDiagnosticCaptureBudget()
	for index := 0; index < 5; index++ {
		if !budget.reserve(fmt.Sprint(index), budgetRecovery("failure", fmt.Sprint(index), "scope"), 10, now) {
			t.Fatal("attempt limit too small")
		}
	}
	if budget.reserve("sixth", budgetRecovery("failure", "sixth", "scope"), 10, now) {
		t.Fatal("sixth attempt retained")
	}
	payload := budgetRecovery("failure", "0", "scope")
	budget.register(payload, "primary", now)
	budget.register(payload, "another", now)
	if budget.eligible("0", payload) || budget.reserve("ambiguous", payload, 10, now) {
		t.Fatal("duplicate primary kept claiming ownership")
	}
	budget = newDiagnosticCaptureBudget()
	for index := 0; index < 50; index++ {
		id := fmt.Sprint(index)
		if !budget.reserve(id, budgetRecovery(id, "attempt", "scope"), 1, now) {
			t.Fatal("window cap too small")
		}
	}
	if budget.reserve("51", budgetRecovery("51", "attempt", "scope"), 1, now) {
		t.Fatal("window cap exceeded")
	}
	budget = newDiagnosticCaptureBudget()
	payload = budgetRecovery("failure", "attempt", "scope")
	if budget.reserve("oversized", payload, 65536, now) {
		t.Fatal("metadata excluded from byte allowance")
	}
	if !budget.reserve("fit", payload, 64000, now) || budget.bytes() > 65536 {
		t.Fatal("bounded reservation failed")
	}
	if budget.replace("fit", "new", payload, 65536, now) || budget.pendingCount() != 0 {
		t.Fatal("oversized replacement retained")
	}
}
func TestDiagnosticResourceObservationRequiresOneRecentUnambiguousPrimary(t *testing.T) {
	now := time.Now()
	budget := newDiagnosticCaptureBudget()
	payload := budgetRecovery("failure", "", "scope")
	payload["kind"] = "resource_observation"
	payload["source_event_id"] = "primary"
	if budget.reserve("early", payload, 100, now) {
		t.Fatal("resource without primary retained")
	}
	budget.register(payload, "primary", now)
	if budget.reserve("late", payload, 100, now.Add(31*time.Second)) {
		t.Fatal("late resource retained")
	}
	if !budget.reserve("one", payload, 100, now.Add(time.Second)) {
		t.Fatal("recent resource missing")
	}
	budget.release("one")
	if budget.reserve("two", payload, 100, now.Add(2*time.Second)) {
		t.Fatal("resource allowance reset on ACK")
	}
}
