package traceproxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

type Event struct {
	Agent      string    `json:"agent"`
	Version    string    `json:"version"`
	SessionID  string    `json:"session_id"`
	Model      string    `json:"model,omitempty"`
	StatusCode int       `json:"status_code"`
	DurationMS int64     `json:"duration_ms"`
	CreatedAt  time.Time `json:"created_at"`
}

type Sink interface{ Emit(Event) }

type HTTPSink struct {
	URL, Token string
	Client     *http.Client
}

func (s HTTPSink) Emit(event Event) {
	if s.URL == "" {
		return
	}
	raw, _ := json.Marshal(event)
	req, err := http.NewRequest(http.MethodPost, s.URL, bytes.NewReader(raw))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if s.Token != "" {
		req.Header.Set("Authorization", "Bearer "+s.Token)
	}
	response, err := s.Client.Do(req)
	if err == nil {
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
	}
}

// KafkaRESTSink publishes events to the Karapace Kafka REST endpoint using its
// Confluent-compatible envelope, e.g. http://kafka-rest:8082/topics/mlaiops.llm.traces.
type KafkaRESTSink struct {
	URL    string
	Client *http.Client
}

func (s KafkaRESTSink) Emit(event Event) {
	if s.URL == "" {
		return
	}
	raw, _ := json.Marshal(map[string]any{"records": []map[string]any{{"value": event}}})
	req, err := http.NewRequest(http.MethodPost, s.URL, bytes.NewReader(raw))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/vnd.kafka.json.v2+json")
	response, err := s.Client.Do(req)
	if err == nil {
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
	}
}

func New(upstream *url.URL, agent, version string, sink Sink) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	original := proxy.Director
	proxy.Director = func(r *http.Request) {
		original(r)
		r.Header.Set("X-MLAIOps-Agent", agent)
		r.Header.Set("X-MLAIOps-Agent-Version", version)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		proxy.ServeHTTP(recorder, r)
		event := Event{Agent: agent, Version: version, SessionID: r.Header.Get("X-Session-ID"), StatusCode: recorder.status, DurationMS: time.Since(start).Milliseconds(), CreatedAt: time.Now().UTC()}
		go sink.Emit(event)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
