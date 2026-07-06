package docker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"

	"github.com/tonyrosario/setpoint/core/api"
	"github.com/tonyrosario/setpoint/core/provider"
)

const (
	// KindNetwork is the resource kind this Provider owns.
	KindNetwork = "network"

	defaultNetworkDriver = "bridge"
)

// networkSpec is the decoded Spec for the network kind: a driver, defaulting
// to bridge. The network's Substrate name derives from the resource name.
type networkSpec struct {
	Driver string `json:"driver,omitempty"`
}

// NetworkProvider manages Docker networks. It is a sibling of the container
// Provider — same ownership labels, same spec-hash drift detection — proving
// the Provider contract (ADR-0001) generalizes beyond containers.
type NetworkProvider struct {
	cli client.APIClient
}

// NewNetwork connects to the Docker daemon from the environment, negotiating
// the API version with the daemon.
func NewNetwork() (*NetworkProvider, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &NetworkProvider{cli: cli}, nil
}

// NewNetworkWithClient builds a NetworkProvider around an existing client (tests).
func NewNetworkWithClient(cli client.APIClient) *NetworkProvider {
	return &NetworkProvider{cli: cli}
}

func (p *NetworkProvider) Kinds() []string { return []string{KindNetwork} }

func (p *NetworkProvider) Observe(ctx context.Context, res *api.Resource) (provider.Observation, error) {
	list, err := p.cli.NetworkList(ctx, network.ListOptions{Filters: ownedBy(res)})
	if err != nil {
		return provider.Observation{}, fmt.Errorf("list networks: %w", err)
	}
	if len(list) == 0 {
		return provider.Observation{Exists: false}, nil
	}

	n := list[0]
	upToDate := n.Labels[labelSpecHash] == specHash(res)
	obs := provider.Observation{
		Exists: true,
		// A network has no run state to become unhealthy; existing is ready.
		Ready:    true,
		UpToDate: upToDate,
		Details: map[string]string{
			"networkId": shortID(n.ID),
			"driver":    n.Driver,
		},
	}
	if !upToDate {
		obs.Message = "network does not match spec; will recreate"
	}
	return obs, nil
}

func (p *NetworkProvider) Create(ctx context.Context, res *api.Resource) error {
	spec, err := decodeNetworkSpec(res)
	if err != nil {
		return err
	}
	_, err = p.cli.NetworkCreate(ctx, networkName(res), network.CreateOptions{
		Driver: spec.Driver,
		Labels: ownershipLabels(res),
	})
	if err != nil {
		return fmt.Errorf("create network: %w", err)
	}
	return nil
}

// Update converges an existing network toward Spec by delete-and-recreate:
// Docker cannot change a network's driver in place. Same crash-safety
// argument as the container path — a crash between Delete and Create leaves
// no owned network, which the next reconcile pass repairs via Create.
func (p *NetworkProvider) Update(ctx context.Context, res *api.Resource) error {
	if err := p.Delete(ctx, res); err != nil {
		return err
	}
	return p.Create(ctx, res)
}

// Delete removes the owned network(s). Idempotent: a resource whose network
// is already gone returns nil. A network with containers still attached
// cannot be removed by Docker (403); that comes back as a retryable error the
// reconciler surfaces as a pending-deletion condition, not a hard failure —
// deletion converges once the containers detach.
func (p *NetworkProvider) Delete(ctx context.Context, res *api.Resource) error {
	list, err := p.cli.NetworkList(ctx, network.ListOptions{Filters: ownedBy(res)})
	if err != nil {
		return fmt.Errorf("list networks: %w", err)
	}
	for _, n := range list {
		switch err := p.cli.NetworkRemove(ctx, n.ID); {
		case err == nil:
		case errdefs.IsNotFound(err):
			// Already gone (removed out-of-band between list and remove).
		case errdefs.IsForbidden(err):
			return fmt.Errorf("network %s has containers attached; waiting for them to disconnect", shortID(n.ID))
		default:
			return fmt.Errorf("remove network %s: %w", shortID(n.ID), err)
		}
	}
	return nil
}

func decodeNetworkSpec(res *api.Resource) (networkSpec, error) {
	var spec networkSpec
	if err := json.Unmarshal(res.Spec, &spec); err != nil {
		return spec, fmt.Errorf("decode network spec: %w", err)
	}
	if spec.Driver == "" {
		spec.Driver = defaultNetworkDriver
	}
	return spec, nil
}

// networkName gives the Substrate object a predictable, prefixed name.
// Correlation still happens via labels, never via the name.
func networkName(res *api.Resource) string {
	return "setpoint-" + res.Name
}
