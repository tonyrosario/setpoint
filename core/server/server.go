// Package server exposes the control plane's single public contract
// (ADR-0007): resource-oriented REST, declarative writes only, async by
// construction. PUT persists Spec and returns 202 Accepted immediately;
// convergence is observed later via GET and Status.
package server

import (
	"encoding/json"
	"errors"
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
	return mux
}

// kindParam normalizes the kind path segment to the canonical form:
// lowercase singular. "/v1/containers/web" and "/v1/container/web" address
// the same resource.
func kindParam(r *http.Request) string {
	kind := strings.ToLower(r.PathValue("kind"))
	return strings.TrimSuffix(kind, "s")
}

// putBody is what clients send: desired state only. Kind and name come from
// the URL; Status is never writable through the API (ADR-0004).
type putBody struct {
	Spec json.RawMessage `json:"spec"`
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

	actor := r.Header.Get(actorHeader)
	if actor == "" {
		actor = "anonymous"
	}

	res := &api.Resource{
		Kind:     kindParam(r),
		Name:     r.PathValue("name"),
		Metadata: api.Metadata{Actor: actor},
		Spec:     body.Spec,
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
