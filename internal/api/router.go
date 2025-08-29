package api

import (
	"net/http"

	"linkwatch/internal/storage"
)

// NewRouter creates a new http.ServeMux and registers the API handlers.
func NewRouter(store storage.Storer) *http.ServeMux {
	mux := http.NewServeMux()
	h := NewHandlers(store)

	mux.HandleFunc("POST /v1/targets", h.CreateTarget)
	mux.HandleFunc("GET /v1/targets", h.ListTargets)
	mux.HandleFunc("GET /v1/targets/{target_id}/results", h.ListCheckResults)
	mux.HandleFunc("GET /healthz", h.Healthz)

	return mux
}
