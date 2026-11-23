package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Log streaming: GET /api/v1/applications/{name}/logs?follow=true answers
// with Server-Sent Events. Each line is
//
//	id: <RFC 3339 timestamp with nanoseconds>
//	event: log
//	data: {"pod":…,"container":…,"time":…,"line":…}
//
// A client resumes by reconnecting with Last-Event-ID (or ?since=) set to the
// last id it received; lines at or before that instant are skipped. Streams
// are bounded: at most maxLogPods pods, max_seconds of streaming (default
// 600, at most 3600) and maxStreamLineBytes per line. The stream ends with
// `event: end` carrying the reason; pods that start later are picked up by
// reconnecting.

const (
	defaultFollowSeconds = 600
	maxFollowSeconds     = 3600
	maxStreamLineBytes   = 64 * 1024
	logHeartbeat         = 15 * time.Second
	streamWriteTimeout   = 10 * time.Second
)

type logLine struct {
	Pod       string `json:"pod"`
	Container string `json:"container"`
	Time      string `json:"time,omitempty"`
	Line      string `json:"line"`
}

// parseLogLine splits a kubelet line written with timestamps=true:
// "<RFC3339Nano> <text>". Lines without a parsable timestamp keep their text
// and get no time.
func parseLogLine(raw string) (time.Time, string) {
	stamp, text, found := strings.Cut(raw, " ")
	if !found {
		stamp, text = raw, ""
	}
	parsed, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return time.Time{}, raw
	}
	return parsed, text
}

// logEntry turns one kubelet line into an event. SinceTime has second
// precision, so lines at or before the resume point are dropped here.
func logEntry(pod, container, raw string, since time.Time) (logLine, bool) {
	at, text := parseLogLine(raw)
	if !since.IsZero() && !at.IsZero() && !at.After(since) {
		return logLine{}, false
	}
	entry := logLine{Pod: pod, Container: container, Line: text}
	if !at.IsZero() {
		entry.Time = at.UTC().Format(time.RFC3339Nano)
	}
	return entry, true
}

// logSince reads the resume point from Last-Event-ID or ?since=; the later
// of the two wins. Empty means from the tail.
func logSince(r *http.Request) (time.Time, error) {
	var since time.Time
	for _, raw := range []string{r.URL.Query().Get("since"), r.Header.Get("Last-Event-ID")} {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return time.Time{}, fmt.Errorf("since must be an RFC 3339 timestamp, such as the id of the last event received")
		}
		if parsed.After(since) {
			since = parsed
		}
	}
	return since, nil
}

func (s *Server) streamApplicationLogs(w http.ResponseWriter, r *http.Request, access *kubeAccess, name, container string, tail int64) {
	since, err := logSince(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	seconds := defaultFollowSeconds
	if raw := r.URL.Query().Get("max_seconds"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > maxFollowSeconds {
			writeError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("max_seconds must be between 1 and %d", maxFollowSeconds))
			return
		}
		seconds = value
	}
	pods, err := applicationPods(r.Context(), access, name)
	if err != nil {
		writeError(w, http.StatusBadGateway, "kubernetes_unavailable", "listing pods failed: "+err.Error())
		return
	}
	if len(pods) > maxLogPods {
		pods = pods[:maxLogPods]
	}
	controller := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(seconds)*time.Second)
	defer cancel()

	lines := make(chan logLine, 256)
	var wg sync.WaitGroup
	for _, pod := range pods {
		podContainer := container
		if podContainer == "" && len(pod.Spec.Containers) > 0 {
			podContainer = pod.Spec.Containers[0].Name
		}
		options := &corev1.PodLogOptions{Follow: true, Timestamps: true, Container: podContainer}
		if since.IsZero() {
			options.TailLines = &tail
		} else {
			start := metav1.NewTime(since.Truncate(time.Second))
			options.SinceTime = &start
		}
		wg.Add(1)
		go func(podName string) {
			defer wg.Done()
			stream, err := access.core.CoreV1().Pods(access.namespace).GetLogs(podName, options).Stream(ctx)
			if err != nil {
				select {
				case lines <- logLine{Pod: podName, Container: podContainer, Line: "log stream unavailable: " + err.Error()}:
				case <-ctx.Done():
				}
				return
			}
			defer func() { _ = stream.Close() }()
			scanner := bufio.NewScanner(stream)
			scanner.Buffer(make([]byte, 4096), maxStreamLineBytes)
			for scanner.Scan() {
				entry, keep := logEntry(podName, podContainer, scanner.Text(), since)
				if !keep {
					continue
				}
				select {
				case lines <- entry:
				case <-ctx.Done():
					return
				}
			}
		}(pod.Name)
	}
	go func() {
		wg.Wait()
		close(lines)
	}()

	// send bounds every write: a client that stops reading cannot hold the
	// handler (and its pod streams) past streamWriteTimeout. Any write error
	// ends the stream; the deferred cancel stops the pod readers.
	send := func(text string) bool {
		if err := controller.SetWriteDeadline(time.Now().Add(streamWriteTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return false
		}
		if _, err := io.WriteString(w, text); err != nil {
			return false
		}
		return controller.Flush() == nil
	}
	end := func(reason string) {
		payload, _ := json.Marshal(map[string]string{"reason": reason})
		send("event: end\ndata: " + string(payload) + "\n\n")
	}
	if len(pods) == 0 {
		end("no pods are running")
		return
	}
	heartbeat := time.NewTicker(logHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case entry, open := <-lines:
			if !open {
				end("all pod log streams closed")
				return
			}
			payload, _ := json.Marshal(entry)
			event := ""
			if entry.Time != "" {
				event = "id: " + entry.Time + "\n"
			}
			if !send(event + "event: log\ndata: " + string(payload) + "\n\n") {
				return
			}
		case <-heartbeat.C:
			if !send(": keep-alive\n\n") {
				return
			}
		case <-ctx.Done():
			if r.Context().Err() == nil {
				end("max_seconds reached; reconnect with Last-Event-ID to continue")
			}
			return
		}
	}
}
