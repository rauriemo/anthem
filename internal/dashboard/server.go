package dashboard

import "net/http"

// Server is the embedded HTTP server for the web dashboard.
// Stub for Phase 3 implementation.
type Server struct {
	mux  *http.ServeMux
	port int
}

func New(port int) *Server {
	return &Server{
		mux:  http.NewServeMux(),
		port: port,
	}
}
