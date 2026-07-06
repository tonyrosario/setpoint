package reconcile

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tonyrosario/setpoint/core/api"
	"github.com/tonyrosario/setpoint/core/store"
)

// putNetworked stores a container whose Spec wires a network through a
// Reference (ADR-0012).
func putNetworked(t *testing.T, st store.Store, name string) {
	t.Helper()
	err := st.Put(context.Background(), &api.Resource{
		Kind: "container",
		Name: name,
		References: map[string]api.Reference{
			"net": {Kind: "network", Name: "backend", Field: "networkId"},
		},
		Spec: json.RawMessage(`{"image":"nginx","network":"$(ref:net)"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
}

// reconcileOne reconciles a single resource by key. Reference tests stamp
// target statuses by hand, and no provider is registered for the target's
// kind — reconcileAll would visit the target too and overwrite the stamped
// status with a no-provider Error (in whichever map order List returns), so
// these tests reconcile only the resource under test.
func reconcileOne(t *testing.T, rec *Reconciler, kind, name string) {
	t.Helper()
	res, err := rec.store.Get(context.Background(), kind, name)
	if err != nil {
		t.Fatal(err)
	}
	_ = rec.reconcile(context.Background(), res)
}

// makeTargetReady stores the referenced network and stamps a Ready status
// carrying the observed field — resolution reads the Store, not a Provider.
func makeTargetReady(t *testing.T, st store.Store, networkID string) {
	t.Helper()
	ctx := context.Background()
	if err := st.Put(ctx, &api.Resource{Kind: "network", Name: "backend", Spec: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	err := st.UpdateStatus(ctx, "network", "backend", api.Status{
		Phase: api.PhaseReady, Ready: true,
		Observed: map[string]string{"networkId": networkID},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestReferenceDanglingParksPending(t *testing.T) {
	fake := &fakeProvider{kind: "container"}
	rec, st := newTest(fake)
	putNetworked(t, st, "web") // no network resource exists at all

	reconcileAll(rec)

	if fake.created != 0 || fake.sawSpec != nil {
		t.Fatalf("provider was invoked (created=%d) despite unresolvable reference", fake.created)
	}
	got := status(t, st, "web")
	if got.Ready || got.Phase != api.PhasePending {
		t.Errorf("status = %+v, want not-ready Pending (never an error)", got)
	}
	if !strings.Contains(got.Message, "waiting for network/backend") || !strings.Contains(got.Message, "no such resource") {
		t.Errorf("message %q should say what it is waiting for", got.Message)
	}
}

func TestReferenceTargetNotReadyGates(t *testing.T) {
	fake := &fakeProvider{kind: "container"}
	rec, st := newTest(fake)
	putNetworked(t, st, "web")
	// Target exists but is not Ready — and even carries a stale observed
	// value, which must NOT be substituted (readiness gates, not presence).
	ctx := context.Background()
	if err := st.Put(ctx, &api.Resource{Kind: "network", Name: "backend", Spec: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	err := st.UpdateStatus(ctx, "network", "backend", api.Status{
		Phase: api.PhaseCreating, Ready: false,
		Observed: map[string]string{"networkId": "stale111"},
	})
	if err != nil {
		t.Fatal(err)
	}

	reconcileOne(t, rec, "container", "web")

	if fake.created != 0 {
		t.Fatal("provider invoked while target not Ready")
	}
	got := status(t, st, "web")
	if got.Phase != api.PhasePending || !strings.Contains(got.Message, "not ready") {
		t.Errorf("status = %+v, want Pending waiting-not-ready", got)
	}
}

func TestReferenceFieldUnsetGates(t *testing.T) {
	fake := &fakeProvider{kind: "container"}
	rec, st := newTest(fake)
	putNetworked(t, st, "web")
	ctx := context.Background()
	if err := st.Put(ctx, &api.Resource{Kind: "network", Name: "backend", Spec: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	// Ready, but the named field is absent from Observed.
	if err := st.UpdateStatus(ctx, "network", "backend", api.Status{Ready: true, Phase: api.PhaseReady}); err != nil {
		t.Fatal(err)
	}

	reconcileOne(t, rec, "container", "web")

	got := status(t, st, "web")
	if got.Phase != api.PhasePending || !strings.Contains(got.Message, `"networkId" not set`) {
		t.Errorf("status = %+v, want Pending waiting-on-field", got)
	}
}

func TestReferenceResolvesAndSubstitutes(t *testing.T) {
	fake := &fakeProvider{kind: "container"}
	rec, st := newTest(fake)
	putNetworked(t, st, "web")
	makeTargetReady(t, st, "abc123def456")

	reconcileOne(t, rec, "container", "web")

	if fake.created != 1 {
		t.Fatalf("created = %d, want 1 (reference resolved)", fake.created)
	}
	if want := `{"image":"nginx","network":"abc123def456"}`; string(fake.sawSpec) != want {
		t.Errorf("provider saw spec %s, want %s", fake.sawSpec, want)
	}
	// The Store must still hold the user's original bytes — substitution is
	// transient, never persisted.
	res, _ := st.Get(context.Background(), "container", "web")
	if !strings.Contains(string(res.Spec), "$(ref:net)") {
		t.Errorf("stored spec %s lost its placeholder — substitution leaked into the Store", res.Spec)
	}
	if got := status(t, st, "web"); !got.Ready {
		t.Errorf("status = %+v, want Ready", got)
	}
}

func TestReferenceWrongOrderApplyConverges(t *testing.T) {
	// The M1 demo beat: apply the dependent first, the dependency later —
	// ordering emerges with no manual retry (ADR-0005).
	fake := &fakeProvider{kind: "container"}
	rec, st := newTest(fake)
	putNetworked(t, st, "web")

	reconcileAll(rec) // dependency missing → parked Pending
	if got := status(t, st, "web"); got.Phase != api.PhasePending {
		t.Fatalf("status = %+v, want Pending before dependency exists", got)
	}

	makeTargetReady(t, st, "abc123")
	reconcileOne(t, rec, "container", "web") // next pass: resolves and converges

	if fake.created != 1 {
		t.Fatalf("created = %d, want 1 after dependency became Ready", fake.created)
	}
	if got := status(t, st, "web"); !got.Ready {
		t.Errorf("status = %+v, want Ready", got)
	}
}

func TestReferenceUnresolvableRequeuesNotBackoff(t *testing.T) {
	// Waiting on a dependency must use the requeue-after seam, not the
	// error/backoff path: no failure count accrues.
	fake := &fakeProvider{kind: "container"}
	rec, st := newTest(fake)
	putNetworked(t, st, "web")

	key := resourceKey{Kind: "container", Name: "web"}
	rec.queue.enqueue(key)
	got, _ := rec.queue.get()
	rec.process(context.Background(), got)

	if f := rec.queue.limiter.failures(key); f != 0 {
		t.Errorf("failures = %d, want 0 — waiting on a reference is not an error", f)
	}
}

func TestReferenceDeletionIgnoresReferences(t *testing.T) {
	// Deleting a dependent must not be blocked by its (dangling) reference.
	fake := &fakeProvider{kind: "container", exists: true}
	rec, st := newTest(fake)
	putNetworked(t, st, "web")
	if err := st.MarkForDeletion(context.Background(), "container", "web"); err != nil {
		t.Fatal(err)
	}

	reconcileAll(rec)

	if fake.deleted != 1 {
		t.Fatalf("deleted = %d, want 1 (deletion must bypass reference resolution)", fake.deleted)
	}
}

func TestReferenceCycleStallsMutuallyPending(t *testing.T) {
	// Two resources referencing each other stall as mutually not-Ready with
	// legible messages — no detector, no error (ADR-0012).
	fake := &fakeProvider{kind: "container"}
	rec, st := newTest(fake)
	ctx := context.Background()
	put := func(name, other string) {
		err := st.Put(ctx, &api.Resource{
			Kind: "container", Name: name,
			References: map[string]api.Reference{
				"peer": {Kind: "container", Name: other, Field: "containerId"},
			},
			Spec: json.RawMessage(`{"image":"nginx","network":"$(ref:peer)"}`),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	put("a", "b")
	put("b", "a")

	reconcileAll(rec)
	reconcileAll(rec)

	for _, name := range []string{"a", "b"} {
		got := status(t, st, name)
		if got.Phase != api.PhasePending || got.Ready {
			t.Errorf("%s status = %+v, want Pending (cycle stalls, never errors)", name, got)
		}
	}
	if fake.created != 0 {
		t.Errorf("created = %d, want 0 (nothing in a cycle converges)", fake.created)
	}
}
