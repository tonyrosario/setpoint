// Package api defines the resource envelope: the public shape of every
// resource the control plane manages. Per ADR-0002, every resource splits
// into Spec (desired state, owned by user writes) and Status (observed
// state, owned by the reconciler).
package api

import (
	"encoding/json"
	"time"
)

// Resource is the envelope for every object in the control plane.
// Spec is kept as a raw document rather than a typed struct so the store
// and API never couple to any resource shape (ADR-0004); Providers decode
// the kinds they own.
type Resource struct {
	Kind     string          `json:"kind"`
	Name     string          `json:"name"`
	Metadata Metadata        `json:"metadata"`
	Spec     json.RawMessage `json:"spec"`
	Status   Status          `json:"status"`
}

// IsMarkedForDeletion reports whether a DELETE has requested this resource
// be removed. Deletion is declarative and async (ADR-0007): the mark is set,
// and the reconciler removes the Substrate object, then the resource itself.
func (r *Resource) IsMarkedForDeletion() bool {
	return !r.Metadata.DeletedAt.IsZero()
}

// Metadata records who touched the resource and when. Actor is recorded on
// every write from day one per ADR-0010; nothing enforces it yet.
type Metadata struct {
	Actor     string    `json:"actor,omitempty"`
	CreatedAt time.Time `json:"createdAt,omitzero"`
	UpdatedAt time.Time `json:"updatedAt,omitzero"`
	// DeletedAt marks the resource for deletion. Once set, the reconciler
	// stops converging Spec and instead drives the resource to removal.
	DeletedAt time.Time `json:"deletedAt,omitzero"`
}

// Status is the observed-state half of the resource. It is a cache of what
// the reconciler last saw in the Substrate (ADR-0004); the reconciler
// overwrites it on every pass.
type Status struct {
	Phase      Phase             `json:"phase,omitempty"`
	Ready      bool              `json:"ready"`
	Message    string            `json:"message,omitempty"`
	Observed   map[string]string `json:"observed,omitempty"`
	ObservedAt time.Time         `json:"observedAt,omitzero"`
}

// Phase is a coarse, human-readable summary of where a resource is in its
// convergence. Readiness (the machine-checked signal dependents wait on)
// is the Ready bool, not the Phase.
type Phase string

const (
	PhasePending  Phase = "Pending"
	PhaseCreating Phase = "Creating"
	PhaseReady    Phase = "Ready"
	PhaseDeleting Phase = "Deleting"
	PhaseError    Phase = "Error"
)
