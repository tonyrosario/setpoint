// Package api defines the resource envelope: the public shape of every
// resource the control plane manages. Per ADR-0002, every resource splits
// into Spec (desired state, owned by user writes) and Status (observed
// state, owned by the reconciler).
package api

import (
	"encoding/json"
	"regexp"
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
	// References name other resources' observed Status values this
	// resource depends on (ADR-0012). Envelope structure, not Spec: the
	// core resolves them generically and never parses Spec to do so.
	// Written only by the user, like Spec.
	References map[string]Reference `json:"references,omitempty"`
	Spec       json.RawMessage      `json:"spec"`
	Status     Status               `json:"status"`
}

// Reference points at a value in another resource's Status.Observed map.
// It resolves only while the target exists, is Ready, and carries a
// non-empty value at Field; otherwise the referring resource is not Ready
// and is requeued — never an error (ADR-0005).
type Reference struct {
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	Field string `json:"field"`
}

// refToken matches $(ref:name) tokens inside Spec string values. Names use
// the same charset resource names do.
var refToken = regexp.MustCompile(`\$\(ref:([A-Za-z0-9_.-]+)\)`)

// SpecReferenceTokens returns the reference names used by $(ref:name)
// tokens in spec, in order of appearance (duplicates preserved).
func SpecReferenceTokens(spec []byte) []string {
	var names []string
	for _, m := range refToken.FindAllSubmatch(spec, -1) {
		names = append(names, string(m[1]))
	}
	return names
}

// SubstituteReferences replaces every $(ref:name) token in spec with
// values[name], JSON-string-escaped so the splice can never break the
// document. Tokens whose name has no value are left untouched — callers
// substitute only once every declared reference has resolved.
func SubstituteReferences(spec []byte, values map[string]string) json.RawMessage {
	return refToken.ReplaceAllFunc(spec, func(m []byte) []byte {
		name := string(refToken.FindSubmatch(m)[1])
		v, ok := values[name]
		if !ok {
			return m
		}
		b, err := json.Marshal(v)
		if err != nil {
			return m
		}
		return b[1 : len(b)-1] // strip the quotes json.Marshal adds
	})
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
