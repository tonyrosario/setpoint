package store

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/tonyrosario/setpoint/core/api"
)

// Memory is the bootstrap Store implementation (ADR-0004): a mutex-guarded
// map. Resources are copied on the way in and out so callers can never
// mutate stored state through a shared pointer.
type Memory struct {
	mu    sync.RWMutex
	items map[string]*api.Resource
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{items: make(map[string]*api.Resource)}
}

func key(kind, name string) string { return kind + "/" + name }

func (m *Memory) Put(ctx context.Context, res *api.Resource) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	cp := copyResource(res)
	now := time.Now().UTC()
	if existing, ok := m.items[key(res.Kind, res.Name)]; ok {
		cp.Status = existing.Status
		cp.Metadata.CreatedAt = existing.Metadata.CreatedAt
		// Preserve the deletion mark: re-applying a Spec must not
		// resurrect a resource already being torn down.
		cp.Metadata.DeletedAt = existing.Metadata.DeletedAt
	} else {
		cp.Metadata.CreatedAt = now
	}
	cp.Metadata.UpdatedAt = now
	m.items[key(res.Kind, res.Name)] = cp
	return nil
}

func (m *Memory) Get(ctx context.Context, kind, name string) (*api.Resource, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	res, ok := m.items[key(kind, name)]
	if !ok {
		return nil, ErrNotFound
	}
	return copyResource(res), nil
}

func (m *Memory) List(ctx context.Context, kind string) ([]*api.Resource, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []*api.Resource
	for _, res := range m.items {
		if kind == "" || res.Kind == kind {
			out = append(out, copyResource(res))
		}
	}
	return out, nil
}

func (m *Memory) UpdateStatus(ctx context.Context, kind, name string, status api.Status) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	res, ok := m.items[key(kind, name)]
	if !ok {
		return ErrNotFound
	}
	// Copy the caller's Status (including its Observed map) so the stored
	// entry never shares a map reference with the caller — the same
	// copy-in/copy-out invariant Put honours via copyResource.
	res.Status = copyStatus(status)
	return nil
}

func (m *Memory) MarkForDeletion(ctx context.Context, kind, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	res, ok := m.items[key(kind, name)]
	if !ok {
		return ErrNotFound
	}
	if res.Metadata.DeletedAt.IsZero() {
		res.Metadata.DeletedAt = time.Now().UTC()
	}
	return nil
}

func (m *Memory) Delete(ctx context.Context, kind, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.items[key(kind, name)]; !ok {
		return ErrNotFound
	}
	delete(m.items, key(kind, name))
	return nil
}

func copyResource(res *api.Resource) *api.Resource {
	cp := *res
	cp.Spec = json.RawMessage(append([]byte(nil), res.Spec...))
	cp.Status = copyStatus(res.Status)
	return &cp
}

// copyStatus returns a Status whose Observed map is a fresh copy, so the
// result shares no mutable reference with the input.
func copyStatus(status api.Status) api.Status {
	if status.Observed != nil {
		observed := make(map[string]string, len(status.Observed))
		for k, v := range status.Observed {
			observed[k] = v
		}
		status.Observed = observed
	}
	return status
}
