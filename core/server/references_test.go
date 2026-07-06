package server

import (
	"context"
	"net/http"
	"testing"
)

func TestPutPersistsReferences(t *testing.T) {
	srv, st, _ := newTestServer(t)

	body := `{"spec":{"image":"nginx","network":"$(ref:net)"},` +
		`"references":{"net":{"kind":"Networks","name":"backend","field":"networkId"}}}`
	resp := doPut(t, srv.URL+"/v1/containers/web", body, "tony")
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}

	res, err := st.Get(context.Background(), "container", "web")
	if err != nil {
		t.Fatal(err)
	}
	ref, ok := res.References["net"]
	if !ok {
		t.Fatalf("references = %v, want net entry", res.References)
	}
	// Reference kinds normalize exactly like URL kinds.
	if ref.Kind != "network" || ref.Name != "backend" || ref.Field != "networkId" {
		t.Errorf("ref = %+v, want normalized network/backend networkId", ref)
	}
}

func TestPutRejectsUndeclaredToken(t *testing.T) {
	srv, _, nudges := newTestServer(t)

	resp := doPut(t, srv.URL+"/v1/containers/web", `{"spec":{"network":"$(ref:net)"}}`, "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for undeclared $(ref:net)", resp.StatusCode)
	}
	if *nudges != 0 {
		t.Errorf("nudges = %d, want 0 (rejected write must not nudge)", *nudges)
	}
}

func TestPutRejectsIncompleteReference(t *testing.T) {
	srv, _, _ := newTestServer(t)

	body := `{"spec":{"image":"nginx"},"references":{"net":{"kind":"network","name":"backend"}}}`
	resp := doPut(t, srv.URL+"/v1/containers/web", body, "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for reference missing field", resp.StatusCode)
	}
}

func TestPutRejectsUnaddressableAliasName(t *testing.T) {
	srv, _, _ := newTestServer(t)

	// An alias with a space can never be matched by a $(ref:...) token —
	// reject it at the door rather than store inert dead weight.
	body := `{"spec":{"image":"nginx"},"references":{"my alias":{"kind":"network","name":"backend","field":"networkId"}}}`
	resp := doPut(t, srv.URL+"/v1/containers/web", body, "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for unaddressable alias name", resp.StatusCode)
	}
}

func TestPutAllowsDeclaredButUnusedReference(t *testing.T) {
	srv, _, _ := newTestServer(t)

	body := `{"spec":{"image":"nginx"},"references":{"net":{"kind":"network","name":"backend","field":"networkId"}}}`
	resp := doPut(t, srv.URL+"/v1/containers/web", body, "")
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (unused reference is allowed)", resp.StatusCode)
	}
}
