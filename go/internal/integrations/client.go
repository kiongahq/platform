package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func New(baseURL, token string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Token: token, HTTP: &http.Client{Timeout: 15 * time.Second}}
}

func (c *Client) JSON(ctx context.Context, method, path string, input, output any) error {
	return c.jsonWithMediaType(ctx, method, path, input, output, "application/json", "application/json")
}

func (c *Client) jsonWithMediaType(ctx context.Context, method, path string, input, output any, contentType, accept string) error {
	if c.BaseURL == "" {
		return errors.New("integration base URL is not configured")
	}
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Accept", accept)
	if c.Token != "" {
		request.Header.Set("Authorization", "Bearer "+c.Token)
	}
	response, err := c.HTTP.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("integration returned %d: %s", response.StatusCode, strings.TrimSpace(string(raw)))
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(output)
}

type MLflow struct{ client *Client }

func NewMLflow(baseURL, token string) MLflow { return MLflow{client: New(baseURL, token)} }
func (m MLflow) TransitionStage(ctx context.Context, name, version, stage string) error {
	return m.client.JSON(ctx, http.MethodPost, "/api/2.0/mlflow/model-versions/transition-stage", map[string]any{"name": name, "version": version, "stage": stage, "archive_existing_versions": stage == "Production"}, &map[string]any{})
}

// ModelVersionDownloadURI returns where MLflow stores a registered model
// version's artifacts (GET model-versions/get-download-uri). The server
// resolves runs:/ and models:/ sources to the backing store location.
func (m MLflow) ModelVersionDownloadURI(ctx context.Context, name, version string) (string, error) {
	var result struct {
		ArtifactURI string `json:"artifact_uri"`
	}
	query := url.Values{"name": {name}, "version": {version}}
	if err := m.client.JSON(ctx, http.MethodGet, "/api/2.0/mlflow/model-versions/get-download-uri?"+query.Encode(), nil, &result); err != nil {
		return "", err
	}
	return result.ArtifactURI, nil
}

// RunArtifactURI returns a run's artifact root (GET runs/get).
func (m MLflow) RunArtifactURI(ctx context.Context, runID string) (string, error) {
	var result struct {
		Run struct {
			Info struct {
				ArtifactURI string `json:"artifact_uri"`
			} `json:"info"`
		} `json:"run"`
	}
	if err := m.client.JSON(ctx, http.MethodGet, "/api/2.0/mlflow/runs/get?"+url.Values{"run_id": {runID}}.Encode(), nil, &result); err != nil {
		return "", err
	}
	return result.Run.Info.ArtifactURI, nil
}

// LoggedModelArtifactURI returns an MLflow 3 logged model's artifact location
// (GET logged-models/{model_id}).
func (m MLflow) LoggedModelArtifactURI(ctx context.Context, modelID string) (string, error) {
	var result struct {
		Model struct {
			Info struct {
				ArtifactURI string `json:"artifact_uri"`
			} `json:"info"`
		} `json:"model"`
	}
	if err := m.client.JSON(ctx, http.MethodGet, "/api/2.0/mlflow/logged-models/"+url.PathEscape(modelID), nil, &result); err != nil {
		return "", err
	}
	return result.Model.Info.ArtifactURI, nil
}

// StatusError is a non-2xx answer from an integration. It carries the status
// and the integration's machine-readable error code only: response bodies can
// hold artifact contents and are never copied into an error.
type StatusError struct {
	Status int
	Code   string
}

func (e *StatusError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("integration returned %d (%s)", e.Status, e.Code)
	}
	return fmt.Sprintf("integration returned %d", e.Status)
}

// ErrTooLarge reports a response body larger than the caller's limit.
var ErrTooLarge = errors.New("integration response exceeds the size limit")

// getBytes GETs path and returns the body when it is at most limit bytes.
func (c *Client) getBytes(ctx context.Context, path string, limit int64) ([]byte, error) {
	if c.BaseURL == "" {
		return nil, errors.New("integration base URL is not configured")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	if c.Token != "" {
		request.Header.Set("Authorization", "Bearer "+c.Token)
	}
	// A redirect is never followed: the configured server answers itself, and
	// following one would send the gateway, and possibly its token, wherever
	// the answer points. The 3xx is reported as a StatusError.
	client := *c.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failure struct {
			ErrorCode string `json:"error_code"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&failure)
		if !errorCodePattern.MatchString(failure.ErrorCode) {
			failure.ErrorCode = ""
		}
		return nil, &StatusError{Status: response.StatusCode, Code: failure.ErrorCode}
	}
	if response.ContentLength > limit {
		return nil, ErrTooLarge
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, ErrTooLarge
	}
	return raw, nil
}

// errorCodePattern keeps a reported error code to an identifier, so nothing
// else from a response body reaches an error message or a log line.
var errorCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

// Configured reports whether an MLflow server URL is set.
func (m MLflow) Configured() bool { return m.client != nil && m.client.BaseURL != "" }

// ModelVersionArtifact reads one file of a registered model version through
// the MLflow server's own artifact route (GET /model-versions/get-artifact),
// so the caller needs no access to the artifact store behind it. A body
// larger than limit bytes is ErrTooLarge; a non-2xx answer is a *StatusError.
func (m MLflow) ModelVersionArtifact(ctx context.Context, name, version, path string, limit int64) ([]byte, error) {
	query := url.Values{"name": {name}, "version": {version}, "path": {path}}
	return m.client.getBytes(ctx, "/model-versions/get-artifact?"+query.Encode(), limit)
}

type Langfuse struct{ client *Client }

func NewLangfuse(baseURL, token string) Langfuse { return Langfuse{client: New(baseURL, token)} }
func (l Langfuse) Ingest(ctx context.Context, batch []map[string]any) error {
	return l.client.JSON(ctx, http.MethodPost, "/api/public/ingestion", map[string]any{"batch": batch}, &map[string]any{})
}

// Prefect drives real pipeline execution against a self-hosted Prefect
// server (the Compose-native fulfilment of the pipeline engine; Kubernetes
// runs pipeline nodes as Jobs, see internal/k8sexec).
type Prefect struct{ client *Client }

// NewPrefect accepts the Prefect convention (PREFECT_API_URL ends in /api)
// as well as a bare server URL; client paths always include /api.
func NewPrefect(baseURL, token string) Prefect {
	return Prefect{client: New(strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "/api"), token)}
}

// CreateFlowRun resolves a deployment by flow/deployment name and creates a
// flow run carrying the control-plane run id as a parameter so the flow can
// report step status back.
func (p Prefect) CreateFlowRun(ctx context.Context, flowName, deploymentName, runName string, parameters map[string]any) (string, error) {
	var deployment struct {
		ID string `json:"id"`
	}
	path := "/api/deployments/name/" + url.PathEscape(flowName) + "/" + url.PathEscape(deploymentName)
	if err := p.client.JSON(ctx, http.MethodGet, path, nil, &deployment); err != nil {
		return "", fmt.Errorf("prefect deployment %s/%s not found: %w", flowName, deploymentName, err)
	}
	var flowRun struct {
		ID string `json:"id"`
	}
	payload := map[string]any{"name": runName, "parameters": parameters}
	if err := p.client.JSON(ctx, http.MethodPost, "/api/deployments/"+deployment.ID+"/create_flow_run", payload, &flowRun); err != nil {
		return "", err
	}
	if flowRun.ID == "" {
		return "", errors.New("prefect did not return a flow run id")
	}
	return flowRun.ID, nil
}

// CancelFlowRun requests cancellation of a running flow run.
func (p Prefect) CancelFlowRun(ctx context.Context, flowRunID string) error {
	payload := map[string]any{"state": map[string]any{"type": "CANCELLING", "name": "Cancelling"}}
	return p.client.JSON(ctx, http.MethodPost, "/api/flow_runs/"+url.PathEscape(flowRunID)+"/set_state", payload, &map[string]any{})
}

// OpenFaaS is the platform's serverless AI deployment integration, chosen to
// satisfy the serverless requirement without any excluded technology.
// Locally and on a VM it targets faasd; on Kubernetes the
// same API is served by OpenFaaS.
type OpenFaaS struct {
	client   *Client
	user     string
	password string
}

func NewOpenFaaS(baseURL, user, password string) OpenFaaS {
	return OpenFaaS{client: New(baseURL, ""), user: user, password: password}
}

type Function struct {
	Name        string            `json:"name"`
	Image       string            `json:"image"`
	Replicas    int               `json:"replicas"`
	EnvVars     map[string]string `json:"envVars,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Limits      map[string]string `json:"limits,omitempty"`
	Requests    map[string]string `json:"requests,omitempty"`
}

func (o OpenFaaS) request(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	if o.client.BaseURL == "" {
		return nil, errors.New("openfaas base URL is not configured")
	}
	request, err := http.NewRequestWithContext(ctx, method, o.client.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if o.user != "" {
		request.SetBasicAuth(o.user, o.password)
	}
	return o.client.HTTP.Do(request)
}

func (o OpenFaaS) checked(response *http.Response, err error) ([]byte, error) {
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("openfaas returned %d: %s", response.StatusCode, strings.TrimSpace(string(raw)))
	}
	return raw, nil
}

// DeployFunction creates or updates a function (PUT is OpenFaaS's idempotent
// deploy).
func (o OpenFaaS) DeployFunction(ctx context.Context, function Function) error {
	if function.Name == "" || function.Image == "" {
		return errors.New("function name and image are required")
	}
	raw, err := json.Marshal(map[string]any{
		"service":     function.Name,
		"image":       function.Image,
		"envVars":     function.EnvVars,
		"labels":      function.Labels,
		"annotations": function.Annotations,
		"limits":      function.Limits,
		"requests":    function.Requests,
	})
	if err != nil {
		return err
	}
	response, err := o.request(ctx, http.MethodPut, "/system/functions", bytes.NewReader(raw))
	_, err = o.checked(response, err)
	return err
}

// DeleteFunction removes a deployed function through the OpenFaaS provider.
func (o OpenFaaS) DeleteFunction(ctx context.Context, name string) error {
	if name == "" {
		return errors.New("function name is required")
	}
	raw, err := json.Marshal(map[string]string{"functionName": name})
	if err != nil {
		return err
	}
	response, err := o.request(ctx, http.MethodDelete, "/system/functions", bytes.NewReader(raw))
	_, err = o.checked(response, err)
	return err
}

// ListFunctions returns the deployed functions with their replica counts
// (replicas 0 = scaled to zero).
func (o OpenFaaS) ListFunctions(ctx context.Context) ([]Function, error) {
	response, err := o.request(ctx, http.MethodGet, "/system/functions", nil)
	raw, err := o.checked(response, err)
	if err != nil {
		return nil, err
	}
	var listed []struct {
		Name     string            `json:"name"`
		Image    string            `json:"image"`
		Replicas int               `json:"replicas"`
		Labels   map[string]string `json:"labels"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		return nil, err
	}
	functions := make([]Function, 0, len(listed))
	for _, item := range listed {
		functions = append(functions, Function{Name: item.Name, Image: item.Image, Replicas: item.Replicas, Labels: item.Labels})
	}
	return functions, nil
}

// Invoke calls a function synchronously and returns the raw response body.
func (o OpenFaaS) Invoke(ctx context.Context, name string, payload []byte) ([]byte, error) {
	if name == "" {
		return nil, errors.New("function name is required")
	}
	response, err := o.request(ctx, http.MethodPost, "/function/"+url.PathEscape(name), bytes.NewReader(payload))
	return o.checked(response, err)
}

// InvokeAsync queues a function invocation and returns the provider call id.
func (o OpenFaaS) InvokeAsync(ctx context.Context, name string, payload []byte) (string, error) {
	if name == "" {
		return "", errors.New("function name is required")
	}
	response, err := o.request(ctx, http.MethodPost, "/async-function/"+url.PathEscape(name), bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	callID := response.Header.Get("X-Call-Id")
	if _, err = o.checked(response, nil); err != nil {
		return "", err
	}
	return callID, nil
}

type KafkaREST struct{ client *Client }

func NewKafkaREST(baseURL, token string) KafkaREST { return KafkaREST{client: New(baseURL, token)} }
func (k KafkaREST) Publish(ctx context.Context, topic string, event any) error {
	if topic == "" {
		return errors.New("topic is required")
	}
	return k.client.jsonWithMediaType(
		ctx,
		http.MethodPost,
		"/topics/"+url.PathEscape(topic),
		map[string]any{"records": []map[string]any{{"value": event}}},
		&map[string]any{},
		"application/vnd.kafka.json.v2+json",
		"application/vnd.kafka.v2+json",
	)
}
