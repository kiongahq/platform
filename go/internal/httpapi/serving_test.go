package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// registerDeployableModel registers a model whose metrics pass the evaluation
// gate and returns its id.
func registerDeployableModel(t *testing.T, server http.Handler) string {
	t.Helper()
	projectID := createTestProject(t, server)
	register := newTestRequest(http.MethodPost, "/api/v1/models", strings.NewReader(
		`{"project_id":"`+projectID+`","name":"churn-classifier","version":"3","artifact_uri":"models:/churn-classifier/3","serving_image":"ghcr.io/acme/churn-xgboost:3","metrics":{"accuracy":0.93}}`))
	registered := httptest.NewRecorder()
	server.ServeHTTP(registered, register)
	if registered.Code != http.StatusCreated {
		t.Fatalf("model registration failed: %d %s", registered.Code, registered.Body.String())
	}
	return strings.Split(strings.Split(registered.Body.String(), `"id":"`)[1], `"`)[0]
}

func TestDeployModelStartsRealServing(t *testing.T) {
	manager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/deployments" {
			t.Fatalf("unexpected serving manager call %s %s", r.Method, r.URL.Path)
		}
		var request struct {
			Name         string `json:"name"`
			ArtifactURI  string `json:"artifact_uri"`
			ServingImage string `json:"serving_image"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Name != "churn-classifier" || request.ArtifactURI != "models:/churn-classifier/3" || request.ServingImage != "ghcr.io/acme/churn-xgboost:3" {
			t.Fatalf("model serving metadata was not forwarded: %#v", request)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"name":"churn-classifier","endpoint":"http://kionga-serve-churn-classifier:5001"}`))
	}))
	defer manager.Close()
	t.Setenv("SERVING_MANAGER_URL", manager.URL)

	server := testServer()
	modelID := registerDeployableModel(t, server)
	deploy := newTestRequest(http.MethodPost, "/api/v1/models/"+modelID+"/deploy", strings.NewReader(`{"canary_weight":0}`))
	deployed := httptest.NewRecorder()
	server.ServeHTTP(deployed, deploy)
	if deployed.Code != http.StatusAccepted {
		t.Fatalf("deploy failed: %d %s", deployed.Code, deployed.Body.String())
	}
	body := deployed.Body.String()
	if !strings.Contains(body, `"endpoint_url":"http://kionga-serve-churn-classifier:5001"`) || !strings.Contains(body, `"deployment_status":"serving"`) {
		t.Fatalf("live endpoint not recorded: %s", body)
	}
}

func TestDeployModelFailsClosedWhenServingRejects(t *testing.T) {
	manager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":"image missing"}`))
	}))
	defer manager.Close()
	t.Setenv("SERVING_MANAGER_URL", manager.URL)

	server := testServer()
	modelID := registerDeployableModel(t, server)
	deploy := newTestRequest(http.MethodPost, "/api/v1/models/"+modelID+"/deploy", strings.NewReader(`{"canary_weight":0}`))
	deployed := httptest.NewRecorder()
	server.ServeHTTP(deployed, deploy)
	if deployed.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 fail-closed, got %d %s", deployed.Code, deployed.Body.String())
	}
}

func TestPredictProxiesToLiveEndpoint(t *testing.T) {
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/invocations" {
			t.Fatalf("unexpected endpoint path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"predictions":[1]}`))
	}))
	defer endpoint.Close()
	manager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"endpoint":"` + endpoint.URL + `"}`))
	}))
	defer manager.Close()
	t.Setenv("SERVING_MANAGER_URL", manager.URL)

	server := testServer()
	modelID := registerDeployableModel(t, server)
	deploy := newTestRequest(http.MethodPost, "/api/v1/models/"+modelID+"/deploy", strings.NewReader(`{"canary_weight":0}`))
	server.ServeHTTP(httptest.NewRecorder(), deploy)

	predict := newTestRequest(http.MethodPost, "/api/v1/models/"+modelID+"/predict", strings.NewReader(`{"inputs":[[1,2,3]]}`))
	predicted := httptest.NewRecorder()
	server.ServeHTTP(predicted, predict)
	if predicted.Code != http.StatusOK || !strings.Contains(predicted.Body.String(), `"predictions":[1]`) {
		t.Fatalf("prediction proxy failed: %d %s", predicted.Code, predicted.Body.String())
	}
}

func TestPredictWithoutLiveEndpointIsConflict(t *testing.T) {
	server := testServer()
	modelID := registerDeployableModel(t, server)
	predict := newTestRequest(http.MethodPost, "/api/v1/models/"+modelID+"/predict", strings.NewReader(`{}`))
	predicted := httptest.NewRecorder()
	server.ServeHTTP(predicted, predict)
	if predicted.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d %s", predicted.Code, predicted.Body.String())
	}
}

func TestServingRequestTimeoutStaysAboveTheManagerReadinessWait(t *testing.T) {
	for raw, want := range map[string]time.Duration{"": 90 * time.Second, "150": 150 * time.Second, "0": 90 * time.Second, "-5": 90 * time.Second, "soon": 90 * time.Second, "7200": 90 * time.Second} {
		t.Setenv("KIONGA_SERVING_REQUEST_TIMEOUT_SECONDS", raw)
		if got := servingRequestTimeout(); got != want {
			t.Errorf("KIONGA_SERVING_REQUEST_TIMEOUT_SECONDS=%q = %s, want %s", raw, got, want)
		}
	}
	// serving's SERVE_READY_TIMEOUT_SECONDS defaults to 80.
	if defaultServingRequestTimeout <= 80*time.Second {
		t.Fatalf("default gateway wait %s must exceed the serving manager's 80s readiness wait", defaultServingRequestTimeout)
	}
}

// A model server that takes longer than the listener's write timeout to
// become ready must still produce a response: deployModel extends its own
// write deadline to cover the serving manager wait.
func TestDeployModelOutlivesTheListenerWriteTimeout(t *testing.T) {
	manager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond) // the manager waiting for readiness
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"endpoint":"http://kionga-serve-churn-classifier:5001"}`))
	}))
	defer manager.Close()
	t.Setenv("SERVING_MANAGER_URL", manager.URL)
	t.Setenv("KIONGA_SERVING_REQUEST_TIMEOUT_SECONDS", "5")
	handler := testServer()
	modelID := registerDeployableModel(t, handler)
	gateway := httptest.NewUnstartedServer(handler)
	gateway.Config.WriteTimeout = 100 * time.Millisecond // stands in for the 30s production limit
	gateway.Start()
	defer gateway.Close()
	request, _ := http.NewRequest(http.MethodPost, gateway.URL+"/api/v1/models/"+modelID+"/deploy", strings.NewReader(`{"canary_weight":0}`))
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("deploy response was cut off by the write timeout: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("deploy = %d", response.StatusCode)
	}
}
