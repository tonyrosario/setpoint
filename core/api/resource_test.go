package api

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSpecReferenceTokens(t *testing.T) {
	spec := []byte(`{"image":"nginx","network":"$(ref:net)","extra":"$(ref:other)-$(ref:net)"}`)
	got := SpecReferenceTokens(spec)
	want := []string{"net", "other", "net"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tokens = %v, want %v", got, want)
	}
	if got := SpecReferenceTokens([]byte(`{"image":"nginx"}`)); got != nil {
		t.Errorf("tokens = %v, want none", got)
	}
}

func TestSubstituteReferences(t *testing.T) {
	spec := []byte(`{"network":"$(ref:net)","also":"pre-$(ref:net)-post"}`)
	got := SubstituteReferences(spec, map[string]string{"net": "abc123"})
	want := `{"network":"abc123","also":"pre-abc123-post"}`
	if string(got) != want {
		t.Errorf("substituted = %s, want %s", got, want)
	}
}

func TestSubstituteReferencesEscapesJSON(t *testing.T) {
	// A hostile resolved value must not be able to break out of its JSON
	// string — the splice is escaped.
	spec := []byte(`{"network":"$(ref:net)"}`)
	got := SubstituteReferences(spec, map[string]string{"net": `a"b\c`})

	var decoded struct {
		Network string `json:"network"`
	}
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("substituted spec is not valid JSON: %v (%s)", err, got)
	}
	if decoded.Network != `a"b\c` {
		t.Errorf("network = %q, want the raw value round-tripped", decoded.Network)
	}
}

func TestSubstituteReferencesLeavesUnknownTokens(t *testing.T) {
	spec := []byte(`{"network":"$(ref:net)"}`)
	got := SubstituteReferences(spec, map[string]string{})
	if string(got) != string(spec) {
		t.Errorf("substituted = %s, want untouched", got)
	}
}
