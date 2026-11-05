package httpapi

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/kiongahq/platform/internal/agentteam"
	"github.com/kiongahq/platform/internal/redact"
	"github.com/kiongahq/platform/internal/store"
)

// A run's event log lives apart from the run. Appends go to one tail document,
// whose row lock makes seq strictly increasing and commit order equal to seq
// order. Full pages are copied out and trimmed from the tail; a crash between
// the two leaves an overlap the reader removes.
const (
	teamEventTailKind = "team_run_event_tail"
	teamEventPageKind = "team_run_event_page"
	teamEventPageSize = 100
	teamEventRead     = 500
)

type teamEventError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// teamEvent is contracts agent-team/team-run-event.v1. Add a field there first.
type teamEvent struct {
	Seq               int              `json:"seq"`
	Type              string           `json:"type"`
	RunID             string           `json:"run_id"`
	TS                time.Time        `json:"ts"`
	WorkerID          string           `json:"worker_id,omitempty"`
	Agent             string           `json:"agent,omitempty"`
	Role              string           `json:"role,omitempty"`
	Turn              int              `json:"turn,omitempty"`
	Attempt           int              `json:"attempt,omitempty"`
	Status            string           `json:"status,omitempty"`
	Tool              string           `json:"tool,omitempty"`
	ToolCallID        string           `json:"tool_call_id,omitempty"`
	DurationMS        *int64           `json:"duration_ms,omitempty"`
	InputPreview      string           `json:"input_preview,omitempty"`
	OutputPreview     string           `json:"output_preview,omitempty"`
	MessagePreview    string           `json:"message_preview,omitempty"`
	From              string           `json:"from,omitempty"`
	To                string           `json:"to,omitempty"`
	NextAttemptAt     *time.Time       `json:"next_attempt_at,omitempty"`
	RetryAfterSeconds *int             `json:"retry_after_seconds,omitempty"`
	ApprovalID        string           `json:"approval_id,omitempty"`
	ResumeCount       *int             `json:"resume_count,omitempty"`
	Usage             *agentteam.Usage `json:"usage,omitempty"`
	Error             *teamEventError  `json:"error,omitempty"`
}

type teamEventTail struct {
	RunID   string      `json:"run_id"`
	NextSeq int         `json:"next_seq"`
	Events  []teamEvent `json:"events"`
}

type teamEventPage struct {
	Events []teamEvent `json:"events"`
}

// previewText redacts first, so a cut cannot leave half a secret, then trims to
// the contract's limit in characters.
func previewText(text string, limit int) string {
	text = redact.String(text)
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit])
}

func preview(text string) string { return previewText(text, 2000) }

func eventError(code, message string) *teamEventError {
	return &teamEventError{Code: code, Message: previewText(message, 500)}
}

func teamEventPageID(runID string, page int) string { return runID + ":" + strconv.Itoa(page) }

// appendTeamEvent never fails a run: losing an event is better than losing work.
func appendTeamEvent(docs store.Documents, event teamEvent) {
	if err := writeTeamEvent(docs, event, false); err != nil {
		log.Printf("team run %s: event %s not recorded: %v", event.RunID, event.Type, err)
	}
}

func writeTeamEvent(docs store.Documents, event teamEvent, once bool) error {
	if event.TS.IsZero() {
		event.TS = time.Now().UTC()
	}
	tail, err := store.UpdateDoc(docs, teamEventTailKind, event.RunID, func(current teamEventTail, _ bool) (teamEventTail, error) {
		if once {
			for i := len(current.Events) - 1; i >= 0; i-- {
				if current.Events[i].Type == event.Type {
					return current, store.ErrSkipWrite
				}
				if current.Events[i].Type == "run.resumed" {
					break
				}
			}
		}
		current.RunID = event.RunID
		current.NextSeq++
		event.Seq = current.NextSeq
		current.Events = append(current.Events, event)
		return current, nil
	}, "", "")
	if err != nil {
		return err
	}
	if len(tail.Events) < 2*teamEventPageSize {
		return nil
	}
	first := tail.Events[0].Seq
	sealed := tail.Events[:teamEventPageSize]
	last := sealed[len(sealed)-1].Seq
	if _, err := store.UpdateDoc(docs, teamEventPageKind, teamEventPageID(event.RunID, (first-1)/teamEventPageSize), func(_ teamEventPage, _ bool) (teamEventPage, error) {
		return teamEventPage{Events: sealed}, nil
	}, "", ""); err != nil {
		return err
	}
	_, err = store.UpdateDoc(docs, teamEventTailKind, event.RunID, func(current teamEventTail, _ bool) (teamEventTail, error) {
		kept := current.Events[:0]
		for _, item := range current.Events {
			if item.Seq > last {
				kept = append(kept, item)
			}
		}
		current.Events = kept
		return current, nil
	}, "", "")
	return err
}

// teamEventsAfter returns up to teamEventRead events with seq above after.
func teamEventsAfter(docs store.Documents, runID string, after int) []teamEvent {
	events := []teamEvent{}
	seen := after
	take := func(items []teamEvent) {
		for _, item := range items {
			if item.Seq > seen && len(events) < teamEventRead {
				events = append(events, item)
				seen = item.Seq
			}
		}
	}
	// Pages first: the tail is read last so nothing trimmed between reads is lost.
	for page := after / teamEventPageSize; len(events) < teamEventRead; page++ {
		item, err := store.GetDoc[teamEventPage](docs, teamEventPageKind, teamEventPageID(runID, page))
		if err != nil {
			break
		}
		take(item.Events)
	}
	if tail, err := store.GetDoc[teamEventTail](docs, teamEventTailKind, runID); err == nil {
		take(tail.Events)
	}
	return events
}

func eventCursor(r *http.Request) int {
	after := 0
	if value, err := strconv.Atoi(r.URL.Query().Get("after")); err == nil && value > 0 {
		after = value
	}
	if value, err := strconv.Atoi(r.Header.Get("Last-Event-ID")); err == nil && value > after {
		after = value
	}
	return after
}

// teamRunEvents is the polling page. done is true only once the run settled and
// the reader has everything, so run.finished is never missed.
func (s *Server) teamRunEvents(w http.ResponseWriter, r *http.Request) {
	run, ok := s.teamRunFor(r)
	if !ok {
		writeError(w, 404, "not_found", "run not found")
		return
	}
	after := eventCursor(r)
	events := teamEventsAfter(s.store, run.ID, after)
	writeJSON(w, 200, map[string]any{"events": events, "done": run.SettledAt != nil && len(events) < teamEventRead})
}

// teamRunEventStream serves the same log as Server-Sent Events: "run" for each
// event, "end" after the last one.
func (s *Server) teamRunEventStream(w http.ResponseWriter, r *http.Request) {
	run, ok := s.teamRunFor(r)
	if !ok {
		writeError(w, 404, "not_found", "run not found")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, 501, "streaming_unavailable", "Event streaming is not available.")
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	after := eventCursor(r)
	deadline := time.Now().Add(5 * time.Minute)
	heartbeat := time.Now()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		// Read the run before the events, so a settled run's log is complete.
		current, err := store.GetDoc[agentteam.Run](s.store, teamRunKind, run.ID)
		if err != nil {
			fmt.Fprint(w, "event: end\ndata: {\"status\":\"missing\"}\n\n")
			flusher.Flush()
			return
		}
		events := teamEventsAfter(s.store, run.ID, after)
		for _, event := range events {
			raw, err := marshalEvent(event)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "id: %d\nevent: run\ndata: %s\n\n", event.Seq, raw) // #nosec G705 -- JSON-encoded stored event
			after = event.Seq
			heartbeat = time.Now()
		}
		if current.SettledAt != nil && len(events) < teamEventRead {
			raw, _ := marshalEvent(map[string]string{"status": current.Status})
			fmt.Fprintf(w, "event: end\ndata: %s\n\n", raw) // #nosec G705 -- JSON-encoded status
			flusher.Flush()
			return
		}
		if time.Since(heartbeat) > 15*time.Second {
			fmt.Fprint(w, ": keep-alive\n\n")
			heartbeat = time.Now()
		}
		flusher.Flush()
		if time.Now().After(deadline) {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

var errLeaseLost = errors.New("worker lease lost")
