package evidence

import (
	"encoding/json"
	"math"
	"os"
	"testing"
)

func TestSharedCanonicalEvidenceCorpus(t *testing.T) {
	data, err := os.ReadFile("testdata/evidence-wire-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Events []struct {
			ID    string         `json:"id"`
			Event map[string]any `json:"event"`
			Valid bool           `json:"valid"`
		} `json:"events"`
	}
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	schemaOnce.Do(prepareSchema)
	if !schemaReady {
		t.Fatal("canonical schema unavailable")
	}
	for _, fixture := range corpus.Events {
		t.Run(fixture.ID, func(t *testing.T) {
			if got := Validate(fixture.Event); got != fixture.Valid {
				t.Errorf("canonical acceptance = %v, want %v", got, fixture.Valid)
			}
		})
	}
}

func TestReceivingMetadataJSONByteVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/evidence-wire-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Sizes []struct {
			Value any `json:"value"`
			Bytes int `json:"bytes"`
		} `json:"json_sizes"`
	}
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	for index, fixture := range corpus.Sizes {
		if got := Size(fixture.Value); got != fixture.Bytes {
			t.Errorf("vector %d: size=%d want %d", index, got, fixture.Bytes)
		}
	}
}

func TestCapabilityProofMatchesSharedReceivingCases(t *testing.T) {
	data, err := os.ReadFile("testdata/evidence-wire-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Capabilities []struct {
			ID       string   `json:"id"`
			Input    any      `json:"input"`
			Features []string `json:"features"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range corpus.Capabilities {
		t.Run(fixture.ID, func(t *testing.T) {
			descriptor := Descriptor(fixture.Input)
			if len(descriptor.Features) != len(fixture.Features) {
				t.Fatalf("feature count=%d want %d", len(descriptor.Features), len(fixture.Features))
			}
			for index, feature := range fixture.Features {
				if descriptor.Features[index] != feature {
					t.Fatal("descriptor order differs")
				}
			}
		})
	}
}

func TestNativeNegativeZeroUsesReceivingJSONByteForm(t *testing.T) {
	if Size(math.Copysign(0, -1)) != 1 {
		t.Fatal("JSON.stringify negative zero must count as one byte")
	}
}

func TestLocalSchemaReferencesKeepTheirReceivingConstraints(t *testing.T) {
	schemaOnce.Do(prepareSchema)
	for _, test := range []struct {
		name      string
		reference string
		value     any
		valid     bool
	}{
		{"string", "#/anyOf/0/properties/schema_version", "2026-03-01", true},
		{"wrong type", "#/anyOf/0/properties/schema_version", 1, false},
		{"empty string", "#/anyOf/0/properties/schema_version", "", false},
		{"negative index", "#/anyOf/-1", "value", false},
		{"invalid index", "#/anyOf/not-an-index", "value", false},
		{"missing index", "#/anyOf/9999", "value", false},
		{"missing property", "#/properties/missing", "value", false},
		{"scalar traversal", "#/anyOf/0/properties/schema_version/type/child", "value", false},
		{"external", "https://example.invalid/schema.json", "value", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			work := validationWork{}
			if actual := work.validate(test.value, map[string]any{"$ref": test.reference}, 0); actual != test.valid {
				t.Fatalf("reference acceptance=%v, want %v", actual, test.valid)
			}
		})
	}
}

func TestInvalidCanonicalSchemaVocabularyFailsClosed(t *testing.T) {
	for _, schema := range []any{
		nil,
		map[string]any{"unknownKeyword": true},
		map[string]any{"type": "unknown"},
		map[string]any{"type": []any{}},
		map[string]any{"type": []any{"string", "unknown"}},
		map[string]any{"format": "uri"},
		map[string]any{"pattern": "["},
		map[string]any{"properties": []any{}},
		map[string]any{"properties": map[string]any{"field": map[string]any{"unknownKeyword": true}}},
		map[string]any{"anyOf": []any{}},
		map[string]any{"anyOf": []any{map[string]any{"type": "unknown"}}},
	} {
		if compileSchema(schema, 0) {
			t.Fatalf("unsupported contract accepted: %#v", schema)
		}
	}
}
