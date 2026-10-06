package httpapi

import (
	"context"
	"net/http"
	"time"
)

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if database, ok := s.store.(interface{ Ping(context.Context) error }); ok {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if database.Ping(ctx) != nil {
			writeError(w, 503, "not_ready", "Database is unavailable")
			return
		}
	}
	writeJSON(w, 200, map[string]string{"status": "ready"})
}
