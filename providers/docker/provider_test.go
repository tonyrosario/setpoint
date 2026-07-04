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

func TestContainerName(t *testing.T) {
	res := &api.Resource{Kind: KindContainer, Name: "web"}
	if got := containerName(res); got != "setpoint-web" {
		t.Errorf("containerName = %q, want setpoint-web", got)
	}
}
