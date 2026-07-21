package store

import "testing"

// The behavioral spec lives in RunStoreConformance (conformance_test.go), run
// here against the in-memory store and in sqlite_test.go against SQLite.
func TestMemoryConformance(t *testing.T) {
	RunStoreConformance(t, func(t *testing.T) Store {
		return NewMemory()
	})
}
