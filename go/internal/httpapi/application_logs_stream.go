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
//	id: <cursor>
//	event: log
//	data: {"pod":…,"container":…,"time":…,"line":…}
//
// The id is an opaque cursor (application_logs_cursor.go) that records, per
// pod and container, the last line delivered. A client resumes by
// reconnecting with Last-Event-ID (or ?since=) set to the last id it
// received: each pod then continues exactly after its own last delivered
// line, whatever the other pods had delivered, and lines sharing one instant
// are neither repeated nor dropped. An RFC 3339 timestamp is accepted in
// place of a cursor, as older clients send: every pod then skips lines at or
// before that instant.
//
// Streams are bounded: at most maxLogPods pods, max_seconds of streaming
// (default 600, at most 3600) and maxStreamLineBytes per line. The stream
// ends with `event: end` carrying the reason; pods that start later are
// picked up by reconnecting.

const (
	defaultFollowSeconds = 600
	maxFollowSeconds     = 3600
	maxStreamLineBytes   = 64 * 1024
	logHeartbeat         = 15 * time.Second
	streamWriteTimeout   = 10 * time.Second
	logProbeTimeout      = 10 * time.Second
)

// openPodLog opens one pod's log; tests replace it with a scripted kubelet.
var openPodLog = func(ctx context.Context, access *kubeAccess, pod string, options *corev1.PodLogOptions) (io.ReadCloser, error) {
	return access.core.CoreV1().Pods(access.namespace).GetLogs(pod, options).Stream(ctx)
}

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

// streamedLine is a log event and the instant its cursor advances to.
type streamedLine struct {
	logLine
	key logKey
	at  time.Time
}

// logEntry turns one kubelet line into an event, or drops it when resume
// says the client already has it. The kubelet's SinceTime has second
// precision, so the exact cut is made here.
func logEntry(pod, container, raw string, resume *podResume) (streamedLine, bool) {
	at, text := parseLogLine(raw)
	if !resume.keep(at) {
		return streamedLine{}, false
	}
	entry := streamedLine{logLine: logLine{Pod: pod, Container: container, Line: text}, at: at}
	if !at.IsZero() {
		entry.Time = at.UTC().Format(time.RFC3339Nano)
	}
	return entry, true
}

// firstTailInstant reads the instant of the first of a pod's last tail
// lines. empty reports a log with no lines yet; a zero instant with empty
// false is a first line without a timestamp.
func firstTailInstant(ctx context.Context, access *kubeAccess, pod, container string, tail int64) (at time.Time, empty bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, logProbeTimeout)
	defer cancel()
	stream, err := openPodLog(ctx, access, pod, &corev1.PodLogOptions{Timestamps: true, Container: container, TailLines: &tail})
	if err != nil {
		return time.Time{}, false, err
	}
	defer func() { _ = stream.Close() }()
	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 4096), maxStreamLineBytes)
	if !scanner.Scan() {
		return time.Time{}, scanner.Err() == nil, scanner.Err()
	}
	at, _ = parseLogLine(scanner.Text())
	return at, false, nil
}

// freshPodStart opens a pod on a stream with no resume point: at the first
// of its last tail lines. Starting from that line's instant, rather than
// asking the kubelet for a tail while following, shows every line stamped at
// that instant from the first one, so the count the cursor keeps for it is
// exact. A pod may therefore deliver more than tail lines when several share
// the instant of the first.
func freshPodStart(ctx context.Context, access *kubeAccess, pod, container string, tail int64) podStart {
	at, empty, err := firstTailInstant(ctx, access, pod, container, tail)
	switch {
	case err != nil || (at.IsZero() && !empty):
		// Unreadable, or lines without timestamps: follow the tail as is.
		return podStart{tail: true}
	case empty:
		return podStart{}
	}
	return podStart{since: at, resume: podResume{anchored: true, at: at}}
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
	cursor, resumed, err := logResumePoint(r)
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

	// Every pod gets its start before anything is sent, so the first id
	// already places all of them. A stream with no resume point finds each
	// pod's start itself, one short kubelet read per pod.
	containers, starts := make([]string, len(pods)), make([]podStart, len(pods))
	var probes sync.WaitGroup
	for i, pod := range pods {
		containers[i] = container
		if container == "" && len(pod.Spec.Containers) > 0 {
			containers[i] = pod.Spec.Containers[0].Name
		}
		if resumed {
			starts[i] = resumedPodStart(cursor, podLogKey(pod.Name, containers[i]))
			continue
		}
		probes.Add(1)
		go func() {
			defer probes.Done()
			starts[i] = freshPodStart(ctx, access, pod.Name, containers[i], tail)
		}()
	}
	probes.Wait()
	// position is the cursor sent with each event. It keeps only the pods of
	// this connection, so it never outgrows maxLogPods; a pod that is gone
	// drops out.
	position := logCursor{floor: cursor.floor, pods: make(map[logKey]podPosition, len(pods))}
	if !resumed {
		position.floor = time.Now().Add(-logFloorAllowance).UnixNano()
	}
	for i, pod := range pods {
		if start := starts[i].resume; start.anchored {
			position.pods[podLogKey(pod.Name, containers[i])] = podPosition{at: start.at.UnixNano(), count: start.skip}
		}
	}

	lines := make(chan streamedLine, 256)
	var wg sync.WaitGroup
	for i, pod := range pods {
		podContainer, start, key := containers[i], starts[i], podLogKey(pod.Name, containers[i])
		options := &corev1.PodLogOptions{Follow: true, Timestamps: true, Container: podContainer}
		if start.tail {
			options.TailLines = &tail
		} else if !start.since.IsZero() {
			from := metav1.NewTime(start.since.Truncate(time.Second))
			options.SinceTime = &from
		}
		wg.Add(1)
		go func(podName string) {
			defer wg.Done()
			stream, err := openPodLog(ctx, access, podName, options)
			if err != nil {
				select {
				case lines <- streamedLine{logLine: logLine{Pod: podName, Container: podContainer, Line: "log stream unavailable: " + err.Error()}}:
				case <-ctx.Done():
				}
				return
			}
			defer func() { _ = stream.Close() }()
			scanner := bufio.NewScanner(stream)
			scanner.Buffer(make([]byte, 4096), maxStreamLineBytes)
			resume := start.resume
			for scanner.Scan() {
				entry, keep := logEntry(podName, podContainer, scanner.Text(), &resume)
				if !keep {
					continue
				}
				entry.key = key
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
			payload, _ := json.Marshal(entry.logLine)
			event := ""
			if !entry.at.IsZero() {
				// The position advances only for a line that is sent, so
				// the id never claims a line still waiting in the channel.
				position.advance(entry.key, entry.at)
				event = "id: " + position.encode() + "\n"
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
