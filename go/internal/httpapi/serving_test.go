package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// registerDeployableModel registers a model whose metrics pass the evaluation
// gate and returns its id.
func registerDeployableModel(t *testing.T, server http.Handler) string {
	t.Helper()
	projectID := createTestProject(t, server)
	register := httptest.NewRequest(http.MethodPost, "/api/v1/models", strings.NewReader(
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
		_, _ = w.Write([]byte(`{"name":"churn-classifier","endpoint":"http://mlaiops-serve-churn-classifier:5001"}`))
	}))
	defer manager.Close()
	t.Setenv("SERVING_MANAGER_URL", manager.URL)

	server := testServer()
	modelID := registerDeployableModel(t, server)
	deploy := httptest.NewRequest(http.MethodPost, "/api/v1/models/"+modelID+"/deploy", strings.NewReader(`{"canary_weight":0}`))
	deployed := httptest.NewRecorder()
	server.ServeHTTP(deployed, deploy)
	if deployed.Code != http.StatusAccepted {
		t.Fatalf("deploy failed: %d %s", deployed.Code, deployed.Body.String())
	}
	body := deployed.Body.String()
	if !strings.Contains(body, `"endpoint_url":"http://mlaiops-serve-churn-classifier:5001"`) || !strings.Contains(body, `"deployment_status":"serving"`) {
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
	deploy := httptest.NewRequest(http.MethodPost, "/api/v1/models/"+modelID+"/deploy", strings.NewReader(`{"canary_weight":0}`))
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
	deploy := httptest.NewRequest(http.MethodPost, "/api/v1/models/"+modelID+"/deploy", strings.NewReader(`{"canary_weight":0}`))
	server.ServeHTTP(httptest.NewRecorder(), deploy)

	predict := httptest.NewRequest(http.MethodPost, "/api/v1/models/"+modelID+"/predict", strings.NewReader(`{"inputs":[[1,2,3]]}`))
	predicted := httptest.NewRecorder()
	server.ServeHTTP(predicted, predict)
	if predicted.Code != http.StatusOK || !strings.Contains(predicted.Body.String(), `"predictions":[1]`) {
		t.Fatalf("prediction proxy failed: %d %s", predicted.Code, predicted.Body.String())
	}
}

func TestPredictWithoutLiveEndpointIsConflict(t *testing.T) {
	server := testServer()
	modelID := registerDeployableModel(t, server)
	predict := httptest.NewRequest(http.MethodPost, "/api/v1/models/"+modelID+"/predict", strings.NewReader(`{}`))
	predicted := httptest.NewRecorder()
	server.ServeHTTP(predicted, predict)
	if predicted.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d %s", predicted.Code, predicted.Body.String())
	}
}
