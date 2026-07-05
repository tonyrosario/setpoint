// Package docker implements the Provider contract (ADR-0001) for the Docker
// Substrate. It correlates Substrate state back to resources exclusively
// through ownership labels: it only ever observes or touches containers it
// stamped at creation.
package docker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"

	"github.com/tonyrosario/setpoint/core/api"
	"github.com/tonyrosario/setpoint/core/provider"
)

const (
	// KindContainer is the resource kind this Provider owns.
	KindContainer = "container"

	labelOwner    = "setpoint.io/owner"
	labelName     = "setpoint.io/resource-name"
	labelKind     = "setpoint.io/resource-kind"
	labelSpecHash = "setpoint.io/spec-hash"
	ownerValue    = "setpoint"
)

// containerSpec is the decoded Spec for the container kind. M0 slice 1 is
// the minimal happy path: an image to run.
type containerSpec struct {
	Image string `json:"image"`
}

// Provider manages containers through the Docker Engine API.
type Provider struct {
	cli client.APIClient
}

// New connects to the Docker daemon from the environment, negotiating the
// API version with the daemon.
func New() (*Provider, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &Provider{cli: cli}, nil
}

// NewWithClient builds a Provider around an existing client (tests).
func NewWithClient(cli client.APIClient) *Provider {
	return &Provider{cli: cli}
}

func (p *Provider) Kinds() []string { return []string{KindContainer} }

// ownedBy returns filters matching only containers this control plane
// created for the given resource.
func ownedBy(res *api.Resource) filters.Args {
	return filters.NewArgs(
		filters.Arg("label", labelOwner+"="+ownerValue),
		filters.Arg("label", labelName+"="+res.Name),
		filters.Arg("label", labelKind+"="+res.Kind),
	)
}

func (p *Provider) Observe(ctx context.Context, res *api.Resource) (provider.Observation, error) {
	list, err := p.cli.ContainerList(ctx, container.ListOptions{
		All:     true, // stopped containers still exist; existence != readiness
		Filters: ownedBy(res),
	})
	if err != nil {
		return provider.Observation{}, fmt.Errorf("list containers: %w", err)
	}
	if len(list) == 0 {
		return provider.Observation{Exists: false}, nil
	}

	c := list[0]
	running := c.State == container.StateRunning
	upToDate := c.Labels[labelSpecHash] == specHash(res)
	obs := provider.Observation{
		Exists:   true,
		Ready:    running,
		UpToDate: upToDate,
		Details: map[string]string{
			"containerId": shortID(c.ID),
			"image":       c.Image,
			"state":       string(c.State),
		},
	}
	switch {
	case !upToDate:
		obs.Message = "container does not match spec; will recreate"
	case !running:
		obs.Message = fmt.Sprintf("container exists but is %s", c.State)
	}
	return obs, nil
}

func (p *Provider) Create(ctx context.Context, res *api.Resource) error {
	spec, err := decodeSpec(res)
	if err != nil {
		return err
	}

	// Pull is idempotent and required for images not present locally.
	reader, err := p.cli.ImagePull(ctx, spec.Image, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("pull image %q: %w", spec.Image, err)
	}
	// The pull only completes when the response stream is drained.
	if _, err := io.Copy(io.Discard, reader); err != nil {
		reader.Close()
		return fmt.Errorf("pull image %q: %w", spec.Image, err)
	}
	reader.Close()

	created, err := p.cli.ContainerCreate(ctx,
		&container.Config{
			Image:  spec.Image,
			Labels: ownershipLabels(res),
		},
		nil, nil, nil,
		containerName(res),
	)
	if err != nil {
		return fmt.Errorf("create container: %w", err)
	}
	if err := p.cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("start container: %w", err)
	}
	return nil
}

// Update converges an existing container toward Spec by delete-and-recreate.
// Docker cannot patch a container's image/env/ports in place, so the only
// honest convergence is to remove the stale container and create a fresh one
// (which re-stamps the current spec-hash label). Idempotent and crash-safe:
// a crash between Delete and Create leaves no owned container, which the next
// reconcile pass repairs via Create.
func (p *Provider) Update(ctx context.Context, res *api.Resource) error {
	if err := p.Delete(ctx, res); err != nil {
		return err
	}
	return p.Create(ctx, res)
}

// Delete force-removes the owned container(s). Idempotent: a resource whose
// container is already gone returns nil. Only containers carrying this
// control plane's ownership labels are ever touched.
func (p *Provider) Delete(ctx context.Context, res *api.Resource) error {
	list, err := p.cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: ownedBy(res),
	})
	if err != nil {
		return fmt.Errorf("list containers: %w", err)
	}
	for _, c := range list {
		// Force stops-and-removes a running container in one call.
		if err := p.cli.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true}); err != nil {
			return fmt.Errorf("remove container %s: %w", shortID(c.ID), err)
		}
	}
	return nil
}

func decodeSpec(res *api.Resource) (containerSpec, error) {
	var spec containerSpec
	if err := json.Unmarshal(res.Spec, &spec); err != nil {
		return spec, fmt.Errorf("decode container spec: %w", err)
	}
	if spec.Image == "" {
		return spec, fmt.Errorf("container spec: image is required")
	}
	return spec, nil
}

func ownershipLabels(res *api.Resource) map[string]string {
	return map[string]string{
		labelOwner:    ownerValue,
		labelName:     res.Name,
		labelKind:     res.Kind,
		labelSpecHash: specHash(res),
	}
}

// specHash fingerprints the resource's Spec. A running container carries the
// hash of the Spec it was created from; when the desired Spec's hash differs,
// the container is out of date and gets recreated. This detects any Spec
// change uniformly, without field-by-field comparison against the Substrate.
//
// The Spec is canonicalized before hashing so that semantically identical
// Specs (differing only in key order or whitespace) hash identically — only
// a real Spec change should trigger a recreate. Invalid JSON falls back to
// the raw bytes.
func specHash(res *api.Resource) string {
	canonical := []byte(res.Spec)
	var v any
	if json.Unmarshal(res.Spec, &v) == nil {
		if b, err := json.Marshal(v); err == nil {
			canonical = b
		}
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

// containerName gives the Substrate object a predictable, prefixed name.
// Correlation still happens via labels, never via the name.
func containerName(res *api.Resource) string {
	return "setpoint-" + res.Name
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
