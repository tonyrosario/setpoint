package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tonyrosario/setpoint/core/api"
)

func newSQLiteStore(t *testing.T) Store {
	t.Helper()
	s, err := NewSQLite(":memory:")
	if err != nil {
		t.Fatalf("new sqlite: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSQLiteConformance(t *testing.T) {
	RunStoreConformance(t, newSQLiteStore)
}

// TestSQLitePersistsAcrossReopen is the point of the whole milestone: state
// written by one process is there for the next. Reconciler-owned Status must
// survive the round trip too, not just user-owned Spec.
func TestSQLitePersistsAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "setpoint.db")

	first, err := NewSQLite(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	res := testResource("web")
	res.References = map[string]api.Reference{
		"db": {Kind: "container", Name: "postgres", Field: "address"},
	}
	if err := first.Put(ctx, res); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := first.UpdateStatus(ctx, "container", "web", api.Status{
		Phase: api.PhaseReady, Ready: true, Observed: map[string]string{"id": "abc123"},
	}); err != nil {
		t.Fatalf("update status: %v", err)
	}
	created := mustGet(t, first, "web").Metadata.CreatedAt
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second, err := NewSQLite(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { second.Close() })

	got := mustGet(t, second, "web")
	if !got.Status.Ready || got.Status.Observed["id"] != "abc123" {
		t.Errorf("status not durable across reopen: %+v", got.Status)
	}
	if got.References["db"].Name != "postgres" {
		t.Errorf("references not durable across reopen: %+v", got.References)
	}
	if string(got.Spec) != `{"image":"nginx:alpine"}` {
		t.Errorf("spec not durable across reopen: %s", got.Spec)
	}
	if !got.Metadata.CreatedAt.Equal(created) {
		t.Errorf("CreatedAt shifted across reopen: got %v, want %v", got.Metadata.CreatedAt, created)
	}
}

// TestSQLiteRefusesNewerSchema guards forward compatibility: a database stamped
// by a future, unknown schema must be refused, not silently opened.
func TestSQLiteRefusesNewerSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "future.db")

	s, err := NewSQLite(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, "PRAGMA user_version=999"); err != nil {
		t.Fatalf("bump user_version: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if _, err := NewSQLite(path); err == nil {
		t.Fatal("opened a database with a newer schema version; want refusal")
	}
}

// TestSQLiteHandlesSpecialCharPath guards the DSN percent-encoding: a path
// containing URI metacharacters (a space here) must open at the intended file
// and still round-trip a resource, not silently truncate the DSN or drop the
// safety pragmas.
func TestSQLiteHandlesSpecialCharPath(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "set point #1.db")

	s, err := NewSQLite(path)
	if err != nil {
		t.Fatalf("open %q: %v", path, err)
	}
	t.Cleanup(func() { s.Close() })

	if err := s.Put(ctx, testResource("web")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("database not created at intended path: %v", err)
	}
	if got := mustGet(t, s, "web"); got.Metadata.Actor != "tester" {
		t.Errorf("round-trip failed on special-char path: %+v", got.Metadata)
	}
}

// TestSQLiteReopenIsIdempotent proves CREATE TABLE IF NOT EXISTS and the
// version stamp tolerate reopening an already-initialised database.
func TestSQLiteReopenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idem.db")
	for i := 0; i < 3; i++ {
		s, err := NewSQLite(path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("close %d: %v", i, err)
		}
	}
}

func mustGet(t *testing.T, s Store, name string) *api.Resource {
	t.Helper()
	got, err := s.Get(context.Background(), "container", name)
	if err != nil {
		t.Fatalf("get %s: %v", name, err)
	}
	return got
}
