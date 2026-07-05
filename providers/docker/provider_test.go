package docker

import (
	"encoding/json"
	"testing"

	"github.com/tonyrosario/setpoint/core/api"
)

func TestDecodeSpec(t *testing.T) {
	res := &api.Resource{
		Kind: KindContainer,
		Name: "web",
		Spec: json.RawMessage(`{"image":"nginx:alpine"}`),
	}
	spec, err := decodeSpec(res)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if spec.Image != "nginx:alpine" {
		t.Errorf("image = %q", spec.Image)
	}
}

func TestDecodeSpecRequiresImage(t *testing.T) {
	res := &api.Resource{Kind: KindContainer, Name: "web", Spec: json.RawMessage(`{}`)}
	if _, err := decodeSpec(res); err == nil {
		t.Fatal("expected error for missing image")
	}
}

func TestDecodeSpecRejectsGarbage(t *testing.T) {
	res := &api.Resource{Kind: KindContainer, Name: "web", Spec: json.RawMessage(`"not an object"`)}
	if _, err := decodeSpec(res); err == nil {
		t.Fatal("expected error for non-object spec")
	}
}

func TestOwnershipLabels(t *testing.T) {
	res := &api.Resource{Kind: KindContainer, Name: "web"}
	labels := ownershipLabels(res)

	want := map[string]string{
		"setpoint.io/owner":         "setpoint",
		"setpoint.io/resource-name": "web",
		"setpoint.io/resource-kind": "container",
	}
	for k, v := range want {
		if labels[k] != v {
			t.Errorf("label %s = %q, want %q", k, labels[k], v)
		}
	}
}

func TestSpecHashChangesWithSpec(t *testing.T) {
	a := &api.Resource{Kind: KindContainer, Name: "web", Spec: json.RawMessage(`{"image":"nginx:alpine"}`)}
	b := &api.Resource{Kind: KindContainer, Name: "web", Spec: json.RawMessage(`{"image":"nginx:1.27"}`)}

	if specHash(a) == specHash(b) {
		t.Error("different specs produced the same hash")
	}
	// Stable for identical spec bytes.
	a2 := &api.Resource{Kind: KindContainer, Name: "web", Spec: json.RawMessage(`{"image":"nginx:alpine"}`)}
	if specHash(a) != specHash(a2) {
		t.Error("identical specs produced different hashes")
	}
}

func TestOwnershipLabelsIncludeSpecHash(t *testing.T) {
	res := &api.Resource{Kind: KindContainer, Name: "web", Spec: json.RawMessage(`{"image":"nginx"}`)}
	labels := ownershipLabels(res)
	if labels[labelSpecHash] != specHash(res) {
		t.Errorf("spec-hash label = %q, want %q", labels[labelSpecHash], specHash(res))
	}
}

func TestContainerName(t *testing.T) {
	res := &api.Resource{Kind: KindContainer, Name: "web"}
	if got := containerName(res); got != "setpoint-web" {
		t.Errorf("containerName = %q, want setpoint-web", got)
	}
}
