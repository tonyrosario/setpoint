// Package server exposes the control plane's single public contract
// (ADR-0007): resource-oriented REST, declarative writes only, async by
// construction. PUT persists Spec and returns 202 Accepted immediately;
// convergence is observed later via GET and Status.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/tonyrosario/setpoint/core/api"
	"github.com/tonyrosario/setpoint/core/store"
)

// actorHeader carries the Actor identity on every write (ADR-0010).
// Recorded, not enforced.
const actorHeader = "X-Setpoint-Actor"

// Server handles the /v1 resource API against a Store. After every accepted
// write it calls nudge so the reconciler sweeps soon; convergence never
// depends on the nudge arriving (the periodic sweep is the safety net).
type Server struct {
	store store.Store
	nudge func()
}

// New builds a Server. nudge may be nil.
func New(s store.Store, nudge func()) *Server {
	if nudge == nil {
		nudge = func() {}
	}
	return &Server{store: s, nudge: nudge}
}

// Handler returns the routed http.Handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/{kind}/{name}", s.put)
	mux.HandleFunc("GET /v1/{kind}/{name}", s.get)
	mux.HandleFunc("GET /v1/{kind}", s.list)
	mux.HandleFunc("DELETE /v1/{kind}/{name}", s.delete)
	return mux
}

// kindParam normalizes the kind path segment to the canonical form:
// lowercase singular. "/v1/containers/web" and "/v1/container/web" address
// the same resource.
func kindParam(r *http.Request) string {
	return normalizeKind(r.PathValue("kind"))
}

// normalizeKind lowercases and singularizes a kind, so "Networks" and
// "network" address the same thing whether they arrive in a URL or a
// reference declaration.
func normalizeKind(kind string) string {
	return strings.TrimSuffix(strings.ToLower(kind), "s")
}

// putBody is what clients send: desired state only. Kind and name come from
// the URL; Status is never writable through the API (ADR-0004).
type putBody struct {
	Spec       json.RawMessage          `json:"spec"`
	References map[string]api.Reference `json:"references,omitempty"`
}

// validateReferences checks the references block against the Spec it
// arrives with (ADR-0012) and returns the normalized block. Malformed input
// is a 400-class error — unlike an unresolvable reference at reconcile
// time, which is never an error. Rules: every reference names a kind, name,
// and field; every $(ref:name) token in the Spec names a declared
// reference. Declared-but-unused references are allowed.
func validateReferences(refs map[string]api.Reference, spec []byte) (map[string]api.Reference, error) {
	var normalized map[string]api.Reference
	if len(refs) > 0 {
		normalized = make(map[string]api.Reference, len(refs))
		for name, ref := range refs {
			if ref.Kind == "" || ref.Name == "" || ref.Field == "" {
				return nil, fmt.Errorf("reference %q: kind, name, and field are all required", name)
			}
			ref.Kind = normalizeKind(ref.Kind)
			normalized[name] = ref
		}
	}
	for _, token := range api.SpecReferenceTokens(spec) {
		if _, ok := normalized[token]; !ok {
			return nil, fmt.Errorf("spec token $(ref:%s) does not match any declared reference", token)
		}
	}
	return normalized, nil
}

func (s *Server) put(w http.ResponseWriter, r *http.Request) {
	var body putBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if len(body.Spec) == 0 {
		httpError(w, http.StatusBadRequest, "spec is required")
		return
	}

	refs, err := validateReferences(body.References, body.Spec)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}

	actor := r.Header.Get(actorHeader)
	if actor == "" {
		actor = "anonymous"
	}

	res := &api.Resource{
		Kind:       kindParam(r),
		Name:       r.PathValue("name"),
		Metadata:   api.Metadata{Actor: actor},
		References: refs,
		Spec:       body.Spec,
	}
	if err := s.store.Put(r.Context(), res); err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.nudge()

	// 202: the write is durable, convergence is pending (ADR-0007).
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{
		"kind": res.Kind, "name": res.Name, "status": "accepted",
	})
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	res, err := s.store.Get(r.Context(), kindParam(r), r.PathValue("name"))
	if errors.Is(err, store.ErrNotFound) {
		httpError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, res)
}

// delete marks a resource for deletion and returns 202. Removal is
// declarative and async (ADR-0007): the reconciler tears down the Substrate
// object and then removes the resource; the caller observes progress via GET
// (Phase Deleting) until the resource disappears.
func (s *Server) delete(w http.ResponseWriter, r *http.Request) {
	kind, name := kindParam(r), r.PathValue("name")
	err := s.store.MarkForDeletion(r.Context(), kind, name)
	if errors.Is(err, store.ErrNotFound) {
		httpError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.nudge()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{
		"kind": kind, "name": name, "status": "deleting",
	})
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.List(r.Context(), kindParam(r))
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if items == nil {
		items = []*api.Resource{}
	}
	writeJSON(w, map[string]any{"items": items})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
