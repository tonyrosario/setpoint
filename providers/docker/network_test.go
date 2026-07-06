package docker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"

	"github.com/tonyrosario/setpoint/core/api"
)

func TestDecodeNetworkSpecDefaultsDriver(t *testing.T) {
	res := &api.Resource{Kind: KindNetwork, Name: "backend", Spec: json.RawMessage(`{}`)}
	spec, err := decodeNetworkSpec(res)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if spec.Driver != "bridge" {
		t.Errorf("driver = %q, want bridge", spec.Driver)
	}
}

func TestDecodeNetworkSpecExplicitDriver(t *testing.T) {
	res := &api.Resource{Kind: KindNetwork, Name: "backend", Spec: json.RawMessage(`{"driver":"macvlan"}`)}
	spec, err := decodeNetworkSpec(res)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if spec.Driver != "macvlan" {
		t.Errorf("driver = %q, want macvlan", spec.Driver)
	}
}

func TestDecodeNetworkSpecRejectsGarbage(t *testing.T) {
	res := &api.Resource{Kind: KindNetwork, Name: "backend", Spec: json.RawMessage(`"not an object"`)}
	if _, err := decodeNetworkSpec(res); err == nil {
		t.Fatal("expected error for non-object spec")
	}
}

func TestNetworkName(t *testing.T) {
	res := &api.Resource{Kind: KindNetwork, Name: "backend"}
	if got := networkName(res); got != "setpoint-backend" {
		t.Errorf("networkName = %q, want setpoint-backend", got)
	}
}

func TestNetworkProviderKinds(t *testing.T) {
	p := NewNetworkWithClient(nil)
	kinds := p.Kinds()
	if len(kinds) != 1 || kinds[0] != "network" {
		t.Errorf("Kinds() = %v, want [network]", kinds)
	}
}

// fakeNetworkClient stubs the handful of network calls the provider uses.
// The embedded interface panics on anything else — a test reaching an
// unstubbed method is a test bug.
type fakeNetworkClient struct {
	client.APIClient
	networks   []network.Summary
	removeErr  error
	removed    []string
	createName string
	createOpts network.CreateOptions
	createErr  error
}

func (f *fakeNetworkClient) NetworkList(ctx context.Context, opts network.ListOptions) ([]network.Summary, error) {
	return f.networks, nil
}

func (f *fakeNetworkClient) NetworkRemove(ctx context.Context, id string) error {
	if f.removeErr != nil {
		return f.removeErr
	}
	f.removed = append(f.removed, id)
	return nil
}

func (f *fakeNetworkClient) NetworkCreate(ctx context.Context, name string, opts network.CreateOptions) (network.CreateResponse, error) {
	if f.createErr != nil {
		return network.CreateResponse{}, f.createErr
	}
	f.createName = name
	f.createOpts = opts
	return network.CreateResponse{ID: "created"}, nil
}

func networkRes(spec string) *api.Resource {
	return &api.Resource{Kind: KindNetwork, Name: "backend", Spec: json.RawMessage(spec)}
}

func TestNetworkObserveAbsent(t *testing.T) {
	p := NewNetworkWithClient(&fakeNetworkClient{})
	obs, err := p.Observe(context.Background(), networkRes(`{}`))
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if obs.Exists {
		t.Error("Exists = true for no owned networks")
	}
}

func TestNetworkObserveReadyAndUpToDate(t *testing.T) {
	res := networkRes(`{"driver":"bridge"}`)
	fake := &fakeNetworkClient{networks: []network.Summary{{
		ID:     "abcdef123456789",
		Driver: "bridge",
		Labels: map[string]string{labelSpecHash: specHash(res)},
	}}}
	p := NewNetworkWithClient(fake)

	obs, err := p.Observe(context.Background(), res)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if !obs.Exists || !obs.Ready || !obs.UpToDate {
		t.Errorf("obs = %+v, want exists+ready+upToDate", obs)
	}
	if obs.Details["networkId"] != "abcdef123456" {
		t.Errorf("networkId = %q, want short id", obs.Details["networkId"])
	}
}

func TestNetworkObserveStaleSpecHash(t *testing.T) {
	res := networkRes(`{"driver":"bridge"}`)
	fake := &fakeNetworkClient{networks: []network.Summary{{
		ID:     "abc",
		Labels: map[string]string{labelSpecHash: "stale"},
	}}}
	p := NewNetworkWithClient(fake)

	obs, err := p.Observe(context.Background(), res)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if obs.UpToDate {
		t.Error("UpToDate = true for stale spec-hash")
	}
	if obs.Message == "" {
		t.Error("expected a will-recreate message")
	}
}

func TestNetworkDeleteAlreadyGone(t *testing.T) {
	p := NewNetworkWithClient(&fakeNetworkClient{})
	if err := p.Delete(context.Background(), networkRes(`{}`)); err != nil {
		t.Fatalf("delete of absent network: %v", err)
	}
}

func TestNetworkDeleteToleratesNotFoundRace(t *testing.T) {
	fake := &fakeNetworkClient{
		networks:  []network.Summary{{ID: "abc"}},
		removeErr: errdefs.NotFound(errors.New("no such network")),
	}
	p := NewNetworkWithClient(fake)
	if err := p.Delete(context.Background(), networkRes(`{}`)); err != nil {
		t.Fatalf("delete racing an out-of-band removal: %v", err)
	}
}

func TestNetworkDeleteAttachedContainersIsPending(t *testing.T) {
	fake := &fakeNetworkClient{
		networks:  []network.Summary{{ID: "abc"}},
		removeErr: errdefs.Forbidden(errors.New("network abc has active endpoints")),
	}
	p := NewNetworkWithClient(fake)

	err := p.Delete(context.Background(), networkRes(`{}`))
	if err == nil {
		t.Fatal("expected a retryable error while containers are attached")
	}
	if !strings.Contains(err.Error(), "containers attached") {
		t.Errorf("error %q should explain the pending condition", err)
	}
}

func TestNetworkCreateDefaultsDriverAndStampsLabels(t *testing.T) {
	res := networkRes(`{}`)
	fake := &fakeNetworkClient{}
	p := NewNetworkWithClient(fake)

	if err := p.Create(context.Background(), res); err != nil {
		t.Fatalf("create: %v", err)
	}
	if fake.createName != "setpoint-backend" {
		t.Errorf("created name = %q, want setpoint-backend", fake.createName)
	}
	if fake.createOpts.Driver != "bridge" {
		t.Errorf("driver = %q, want bridge default", fake.createOpts.Driver)
	}
	if fake.createOpts.Labels[labelOwner] != ownerValue {
		t.Errorf("owner label = %q, want %q", fake.createOpts.Labels[labelOwner], ownerValue)
	}
	if fake.createOpts.Labels[labelSpecHash] != specHash(res) {
		t.Errorf("spec-hash label = %q, want %q", fake.createOpts.Labels[labelSpecHash], specHash(res))
	}
}

func TestNetworkUpdateDeletesThenRecreates(t *testing.T) {
	fake := &fakeNetworkClient{networks: []network.Summary{{ID: "stale"}}}
	p := NewNetworkWithClient(fake)

	if err := p.Update(context.Background(), networkRes(`{"driver":"bridge"}`)); err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(fake.removed) != 1 || fake.removed[0] != "stale" {
		t.Errorf("removed = %v, want the stale network", fake.removed)
	}
	if fake.createName != "setpoint-backend" {
		t.Error("update did not recreate the network after removal")
	}
}

func TestNetworkDeleteRemovesOwned(t *testing.T) {
	fake := &fakeNetworkClient{networks: []network.Summary{{ID: "net1"}, {ID: "net2"}}}
	p := NewNetworkWithClient(fake)

	if err := p.Delete(context.Background(), networkRes(`{}`)); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(fake.removed) != 2 {
		t.Errorf("removed %v, want both owned networks", fake.removed)
	}
}
