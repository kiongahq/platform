// Package logexport ships Kionga log entries to Elasticsearch or OpenSearch.
//
// The two targets are separate adapters: Elasticsearch writes `create`
// actions into a data stream (logs-kionga.<tenant>-default); OpenSearch
// writes `index` actions into a daily index (kionga-logs-<tenant>-YYYY.MM.DD)
// and assumes no data-stream support. Each tenant runs its own exporter, and
// the tenant is part of every index name and document.
//
// Delivery is at-least-once: the cursor advances only after a batch is fully
// accepted or dead-lettered, and only failed items are retried. When the
// target is down the exporter backs off and reports itself degraded or
// unavailable; the rest of Kionga is unaffected because nothing waits on it.
package logexport

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
)

// Document is the exported shape (ECS-style field names).
type Document struct {
	Timestamp time.Time         `json:"@timestamp"`
	Message   string            `json:"message"`
	Log       map[string]string `json:"log"`
	Event     map[string]string `json:"event"`
	Kionga    map[string]any    `json:"kionga"`
}

// ToDocument maps a log entry to the exported document.
func ToDocument(tenant string, entry api.LogEntry) Document {
	fields := map[string]any{"tenant": tenant, "sequence": entry.Sequence, "source": entry.Source}
	for key, value := range map[string]string{"project_id": entry.ProjectID, "pipeline_id": entry.PipelineID, "run_id": entry.RunID, "node": entry.Node, "workload_kind": entry.WorkloadKind, "workload_id": entry.WorkloadID} {
		if value != "" {
			fields[key] = value
		}
	}
	if entry.Attempt > 0 {
		fields["attempt"] = entry.Attempt
	}
	if len(entry.Context) > 0 {
		fields["context"] = entry.Context
	}
	return Document{Timestamp: entry.Timestamp.UTC(), Message: entry.Message, Log: map[string]string{"level": entry.Severity},
		Event: map[string]string{"dataset": "kionga." + entry.Source, "id": fmt.Sprintf("%s-%d", tenant, entry.Sequence)}, Kionga: fields}
}

// Sink is one export target.
type Sink interface {
	Name() string
	// Send writes documents and returns one error (or nil) per document.
	// A non-nil second return means the whole request failed.
	Send(ctx context.Context, documents []Document) ([]error, error)
}

var tenantName = regexp.MustCompile(`[^a-z0-9-]+`)

// SafeTenant lowercases and restricts a tenant to index-name characters.
func SafeTenant(tenant string) string {
	cleaned := strings.Trim(tenantName.ReplaceAllString(strings.ToLower(tenant), "-"), "-")
	if cleaned == "" {
		return "default"
	}
	return cleaned
}

// Config configures a sink from environment-style values.
type Config struct {
	Target      string // elasticsearch | opensearch
	URL         string
	Tenant      string
	APIKey      string
	Username    string
	Password    string
	CAFile      string
	InsecureTLS bool
	// Anonymous allows targets without authentication (local test clusters).
	Anonymous bool
	Timeout   time.Duration
}

// ConfigFromEnv reads KIONGA_LOG_EXPORT_* variables. Secrets come from
// *_FILE paths so they never appear in process listings or compose files.
func ConfigFromEnv() (Config, error) {
	config := Config{
		Target: strings.ToLower(os.Getenv("KIONGA_LOG_EXPORT_TARGET")), URL: strings.TrimRight(os.Getenv("KIONGA_LOG_EXPORT_URL"), "/"),
		Tenant: os.Getenv("MLAIOPS_TENANT"), Username: os.Getenv("KIONGA_LOG_EXPORT_USERNAME"),
		CAFile: os.Getenv("KIONGA_LOG_EXPORT_CA_FILE"), InsecureTLS: os.Getenv("KIONGA_LOG_EXPORT_INSECURE_TLS") == "true", Timeout: 15 * time.Second,
		Anonymous: os.Getenv("KIONGA_LOG_EXPORT_ANONYMOUS") == "true",
	}
	if config.Tenant == "" {
		config.Tenant = "default"
	}
	for env, target := range map[string]*string{"KIONGA_LOG_EXPORT_API_KEY_FILE": &config.APIKey, "KIONGA_LOG_EXPORT_PASSWORD_FILE": &config.Password} {
		if path := os.Getenv(env); path != "" {
			raw, err := os.ReadFile(path)
			if err != nil {
				return config, fmt.Errorf("read %s: %w", env, err)
			}
			*target = strings.TrimSpace(string(raw))
		}
	}
	return config, config.Validate()
}

// Validate reports configuration problems without echoing secrets.
func (c Config) Validate() error {
	if c.Target != "elasticsearch" && c.Target != "opensearch" {
		return errors.New("KIONGA_LOG_EXPORT_TARGET must be elasticsearch or opensearch")
	}
	parsed, err := url.Parse(c.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return errors.New("KIONGA_LOG_EXPORT_URL must be an http(s) URL")
	}
	if parsed.User != nil {
		return errors.New("put credentials in KIONGA_LOG_EXPORT_USERNAME / *_FILE variables, not in the URL")
	}
	if !c.Anonymous && c.APIKey == "" && (c.Username == "" || c.Password == "") {
		return errors.New("set KIONGA_LOG_EXPORT_API_KEY_FILE, or a username and KIONGA_LOG_EXPORT_PASSWORD_FILE, or KIONGA_LOG_EXPORT_ANONYMOUS=true for an unauthenticated test cluster")
	}
	if parsed.Scheme == "http" && (c.APIKey != "" || c.Password != "") && !strings.HasPrefix(parsed.Hostname(), "localhost") && parsed.Hostname() != "127.0.0.1" {
		return errors.New("refusing to send credentials over plain http to a remote host; use https")
	}
	return nil
}

func (c Config) httpClient() (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: c.InsecureTLS} //nolint:gosec // explicit, documented opt-in
	if c.CAFile != "" {
		pem, err := os.ReadFile(c.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read CA bundle: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("CA bundle contains no certificates")
		}
		tlsConfig.RootCAs = pool
	}
	transport.TLSClientConfig = tlsConfig
	return &http.Client{Transport: transport, Timeout: c.Timeout}, nil
}

// NewSink builds the adapter for the configured target.
func NewSink(c Config) (Sink, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	client, err := c.httpClient()
	if err != nil {
		return nil, err
	}
	base := bulkSink{config: c, client: client}
	if c.Target == "elasticsearch" {
		return elasticsearch{base}, nil
	}
	return opensearch{base}, nil
}

type bulkSink struct {
	config Config
	client *http.Client
}

func (b bulkSink) authorize(request *http.Request) {
	switch {
	case b.config.APIKey != "":
		request.Header.Set("Authorization", "ApiKey "+b.config.APIKey)
	case b.config.Username != "":
		request.SetBasicAuth(b.config.Username, b.config.Password)
	}
}

type bulkResponse struct {
	Errors bool `json:"errors"`
	Items  []map[string]struct {
		Status int `json:"status"`
		Error  *struct {
			Type   string `json:"type"`
			Reason string `json:"reason"`
		} `json:"error"`
	} `json:"items"`
}

// bulk posts NDJSON and maps per-item results. Request-level failures never
// include credentials: errors name the host and status only.
func (b bulkSink) bulk(ctx context.Context, action func(Document) map[string]any, documents []Document) ([]error, error) {
	var body bytes.Buffer
	encoder := json.NewEncoder(&body)
	for _, document := range documents {
		if err := encoder.Encode(action(document)); err != nil {
			return nil, err
		}
		if err := encoder.Encode(document); err != nil {
			return nil, err
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, b.config.URL+"/_bulk", &body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/x-ndjson")
	b.authorize(request)
	response, err := b.client.Do(request)
	if err != nil {
		var urlError *url.Error
		if errors.As(err, &urlError) {
			return nil, fmt.Errorf("%s unreachable: %v", hostOf(b.config.URL), urlError.Err)
		}
		return nil, fmt.Errorf("%s unreachable", hostOf(b.config.URL))
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("%s rejected the credentials (HTTP %d)", hostOf(b.config.URL), response.StatusCode)
	}
	if response.StatusCode >= 300 {
		return nil, fmt.Errorf("%s returned HTTP %d", hostOf(b.config.URL), response.StatusCode)
	}
	var parsed bulkResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("%s returned an unreadable bulk response", hostOf(b.config.URL))
	}
	if len(parsed.Items) != len(documents) {
		return nil, fmt.Errorf("%s returned %d results for %d documents", hostOf(b.config.URL), len(parsed.Items), len(documents))
	}
	results := make([]error, len(documents))
	for i, item := range parsed.Items {
		for _, outcome := range item {
			if outcome.Status >= 300 {
				reason := http.StatusText(outcome.Status)
				if outcome.Error != nil {
					reason = outcome.Error.Type + ": " + outcome.Error.Reason
				}
				results[i] = &ItemError{Status: outcome.Status, Reason: reason}
			}
		}
	}
	return results, nil
}

func hostOf(raw string) string {
	if parsed, err := url.Parse(raw); err == nil {
		return parsed.Host
	}
	return "export target"
}

// ItemError is a per-document rejection. 429 and 5xx are retryable;
// mapping and validation errors (4xx) are not.
type ItemError struct {
	Status int
	Reason string
}

func (e *ItemError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.Status, e.Reason) }
func (e *ItemError) Retryable() bool {
	return e.Status == http.StatusTooManyRequests || e.Status >= 500
}

type elasticsearch struct{ bulkSink }

func (elasticsearch) Name() string { return "elasticsearch" }

// DataStream is the Elasticsearch data stream for a tenant. It matches the
// built-in logs-*-* index template, so no template setup is required.
func DataStream(tenant string) string { return "logs-kionga." + SafeTenant(tenant) + "-default" }

func (e elasticsearch) Send(ctx context.Context, documents []Document) ([]error, error) {
	index := DataStream(e.config.Tenant)
	return e.bulk(ctx, func(Document) map[string]any { return map[string]any{"create": map[string]string{"_index": index}} }, documents)
}

type opensearch struct{ bulkSink }

func (opensearch) Name() string { return "opensearch" }

// DailyIndex is the OpenSearch index for a tenant and day.
func DailyIndex(tenant string, at time.Time) string {
	return "kionga-logs-" + SafeTenant(tenant) + "-" + at.UTC().Format("2006.01.02")
}

func (o opensearch) Send(ctx context.Context, documents []Document) ([]error, error) {
	return o.bulk(ctx, func(document Document) map[string]any {
		return map[string]any{"index": map[string]string{"_index": DailyIndex(o.config.Tenant, document.Timestamp), "_id": document.Event["id"]}}
	}, documents)
}

// ---- exporter loop --------------------------------------------------------

// Status is reported at /status and shown on the Platform page.
type Status struct {
	State          string    `json:"state"` // configured | healthy | degraded | unavailable
	Target         string    `json:"target"`
	Tenant         string    `json:"tenant"`
	Destination    string    `json:"destination"`
	Cursor         int64     `json:"cursor"`
	Exported       int64     `json:"exported"`
	DeadLettered   int64     `json:"dead_lettered"`
	LastSuccess    time.Time `json:"last_success,omitempty"`
	LastError      string    `json:"last_error,omitempty"`
	LastErrorAt    time.Time `json:"last_error_at,omitempty"`
	ConsecutiveErr int       `json:"consecutive_failures"`
}

// CursorKind stores the export cursor per sink.
const CursorKind = "log_export_cursor"

type cursorDoc struct {
	Sequence     int64 `json:"sequence"`
	Exported     int64 `json:"exported"`
	DeadLettered int64 `json:"dead_lettered"`
}

type Exporter struct {
	Logs        store.LogStore
	Docs        store.Documents
	Sink        Sink
	Tenant      string
	Destination string
	BatchSize   int
	MaxAttempts int
	Sleep       func(time.Duration)
	Now         func() time.Time

	mu     sync.RWMutex
	status Status
}

func (e *Exporter) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now().UTC()
}

func (e *Exporter) sleep(d time.Duration) {
	if e.Sleep != nil {
		e.Sleep(d)
		return
	}
	time.Sleep(d)
}

// Status returns a snapshot.
func (e *Exporter) Status() Status {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.status
}

func (e *Exporter) setFailure(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.status.ConsecutiveErr++
	e.status.LastError, e.status.LastErrorAt = err.Error(), e.now()
	e.status.State = "degraded"
	if e.status.ConsecutiveErr >= 5 {
		e.status.State = "unavailable"
	}
}

func (e *Exporter) loadCursor() cursorDoc {
	cursor, _ := store.GetDoc[cursorDoc](e.Docs, CursorKind, e.Sink.Name())
	return cursor
}

func (e *Exporter) saveCursor(cursor cursorDoc) error {
	_, err := store.UpdateDoc(e.Docs, CursorKind, e.Sink.Name(), func(cursorDoc, bool) (cursorDoc, error) { return cursor, nil }, "", "log-exporter")
	return err
}

// ExportOnce ships at most one batch and returns how many entries it
// handled (0 when caught up). Errors mean the batch will be retried.
func (e *Exporter) ExportOnce(ctx context.Context) (int, error) {
	batch := e.BatchSize
	if batch <= 0 {
		batch = 500
	}
	maxAttempts := e.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 5
	}
	cursor := e.loadCursor()
	e.mu.Lock()
	e.status.Target, e.status.Tenant, e.status.Destination, e.status.Cursor = e.Sink.Name(), e.Tenant, e.Destination, cursor.Sequence
	e.status.Exported, e.status.DeadLettered = cursor.Exported, cursor.DeadLettered
	if e.status.State == "" {
		e.status.State = "configured"
	}
	e.mu.Unlock()
	entries, err := e.Logs.QueryLogs(api.LogFilter{After: cursor.Sequence, Limit: batch})
	if err != nil || len(entries) == 0 {
		return 0, err
	}
	pending := make([]Document, len(entries))
	for i, entry := range entries {
		pending[i] = ToDocument(e.Tenant, entry)
	}
	var dead int64
	backoff := time.Second
	for attempt := 1; len(pending) > 0; attempt++ {
		results, err := e.Sink.Send(ctx, pending)
		if err != nil {
			e.setFailure(err)
			return 0, err // whole request failed: keep the cursor, retry later
		}
		var retry []Document
		for i, itemErr := range results {
			if itemErr == nil {
				continue
			}
			var item *ItemError
			if errors.As(itemErr, &item) && item.Retryable() && attempt < maxAttempts {
				retry = append(retry, pending[i])
			} else {
				dead++
			}
		}
		pending = retry
		if len(pending) > 0 {
			e.sleep(backoff)
			backoff = min(backoff*2, time.Minute)
		}
	}
	cursor.Sequence = entries[len(entries)-1].Sequence
	cursor.Exported += int64(len(entries)) - dead
	cursor.DeadLettered += dead
	if err := e.saveCursor(cursor); err != nil {
		return 0, err
	}
	e.mu.Lock()
	e.status.Cursor, e.status.Exported, e.status.DeadLettered = cursor.Sequence, cursor.Exported, cursor.DeadLettered
	e.status.LastSuccess, e.status.ConsecutiveErr, e.status.State = e.now(), 0, "healthy"
	if dead > 0 {
		e.status.LastError, e.status.LastErrorAt = fmt.Sprintf("%d document(s) rejected by the target and dead-lettered", dead), e.now()
	}
	e.mu.Unlock()
	return len(entries), nil
}

// Run exports until ctx ends, backing off on failure (1s doubling to 60s).
func (e *Exporter) Run(ctx context.Context, idle time.Duration) {
	backoff := time.Second
	for ctx.Err() == nil {
		handled, err := e.ExportOnce(ctx)
		switch {
		case err != nil:
			e.sleep(backoff)
			backoff = min(backoff*2, time.Minute)
		case handled == 0:
			backoff = time.Second
			select {
			case <-ctx.Done():
			case <-time.After(idle):
			}
		default:
			backoff = time.Second
		}
	}
}
