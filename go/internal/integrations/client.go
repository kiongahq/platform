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

type KFP struct{ client *Client }

func NewKFP(baseURL, token string) KFP { return KFP{client: New(baseURL, token)} }
func (k KFP) Submit(ctx context.Context, experimentID, pipelineVersionID, name string, parameters map[string]string) (map[string]any, error) {
	inputs := make([]map[string]string, 0, len(parameters))
	for key, value := range parameters {
		inputs = append(inputs, map[string]string{"name": key, "value": value})
	}
	payload := map[string]any{"display_name": name, "experiment_id": experimentID, "pipeline_version_reference": map[string]string{"pipeline_version_id": pipelineVersionID}, "runtime_config": map[string]any{"parameters": inputs}}
	var result map[string]any
	err := k.client.JSON(ctx, http.MethodPost, "/apis/v2beta1/runs", payload, &result)
	return result, err
}

type MLflow struct{ client *Client }

func NewMLflow(baseURL, token string) MLflow { return MLflow{client: New(baseURL, token)} }
func (m MLflow) TransitionStage(ctx context.Context, name, version, stage string) error {
	return m.client.JSON(ctx, http.MethodPost, "/api/2.0/mlflow/model-versions/transition-stage", map[string]any{"name": name, "version": version, "stage": stage, "archive_existing_versions": stage == "Production"}, &map[string]any{})
}

type Langfuse struct{ client *Client }

func NewLangfuse(baseURL, token string) Langfuse { return Langfuse{client: New(baseURL, token)} }
func (l Langfuse) Ingest(ctx context.Context, batch []map[string]any) error {
	return l.client.JSON(ctx, http.MethodPost, "/api/public/ingestion", map[string]any{"batch": batch}, &map[string]any{})
}

// Prefect drives real pipeline execution against a self-hosted Prefect
// server (the Compose-native fulfilment of the pipeline engine; KFP
// remains the Kubernetes path).
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
