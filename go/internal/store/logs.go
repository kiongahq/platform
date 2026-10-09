package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kiongahq/platform/pkg/api"
)

// LogStore persists structured log entries. Both repository backends
// implement it; the HTTP layer reaches it with a type assertion so the
// Repository interface stays focused on resources.
type LogStore interface {
	AppendLogs(entries []api.LogEntry) error
	QueryLogs(filter api.LogFilter) ([]api.LogEntry, error)
	// PurgeLogs deletes entries older than before and returns the count.
	PurgeLogs(before time.Time) (int64, error)
}

// MaxLogQuery bounds one query page.
const MaxLogQuery = 1000

func normalizeFilter(filter api.LogFilter) api.LogFilter {
	if filter.Limit <= 0 || filter.Limit > MaxLogQuery {
		filter.Limit = 200
	}
	return filter
}

func matchesFilter(entry api.LogEntry, filter api.LogFilter) bool {
	switch {
	case filter.After > 0 && entry.Sequence <= filter.After:
	case filter.Before > 0 && entry.Sequence >= filter.Before:
	case len(filter.ProjectIDs) > 0 && !contains(filter.ProjectIDs, entry.ProjectID):
	case filter.RunID != "" && entry.RunID != filter.RunID:
	case filter.Node != "" && entry.Node != filter.Node:
	case filter.PipelineID != "" && entry.PipelineID != filter.PipelineID:
	case len(filter.Sources) > 0 && !contains(filter.Sources, entry.Source):
	case len(filter.Severities) > 0 && !contains(filter.Severities, entry.Severity):
	case filter.Since != nil && entry.Timestamp.Before(*filter.Since):
	case filter.Until != nil && entry.Timestamp.After(*filter.Until):
	case filter.Query != "" && !strings.Contains(strings.ToLower(entry.Message), strings.ToLower(filter.Query)):
	default:
		return true
	}
	return false
}

func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

// memoryLogs backs the file store. Logs are operational data, so they are
// kept in memory with a bound rather than rewritten into the state file.
type memoryLogs struct {
	mu       sync.RWMutex
	entries  []api.LogEntry
	sequence int64
}

const memoryLogLimit = 50000

func (s *Store) logs() *memoryLogs {
	return &s.logBuffer
}

func (s *Store) AppendLogs(entries []api.LogEntry) error {
	logs := s.logs()
	logs.mu.Lock()
	defer logs.mu.Unlock()
	for _, entry := range entries {
		logs.sequence++
		entry.Sequence = logs.sequence
		logs.entries = append(logs.entries, entry)
	}
	if over := len(logs.entries) - memoryLogLimit; over > 0 {
		logs.entries = append([]api.LogEntry(nil), logs.entries[over:]...)
	}
	return nil
}

func (s *Store) QueryLogs(filter api.LogFilter) ([]api.LogEntry, error) {
	filter = normalizeFilter(filter)
	logs := s.logs()
	logs.mu.RLock()
	defer logs.mu.RUnlock()
	out := []api.LogEntry{}
	if filter.Descending {
		for i := len(logs.entries) - 1; i >= 0 && len(out) < filter.Limit; i-- {
			if matchesFilter(logs.entries[i], filter) {
				out = append(out, logs.entries[i])
			}
		}
		return out, nil
	}
	for _, entry := range logs.entries {
		if len(out) >= filter.Limit {
			break
		}
		if matchesFilter(entry, filter) {
			out = append(out, entry)
		}
	}
	return out, nil
}

func (s *Store) PurgeLogs(before time.Time) (int64, error) {
	logs := s.logs()
	logs.mu.Lock()
	defer logs.mu.Unlock()
	kept := logs.entries[:0]
	var purged int64
	for _, entry := range logs.entries {
		if entry.Timestamp.Before(before) {
			purged++
			continue
		}
		kept = append(kept, entry)
	}
	logs.entries = kept
	return purged, nil
}

func (p *Postgres) AppendLogs(entries []api.LogEntry) error {
	if len(entries) == 0 {
		return nil
	}
	ctx := context.Background()
	var values []string
	var args []any
	for i, entry := range entries {
		contextJSON, _ := json.Marshal(entry.Context)
		if entry.Context == nil {
			contextJSON = []byte("{}")
		}
		base := i * 13
		values = append(values, fmt.Sprintf("($%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d)", base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8, base+9, base+10, base+11, base+12, base+13))
		args = append(args, p.tenant, entry.Timestamp, entry.ProjectID, entry.PipelineID, entry.RunID, entry.Node, entry.Attempt, entry.WorkloadKind, entry.WorkloadID, entry.Source, entry.Severity, entry.Message, contextJSON)
	}
	_, err := p.pool.Exec(ctx, `INSERT INTO log_entries (tenant_id,ts,project_id,pipeline_id,run_id,node,attempt,workload_kind,workload_id,source,severity,message,context) VALUES `+strings.Join(values, ","), args...)
	return err
}

func (p *Postgres) QueryLogs(filter api.LogFilter) ([]api.LogEntry, error) {
	filter = normalizeFilter(filter)
	where := []string{"tenant_id=$1"}
	args := []any{p.tenant}
	add := func(clause string, value any) {
		args = append(args, value)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}
	if filter.After > 0 {
		add("sequence > $%d", filter.After)
	}
	if filter.Before > 0 {
		add("sequence < $%d", filter.Before)
	}
	if len(filter.ProjectIDs) > 0 {
		add("project_id = ANY($%d)", filter.ProjectIDs)
	}
	if filter.RunID != "" {
		add("run_id = $%d", filter.RunID)
	}
	if filter.Node != "" {
		add("node = $%d", filter.Node)
	}
	if filter.PipelineID != "" {
		add("pipeline_id = $%d", filter.PipelineID)
	}
	if len(filter.Sources) > 0 {
		add("source = ANY($%d)", filter.Sources)
	}
	if len(filter.Severities) > 0 {
		add("severity = ANY($%d)", filter.Severities)
	}
	if filter.Since != nil {
		add("ts >= $%d", *filter.Since)
	}
	if filter.Until != nil {
		add("ts <= $%d", *filter.Until)
	}
	if filter.Query != "" {
		add("message ILIKE $%d", "%"+strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(filter.Query)+"%")
	}
	order := "ASC"
	if filter.Descending {
		order = "DESC"
	}
	args = append(args, filter.Limit)
	rows, err := p.pool.Query(context.Background(), `SELECT sequence,ts,project_id,pipeline_id,run_id,node,attempt,workload_kind,workload_id,source,severity,message,context FROM log_entries WHERE `+strings.Join(where, " AND ")+` ORDER BY sequence `+order+fmt.Sprintf(" LIMIT $%d", len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.LogEntry{}
	for rows.Next() {
		var entry api.LogEntry
		var contextJSON []byte
		if err := rows.Scan(&entry.Sequence, &entry.Timestamp, &entry.ProjectID, &entry.PipelineID, &entry.RunID, &entry.Node, &entry.Attempt, &entry.WorkloadKind, &entry.WorkloadID, &entry.Source, &entry.Severity, &entry.Message, &contextJSON); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(contextJSON, &entry.Context)
		out = append(out, entry)
	}
	return out, rows.Err()
}

func (p *Postgres) PurgeLogs(before time.Time) (int64, error) {
	tag, err := p.pool.Exec(context.Background(), `DELETE FROM log_entries WHERE tenant_id=$1 AND ts < $2`, p.tenant, before)
	return tag.RowsAffected(), err
}

// SortLogs orders entries by sequence (used when merging pages).
func SortLogs(entries []api.LogEntry) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].Sequence < entries[j].Sequence })
}
