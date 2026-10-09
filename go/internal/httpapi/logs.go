package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ml-ai-ops/platform/internal/redact"
	"github.com/ml-ai-ops/platform/internal/store"
	"github.com/ml-ai-ops/platform/pkg/api"
)

func init() {
	registerRoutes(func(s *Server, mux *http.ServeMux) {
		mux.HandleFunc("POST /api/v1/pipelines/runs/{id}/logs", s.ingestRunLogs)
		mux.HandleFunc("GET /api/v1/logs", s.queryLogs)
		mux.HandleFunc("GET /api/v1/logs/stream", s.streamLogs)
	})
}

const (
	maxIngestEntries = 500
	maxLogMessage    = 8 * 1024
)

var logSeverities = map[string]bool{"debug": true, "info": true, "warn": true, "error": true}
var logSources = map[string]bool{"runner": true, "engine": true, "platform": true, "k8s": true, "audit": true}

func (s *Server) logStore() store.LogStore {
	logs, _ := s.store.(store.LogStore)
	return logs
}

// appendLogs redacts and stores entries; failures are logged, never fatal,
// because logging must not break execution.
func (s *Server) appendLogs(entries []api.LogEntry) {
	logs := s.logStore()
	if logs == nil || len(entries) == 0 {
		return
	}
	for i := range entries {
		entries[i].Message = redact.String(truncateMessage(entries[i].Message))
		if entries[i].Timestamp.IsZero() {
			entries[i].Timestamp = time.Now().UTC()
		}
	}
	if err := logs.AppendLogs(entries); err != nil {
		log.Printf("append %d log entries: %v", len(entries), err)
	}
}

func truncateMessage(message string) string {
	if len(message) <= maxLogMessage {
		return message
	}
	return message[:maxLogMessage] + " …[truncated]"
}

// reportStep applies a step transition and records it as a platform event,
// so node history and node output share one timeline.
func (s *Server) reportStep(runID string, req api.UpdateRunStepRequest, actor string) (api.PipelineRun, error) {
	run, err := s.store.UpdateRunStep(runID, req, actor)
	if err != nil {
		return run, err
	}
	severity := "info"
	if req.Status == "failed" {
		severity = "error"
	}
	message := fmt.Sprintf("%s → %s", req.Step, req.Status)
	if req.Message != "" && req.Status != "succeeded" {
		message += ": " + lastLine(req.Message)
	}
	entry := api.LogEntry{ProjectID: run.ProjectID, PipelineID: run.DefinitionID, RunID: run.ID, Node: req.Step, Attempt: req.Attempt,
		WorkloadKind: req.WorkloadKind, WorkloadID: req.WorkloadID, Source: "platform", Severity: severity, Message: message,
		Context: map[string]any{"status": req.Status}}
	if req.ExitCode != nil {
		entry.Context["exit_code"] = *req.ExitCode
	}
	if req.At != nil {
		entry.Timestamp = req.At.UTC()
	}
	s.appendLogs([]api.LogEntry{entry})
	return run, nil
}

func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	return lines[len(lines)-1]
}

// ingestRunLogs accepts runner output for one run. Only machine identities
// reach it (auth.machineReportingPath blocks users), and every entry is
// attributed to the run's project, never to caller-supplied scope.
func (s *Server) ingestRunLogs(w http.ResponseWriter, r *http.Request) {
	run, err := s.store.Run(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "run not found")
		return
	}
	var body api.IngestLogsRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if len(body.Entries) > maxIngestEntries {
		writeError(w, http.StatusRequestEntityTooLarge, "too_many_entries", fmt.Sprintf("send at most %d entries per request", maxIngestEntries))
		return
	}
	entries := make([]api.LogEntry, 0, len(body.Entries))
	for _, item := range body.Entries {
		severity := strings.ToLower(item.Severity)
		if !logSeverities[severity] {
			severity = "info"
			if item.Stream == "stderr" {
				severity = "warn"
			}
		}
		entry := api.LogEntry{ProjectID: run.ProjectID, PipelineID: run.DefinitionID, RunID: run.ID, Node: item.Node, Attempt: item.Attempt,
			WorkloadKind: item.WorkloadKind, WorkloadID: item.WorkloadID, Source: "runner", Severity: severity, Message: item.Message, Context: item.Context}
		if item.Stream != "" {
			if entry.Context == nil {
				entry.Context = map[string]any{}
			}
			entry.Context["stream"] = item.Stream
		}
		if item.Timestamp != nil {
			entry.Timestamp = item.Timestamp.UTC()
		}
		entries = append(entries, entry)
	}
	s.appendLogs(entries)
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": len(entries)})
}

// logFilterFor builds a filter from the query string and restricts it to the
// caller's readable projects. It returns ok=false when the caller asked for a
// run or project they cannot read.
func (s *Server) logFilterFor(r *http.Request) (api.LogFilter, int, string) {
	q := r.URL.Query()
	filter := api.LogFilter{RunID: q.Get("run_id"), Node: q.Get("node"), PipelineID: q.Get("pipeline_id"), Query: strings.TrimSpace(q.Get("q"))}
	for _, source := range splitList(q.Get("source")) {
		if logSources[source] {
			filter.Sources = append(filter.Sources, source)
		}
	}
	for _, severity := range splitList(q.Get("severity")) {
		if logSeverities[severity] {
			filter.Severities = append(filter.Severities, severity)
		}
	}
	for name, target := range map[string]**time.Time{"since": &filter.Since, "until": &filter.Until} {
		if value := q.Get(name); value != "" {
			parsed, err := time.Parse(time.RFC3339, value)
			if err != nil {
				return filter, http.StatusBadRequest, name + " must be an RFC 3339 timestamp"
			}
			*target = &parsed
		}
	}
	filter.After, _ = strconv.ParseInt(q.Get("after"), 10, 64)
	filter.Before, _ = strconv.ParseInt(q.Get("before"), 10, 64)
	filter.Limit, _ = strconv.Atoi(q.Get("limit"))
	filter.Descending = q.Get("order") == "desc"

	caller := principal(r)
	allowed := allowedProjectIDs(s.store, caller)
	if filter.RunID != "" {
		run, err := s.store.Run(filter.RunID)
		if err != nil || !projectAllowed(s.store, caller, run.ProjectID) {
			return filter, http.StatusNotFound, "run not found"
		}
	}
	if project := q.Get("project_id"); project != "" {
		if !projectAllowed(s.store, caller, project) {
			return filter, http.StatusNotFound, "project not found"
		}
		filter.ProjectIDs = []string{project}
	} else if allowed != nil {
		filter.ProjectIDs = make([]string, 0, len(allowed))
		for id := range allowed {
			filter.ProjectIDs = append(filter.ProjectIDs, id)
		}
		if len(filter.ProjectIDs) == 0 {
			filter.ProjectIDs = []string{"\x00none"}
		}
	}
	return filter, 0, ""
}

func splitList(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(strings.ToLower(item)); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func (s *Server) queryLogs(w http.ResponseWriter, r *http.Request) {
	logs := s.logStore()
	if logs == nil {
		writeError(w, http.StatusNotImplemented, "logs_unavailable", "This repository does not store logs.")
		return
	}
	filter, status, message := s.logFilterFor(r)
	if status != 0 {
		writeError(w, status, "invalid_request", message)
		return
	}
	entries, err := logs.QueryLogs(filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	response := map[string]any{"items": entries}
	if len(entries) == filter.Limit || (filter.Limit == 0 && len(entries) == 200) {
		last := entries[len(entries)-1].Sequence
		if filter.Descending {
			response["next_before"] = last
		} else {
			response["next_after"] = last
		}
	}
	writeJSON(w, http.StatusOK, response)
}

// streamLogs tails matching entries over SSE. The cursor is the last
// sequence sent, so a slow client never loses entries: each tick sends at
// most one bounded page and resumes from where it stopped (backpressure by
// pull). Clients reconnect with Last-Event-ID.
func (s *Server) streamLogs(w http.ResponseWriter, r *http.Request) {
	logs := s.logStore()
	flusher, ok := w.(http.Flusher)
	if logs == nil || !ok {
		writeError(w, http.StatusNotImplemented, "streaming_unavailable", "Log streaming is not available.")
		return
	}
	filter, status, message := s.logFilterFor(r)
	if status != 0 {
		writeError(w, status, "invalid_request", message)
		return
	}
	if last, err := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64); err == nil && last > filter.After {
		filter.After = last
	}
	filter.Limit, filter.Descending = 200, false
	if controller := http.NewResponseController(w); controller != nil {
		_ = controller.SetWriteDeadline(time.Time{})
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	heartbeat := time.Now()
	for {
		entries, err := logs.QueryLogs(filter)
		if err != nil {
			fmt.Fprintf(w, "event: error\ndata: %q\n\n", "log query failed")
			flusher.Flush()
			return
		}
		if len(entries) > 0 {
			payload, _ := json.Marshal(entries)
			filter.After = entries[len(entries)-1].Sequence
			fmt.Fprintf(w, "id: %d\nevent: logs\ndata: %s\n\n", filter.After, payload)
			flusher.Flush()
			heartbeat = time.Now()
			if len(entries) == filter.Limit {
				continue // more are waiting; send the next page immediately
			}
		} else if time.Since(heartbeat) > 15*time.Second {
			fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
			heartbeat = time.Now()
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

// StartLogRetention deletes log entries older than KIONGA_LOG_RETENTION_DAYS
// (default 30) every hour.
func StartLogRetention(ctx context.Context, data store.Repository) {
	logs, ok := data.(store.LogStore)
	if !ok {
		return
	}
	days := 30
	if value, err := strconv.Atoi(os.Getenv("KIONGA_LOG_RETENTION_DAYS")); err == nil && value > 0 {
		days = value
	}
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			if purged, err := logs.PurgeLogs(time.Now().Add(-time.Duration(days) * 24 * time.Hour)); err != nil {
				log.Printf("log retention: %v", err)
			} else if purged > 0 {
				log.Printf("log retention removed %d entries older than %d days", purged, days)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
