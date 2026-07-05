package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tonyrosario/setpoint/core/api"
	"github.com/tonyrosario/setpoint/core/store"
)

func newTestServer(t *testing.T) (*httptest.Server, *store.Memory, *int) {
	t.Helper()
	st := store.NewMemory()
	nudges := 0
	srv := httptest.NewServer(New(st, func() { nudges++ }).Handler())
	t.Cleanup(srv.Close)
	return srv, st, &nudges
}

func doPut(t *testing.T, url, body, actor string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if actor != "" {
		req.Header.Set("X-Setpoint-Actor", actor)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestPutAccepts(t *testing.T) {
	srv, st, nudges := newTestServer(t)

	resp := doPut(t, srv.URL+"/v1/containers/web", `{"spec":{"image":"nginx:alpine"}}`, "tony")
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	if *nudges != 1 {
		t.Errorf("nudges = %d, want 1", *nudges)
	}

	// Plural URL segment normalizes to canonical lowercase-singular kind.
	res, err := st.Get(context.Background(), "container", "web")
	if err != nil {
		t.Fatalf("stored resource: %v", err)
	}
	if res.Metadata.Actor != "tony" {
		t.Errorf("actor = %q, want tony (ADR-0010)", res.Metadata.Actor)
	}
}

func TestPutDefaultsActor(t *testing.T) {
	srv, st, _ := newTestServer(t)
	doPut(t, srv.URL+"/v1/container/web", `{"spec":{"image":"nginx"}}`, "")

	res, _ := st.Get(context.Background(), "container", "web")
	if res.Metadata.Actor != "anonymous" {
		t.Errorf("actor = %q, want anonymous", res.Metadata.Actor)
	}
}

func TestPutRejectsBadJSON(t *testing.T) {
	srv, _, nudges := newTestServer(t)
	resp := doPut(t, srv.URL+"/v1/containers/web", `{not json`, "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if *nudges != 0 {
		t.Error("rejected write still nudged the reconciler")
	}
}

func TestPutRequiresSpec(t *testing.T) {
	srv, _, _ := newTestServer(t)
	resp := doPut(t, srv.URL+"/v1/containers/web", `{}`, "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestGetNotFound(t *testing.T) {
	srv, _, _ := newTestServer(t)
	resp, err := http.Get(srv.URL + "/v1/containers/nope")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestGetReturnsSpecAndStatus(t *testing.T) {
	srv, st, _ := newTestServer(t)
	doPut(t, srv.URL+"/v1/containers/web", `{"spec":{"image":"nginx"}}`, "")
	st.UpdateStatus(context.Background(), "container", "web",
		api.Status{Phase: api.PhaseReady, Ready: true})

	resp, err := http.Get(srv.URL + "/v1/containers/web")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var res api.Resource
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatal(err)
	}
	if !res.Status.Ready {
		t.Errorf("status not returned: %+v", res.Status)
	}
	var spec map[string]string
	json.Unmarshal(res.Spec, &spec)
	if spec["image"] != "nginx" {
		t.Errorf("spec = %v, want image nginx", spec)
	}
}

func TestListEmptyIsEmptyArray(t *testing.T) {
	srv, _, _ := newTestServer(t)
	resp, err := http.Get(srv.URL + "/v1/containers")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var out struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Items == nil {
		t.Error("items is null, want []")
	}
}
