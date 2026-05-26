package debugbundle

import (
	"os"
	"strings"
	"testing"
)

func TestSmokeHarnessExistsAndIsWiredIntoWorkflows(t *testing.T) {
	t.Helper()

	for _, path := range []string{
		"Makefile",
		"smoke/run_app_driven_smoke.sh",
		".github/workflows/ci.yml",
		".github/workflows/release.yml",
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s to exist: %v", path, err)
		}
	}

	makefile := readRepositoryFile(t, "Makefile")
	ciWorkflow := readRepositoryFile(t, ".github/workflows/ci.yml")
	releaseWorkflow := readRepositoryFile(t, ".github/workflows/release.yml")

	for _, fragment := range []string{
		".PHONY: smoke",
		".PHONY: smoke-published",
		"smoke/run_app_driven_smoke.sh --source local",
		"smoke/run_app_driven_smoke.sh --source published --version $(VERSION)",
	} {
		if !strings.Contains(makefile, fragment) {
			t.Fatalf("expected Makefile to contain %q", fragment)
		}
	}

	if !strings.Contains(ciWorkflow, "make smoke") {
		t.Fatalf("expected CI workflow to run make smoke")
	}

	for _, fragment := range []string{
		"make smoke",
		"release_tag_not_found",
		"make smoke-published VERSION=${RELEASE_VERSION}",
	} {
		if !strings.Contains(releaseWorkflow, fragment) {
			t.Fatalf("expected release workflow to contain %q", fragment)
		}
	}
}

func TestReadmeCoversGoReleaseDocumentationGates(t *testing.T) {
	t.Helper()

	readme := readRepositoryFile(t, "README.md")

	for _, fragment := range []string{
		"## Configuration Reference",
		"Configuration sources and precedence:",
		"Capture-policy fields are server-owned",
		"## Install Examples by Mode",
		"## Runtime and Framework Support",
		"## Dependency Alignment",
		"## Browser Relay",
		"## Service Naming",
		"## Safe Startup and Status",
		"## First-Event Verification",
		"make smoke",
		"same-origin",
		"allowed origins",
		"rate limiting",
		"credential isolation",
		"missing token",
	} {
		if !strings.Contains(readme, fragment) {
			t.Fatalf("expected README to contain %q", fragment)
		}
	}
}

func readRepositoryFile(t *testing.T, path string) string {
	t.Helper()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	return string(content)
}
