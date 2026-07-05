// Package store defines the persistence contract for resources (ADR-0004).
// The in-memory implementation bootstraps the project; SQLite is the
// committed destination, and swapping it in must require no changes to
// callers of this interface.
package store

import (
	"context"
	"errors"

	"github.com/tonyrosario/setpoint/core/api"
)

// ErrNotFound is returned when no resource matches the given kind and name.
var ErrNotFound = errors.New("resource not found")

// Store persists resources. Spec is written only via Put (user writes,
// ADR-0004); Status is written only via UpdateStatus (reconciler writes).
type Store interface {
	// Put upserts a resource's Spec and Metadata, preserving any existing
	// Status. Kind and Name identify the resource.
	Put(ctx context.Context, res *api.Resource) error

	// Get returns a copy of the resource, or ErrNotFound.
	Get(ctx context.Context, kind, name string) (*api.Resource, error)

	// List returns copies of all resources of the given kind; an empty
	// kind lists everything.
	List(ctx context.Context, kind string) ([]*api.Resource, error)

	// UpdateStatus overwrites the resource's Status, or ErrNotFound.
	UpdateStatus(ctx context.Context, kind, name string, status api.Status) error

	// MarkForDeletion sets the resource's deletion mark (idempotent), or
	// ErrNotFound. The resource remains readable until the reconciler
	// removes it via Delete.
	MarkForDeletion(ctx context.Context, kind, name string) error

	// Delete hard-removes the resource, or ErrNotFound. Called by the
	// reconciler once the Substrate object is gone.
	Delete(ctx context.Context, kind, name string) error
}
