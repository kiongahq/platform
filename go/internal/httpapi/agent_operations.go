package httpapi

import (
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
)

// agentOperations is the operations dashboard for one deployed agent: KPIs,
// time series and percentiles computed from the turn traces the runtime
// reports, the rollout timeline from the audit log, and the other
// deployments of the same native design for canary and rollback. Every value
// comes from recorded data; a window without traffic reports zeros and nulls.

type opsBucket struct {
	Start        time.Time `json:"start"`
	Requests     int       `json:"requests"`
	Errors       int       `json:"errors"`
	P50MS        *int64    `json:"p50_ms"`
	P95MS        *int64    `json:"p95_ms"`
	P99MS        *int64    `json:"p99_ms"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	CostUSD      float64   `json:"cost_usd"`
}

type opsEvent struct {
	At       time.Time      `json:"at"`
	Action   string         `json:"action"`
	Actor    string         `json:"actor"`
	Details  map[string]any `json:"details,omitempty"`
	AgentID  string         `json:"agent_id"`
	Version  string         `json:"version,omitempty"`
	Resource string         `json:"resource"`
}

func pointer(value int64) *int64 { return &value }

func percentiles(durations []int64) (p50, p95, p99 *int64) {
	if len(durations) == 0 {
		return nil, nil, nil
	}
	return pointer(percentile(durations, 0.50)), pointer(percentile(durations, 0.95)), pointer(percentile(durations, 0.99))
}

func (s *Server) agentOperations(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !agentAllowed(s.store, principal(r), id) {
		writeError(w, http.StatusNotFound, "not_found", "agent not found")
		return
	}
	hours := 24
	if value := r.URL.Query().Get("hours"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 168 {
			writeError(w, 422, "validation_error", "hours must be 1-168")
			return
		}
		hours = parsed
	}
	var agent api.Agent
	agents := s.store.Agents()
	for _, item := range agents {
		if item.ID == id {
			agent = item
		}
	}
	live := []api.Agent{agent}
	enrichAgentRuntimeReadiness(r.Context(), live)
	agent = live[0]
	agent.FlowJSON = ""

	now := time.Now().UTC()
	const bucketCount = 24
	width := time.Duration(hours) * time.Hour / bucketCount
	start := now.Add(-time.Duration(hours) * time.Hour)
	buckets := make([]opsBucket, bucketCount)
	durations := make([][]int64, bucketCount)
	for index := range buckets {
		buckets[index].Start = start.Add(time.Duration(index) * width)
	}
	all, recent := []int64{}, []api.AgentTrace{}
	totals := opsBucket{Start: start}
	for _, trace := range s.store.AgentTraces(id) {
		if trace.CreatedAt.Before(start) || trace.CreatedAt.After(now) {
			continue
		}
		index := min(int(trace.CreatedAt.Sub(start)/width), bucketCount-1)
		bucket := &buckets[index]
		failed := trace.Status != "succeeded"
		for _, target := range []*opsBucket{bucket, &totals} {
			target.Requests++
			if failed {
				target.Errors++
			}
			target.InputTokens += trace.InputTokens
			target.OutputTokens += trace.OutputTokens
			target.CostUSD += trace.CostUSD
		}
		durations[index] = append(durations[index], trace.DurationMS)
		all = append(all, trace.DurationMS)
		recent = append(recent, trace)
	}
	for index := range buckets {
		buckets[index].P50MS, buckets[index].P95MS, buckets[index].P99MS = percentiles(durations[index])
		buckets[index].CostUSD = math.Round(buckets[index].CostUSD*1e6) / 1e6
	}
	totals.P50MS, totals.P95MS, totals.P99MS = percentiles(all)
	totals.CostUSD = math.Round(totals.CostUSD*1e6) / 1e6
	var successRate any
	if totals.Requests > 0 {
		successRate = float64(totals.Requests-totals.Errors) / float64(totals.Requests)
	}
	sort.Slice(recent, func(i, j int) bool { return recent[i].CreatedAt.After(recent[j].CreatedAt) })
	if len(recent) > 25 {
		recent = recent[:25]
	}

	// Siblings: other deployments of the same native design (rollback and
	// canary targets), found through their published versions.
	designID := ""
	if version, ok := s.versionByID(agent.SnapshotID); ok {
		designID = version.DesignID
	}
	siblings := []map[string]any{}
	related := map[string]bool{id: true}
	for _, other := range agents {
		if other.ID == id || other.GraphModule != nativeGraphModule || designID == "" {
			continue
		}
		if version, ok := s.versionByID(other.SnapshotID); ok && version.DesignID == designID {
			related[other.ID] = true
			siblings = append(siblings, map[string]any{"id": other.ID, "name": other.Name, "version": other.Version,
				"version_id": other.SnapshotID, "canary_weight": other.CanaryWeight, "created_at": other.CreatedAt})
		}
	}
	timeline := []opsEvent{}
	for _, event := range s.store.Audit() {
		if !related[event.ResourceID] || !strings.HasPrefix(event.Action, "agent.") || event.Action == "agent.trace_recorded" || event.Action == "agent.session_updated" {
			continue
		}
		version := ""
		for _, other := range agents {
			if other.ID == event.ResourceID {
				version = other.Version
			}
		}
		timeline = append(timeline, opsEvent{At: event.CreatedAt, Action: event.Action, Actor: event.Actor, Details: event.Metadata,
			AgentID: event.ResourceID, Version: version, Resource: event.Resource})
	}
	sort.Slice(timeline, func(i, j int) bool { return timeline[i].At.After(timeline[j].At) })
	if len(timeline) > 50 {
		timeline = timeline[:50]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"agent": agent, "window_hours": hours, "bucket_seconds": int(width.Seconds()),
		"totals": totals, "success_rate": successRate, "series": buckets,
		"recent_traces": recent, "timeline": timeline, "siblings": siblings,
	})
}

func (s *Server) versionByID(id string) (agentVersion, bool) {
	if !strings.HasPrefix(id, "agv-") {
		return agentVersion{}, false
	}
	version, err := store.GetDoc[agentVersion](s.store, agentVersionKind, id)
	return version, err == nil
}

func init() {
	registerRoutes(func(s *Server, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/agents/{id}/operations", s.agentOperations)
	})
}
