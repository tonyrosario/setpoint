// Package provider defines the contract between the control plane core and
// the modules that manage external Substrates (ADR-0001). The core talks
// only to this interface; it never knows what sits behind it.
package provider

import (
	"context"

	"github.com/tonyrosario/setpoint/core/api"
)

// Observation is what a Provider saw in the Substrate for one resource.
// Details are string key/values (container ID, state, ...) that the
// reconciler copies verbatim into Status.Observed.
type Observation struct {
	// Exists reports whether a Substrate object for this resource is present.
	Exists bool
	// Ready reports whether the Substrate object is healthy/running.
	Ready bool
	// UpToDate reports whether the existing Substrate object matches the
	// resource's current Spec. When false, the reconciler calls Update to
	// converge it. Meaningless when Exists is false.
	UpToDate bool
	Message  string
	Details  map[string]string
}

// Provider gives the core the ability to observe and mutate one kind of
// external Substrate. Implementations must be safe for concurrent use and
// idempotent: every method can be retried after a crash with no cleanup
// having run.
type Provider interface {
	// Kinds returns the resource kinds this Provider owns
	// (lowercase singular, e.g. "container").
	Kinds() []string

	// Observe reports current Substrate state for the resource. It must
	// only ever look at Substrate objects owned by the control plane.
	Observe(ctx context.Context, res *api.Resource) (Observation, error)

	// Create makes the Substrate object described by res.Spec.
	Create(ctx context.Context, res *api.Resource) error

	// Update converges an existing Substrate object toward res.Spec.
	Update(ctx context.Context, res *api.Resource) error

	// Delete removes the Substrate object.
	Delete(ctx context.Context, res *api.Resource) error
}
