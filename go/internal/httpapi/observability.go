package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"time"
)

func init() {
	registerRoutes(func(s *Server, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/observability/log-export", s.logExportStatus)
	})
}

// logExportStatus reports the optional exporter's state. Absence is a state
// ("not_deployed"), never an error: Kionga runs fully without it.
func (s *Server) logExportStatus(w http.ResponseWriter, r *http.Request) {
	base := os.Getenv("KIONGA_LOG_EXPORTER_URL")
	if base == "" {
		writeJSON(w, http.StatusOK, map[string]any{"state": "not_deployed", "detail": "No log exporter is configured. Logs stay in Kionga; see Operations → Log export to ship them to Elasticsearch or OpenSearch."})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/status", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"state": "not_deployed", "detail": "The log exporter is not running. Start it with: docker compose --profile log-export up -d log-exporter"})
		return
	}
	defer response.Body.Close()
	var status map[string]any
	if json.NewDecoder(response.Body).Decode(&status) != nil {
		writeJSON(w, http.StatusOK, map[string]any{"state": "unavailable", "detail": "The log exporter returned an unreadable status."})
		return
	}
	writeJSON(w, http.StatusOK, status)
}
