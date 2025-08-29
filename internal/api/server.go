package api

import (
	"context"
	"log"
	"net/http"

	"linkwatch/internal/storage"
)

// Server wraps the http.Server to provide graceful shutdown.
type Server struct {
	httpServer *http.Server
}

// NewServer creates and configures a new API server.
func NewServer(port string, store storage.Storer) *Server {
	router := NewRouter(store)
	return &Server{
		httpServer: &http.Server{
			Addr:    ":" + port,
			Handler: router,
		},
	}
}

// Start runs the HTTP server in a new goroutine.
func (s *Server) Start() {
	log.Printf("starting HTTP server on port %s", s.httpServer.Addr)
	go func() {
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("could not start HTTP server: %v", err)
		}
	}()
}

// Shutdown gracefully shuts down the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	log.Println("shutting down HTTP server...")
	return s.httpServer.Shutdown(ctx)
}
