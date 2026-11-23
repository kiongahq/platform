package httpapi

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clienttesting "k8s.io/client-go/testing"

	"github.com/kiongahq/platform/pkg/api"
)

// --- functions ---

func TestKubernetesFunctionDeployCreatesKiongaFunction(t *testing.T) {
	harness := newKubeHarness(t)
	projectID := createTestProject(t, harness.server)
	deployed := harness.do(t, http.MethodPost, "/api/v1/functions", `{"project_id":"`+projectID+`","name":"sentiment-fn","image":"registry/fn:1","cpu":"250m","env_vars":{"MODE":"fast"}}`)
	if deployed.Code != http.StatusAccepted || jsonBody(t, deployed)["status"] != "pending" {
		t.Fatalf("deploy: %d %s", deployed.Code, deployed.Body.String())
	}
	spec := harness.object(t, functionsGVR, "sentiment-fn").Object["spec"].(map[string]any)
	minimum, _, _ := unstructured.NestedInt64(spec, "replicas", "min")
	metric, _, _ := unstructured.NestedString(spec, "scaling", "metric")
	target, _, _ := unstructured.NestedInt64(spec, "scaling", "target")
	if spec["image"] != "registry/fn:1" || minimum != 0 || metric != "requests" || target != api.DefaultRequestsTarget {
		t.Fatalf("functions must default to scale-to-zero on requests: %#v", spec)
	}
	listed := harness.do(t, http.MethodGet, "/api/v1/functions", "")
	if !strings.Contains(listed.Body.String(), `"status":"pending"`) || !strings.Contains(listed.Body.String(), `"runtime":"kubernetes"`) {
		t.Fatalf("list: %s", listed.Body.String())
	}
	harness.setStatus(t, functionsGVR, "sentiment-fn", map[string]any{"readyReplicas": int64(0), "conditions": []any{readyCondition("Ready", "True", "ScaledToZero", "")}})
	if listed := harness.do(t, http.MethodGet, "/api/v1/functions", ""); !strings.Contains(listed.Body.String(), `"status":"scaled-to-zero"`) {
		t.Fatalf("status not read back: %s", listed.Body.String())
	}
	if deleted := harness.do(t, http.MethodDelete, "/api/v1/functions/sentiment-fn", ""); deleted.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", deleted.Code, deleted.Body.String())
	}
	if list, _ := harness.dynamic.Resource(functionsGVR).Namespace(testNamespace).List(t.Context(), metavListOptions()); len(list.Items) != 0 {
		t.Fatal("delete must remove the KiongaFunction")
	}
}

func TestKubernetesFunctionRejectsCPUScaleToZero(t *testing.T) {
	harness := newKubeHarness(t)
	projectID := createTestProject(t, harness.server)
	recorder := harness.do(t, http.MethodPost, "/api/v1/functions", `{"project_id":"`+projectID+`","name":"fn","image":"registry/fn:1","scaling":{"metric":"cpu","target":70}}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("cpu scaling with min 0: %d %s", recorder.Code, recorder.Body.String())
	}
}

// interceptor records what the KEDA HTTP add-on interceptor would receive.
type interceptor struct {
	mu    sync.Mutex
	hosts []string
	calls chan string
}

func newInterceptor(t *testing.T) (*interceptor, *httptest.Server) {
	recorder := &interceptor{calls: make(chan string, 4)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		recorder.mu.Lock()
		recorder.hosts = append(recorder.hosts, r.Host)
		recorder.mu.Unlock()
		recorder.calls <- string(body)
		_, _ = w.Write([]byte(`{"sentiment":"positive"}`))
	}))
	t.Cleanup(server.Close)
	return recorder, server
}

func TestKubernetesFunctionInvokeGoesThroughTheInterceptor(t *testing.T) {
	harness := newKubeHarness(t)
	recorder, upstream := newInterceptor(t)
	t.Setenv("KIONGA_KEDA_INTERCEPTOR_URL", upstream.URL)
	projectID := createTestProject(t, harness.server)
	if deployed := harness.do(t, http.MethodPost, "/api/v1/functions", `{"project_id":"`+projectID+`","name":"sentiment-fn","image":"registry/fn:1"}`); deployed.Code != http.StatusAccepted {
		t.Fatalf("deploy: %d", deployed.Code)
	}
	invoked := harness.do(t, http.MethodPost, "/api/v1/functions/sentiment-fn/invoke", `{"text":"great"}`)
	if invoked.Code != http.StatusOK || !strings.Contains(invoked.Body.String(), "positive") {
		t.Fatalf("invoke: %d %s", invoked.Code, invoked.Body.String())
	}
	if got := <-recorder.calls; got != `{"text":"great"}` {
		t.Fatalf("payload: %s", got)
	}
	if recorder.hosts[0] != "sentiment-fn."+testNamespace+".svc.cluster.local" {
		t.Fatalf("the interceptor routes by host; got %q", recorder.hosts[0])
	}
	// The operator may report the host it registered in the HTTPScaledObject.
	harness.setStatus(t, functionsGVR, "sentiment-fn", map[string]any{"host": "sentiment-fn.functions.internal"})
	if invoked := harness.do(t, http.MethodPost, "/api/v1/functions/sentiment-fn/invoke", `{}`); invoked.Code != http.StatusOK {
		t.Fatalf("invoke: %d", invoked.Code)
	}
	<-recorder.calls
	if recorder.hosts[1] != "sentiment-fn.functions.internal" {
		t.Fatalf("status.host must win: %q", recorder.hosts[1])
	}

	queued := harness.do(t, http.MethodPost, "/api/v1/functions/sentiment-fn/invoke-async", `{"text":"later"}`)
	if queued.Code != http.StatusAccepted || !strings.HasPrefix(jsonBody(t, queued)["call_id"].(string), "call-") {
		t.Fatalf("async: %d %s", queued.Code, queued.Body.String())
	}
	select {
	case got := <-recorder.calls:
		if got != `{"text":"later"}` {
			t.Fatalf("async payload: %s", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("async call never reached the function")
	}

	harness.setStatus(t, functionsGVR, "sentiment-fn", map[string]any{"phase": "Failed", "message": "image pull backoff"})
	if failed := harness.do(t, http.MethodPost, "/api/v1/functions/sentiment-fn/invoke", `{}`); failed.Code != http.StatusBadGateway || !strings.Contains(failed.Body.String(), "image pull backoff") {
		t.Fatalf("a failed function must not be invoked: %d %s", failed.Code, failed.Body.String())
	}
}

func TestKubernetesFunctionTargets(t *testing.T) {
	t.Setenv("KIONGA_KEDA_INTERCEPTOR_URL", "")
	object := func(metric string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"metadata": map[string]any{"name": "fn", "namespace": "work"},
			"spec":     map[string]any{"port": int64(9000), "scaling": map[string]any{"metric": metric}},
		}}
	}
	endpoint, host := kubeFunctions{}.target(object("requests"))
	if endpoint != "http://keda-add-ons-http-interceptor-proxy.keda:8080" || host != "fn.work.svc.cluster.local" {
		t.Fatalf("requests-scaled functions go through the interceptor: %s %s", endpoint, host)
	}
	endpoint, host = kubeFunctions{}.target(object("cpu"))
	if endpoint != "http://fn.work.svc:9000" || host != "" {
		t.Fatalf("cpu-scaled functions go to their Service: %s %s", endpoint, host)
	}
}

// --- models ---

func registerModelVersion(t *testing.T, server http.Handler, projectID, version string) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, newTestRequest(http.MethodPost, "/api/v1/models", strings.NewReader(
		`{"project_id":"`+projectID+`","name":"churn-classifier","version":"`+version+`","artifact_uri":"models:/churn-classifier/`+version+`","metrics":{"accuracy":0.93}}`)))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", recorder.Code, recorder.Body.String())
	}
	return jsonBody(t, recorder)["id"].(string)
}

func modelRow(t *testing.T, harness *kubeHarness, id string) map[string]any {
	t.Helper()
	listed := harness.do(t, http.MethodGet, "/api/v1/models", "")
	for _, item := range jsonBody(t, listed)["items"].([]any) {
		if row := item.(map[string]any); row["id"] == id {
			return row
		}
	}
	t.Fatalf("model %s not listed", id)
	return nil
}

func TestKubernetesModelDeployIsPendingUntilReady(t *testing.T) {
	harness := newKubeHarness(t)
	projectID := createTestProject(t, harness.server)
	modelID := registerModelVersion(t, harness.server, projectID, "3")
	deployed := harness.do(t, http.MethodPost, "/api/v1/models/"+modelID+"/deploy", `{"canary_weight":0}`)
	body := jsonBody(t, deployed)
	if deployed.Code != http.StatusAccepted || body["deployment_status"] != "pending" || body["endpoint_url"] != nil {
		t.Fatalf("deploy must report pending without an endpoint: %d %s", deployed.Code, deployed.Body.String())
	}
	name := api.ModelDeploymentDNSName(projectID, "churn-classifier")
	spec := harness.object(t, modelDeploymentsGVR, name).Object["spec"].(map[string]any)
	if spec["modelName"] != "churn-classifier" || spec["version"] != "3" || spec["canaryWeight"] != int64(0) {
		t.Fatalf("unexpected KiongaModelDeployment spec: %#v", spec)
	}
	if row := modelRow(t, harness, modelID); row["deployment_status"] != "pending" {
		t.Fatalf("not ready yet must stay pending: %v", row)
	}
	harness.setStatus(t, modelDeploymentsGVR, name, map[string]any{"url": "http://churn-classifier.kionga-apps.svc", "conditions": []any{readyCondition("Ready", "True", "", "")}})
	if row := modelRow(t, harness, modelID); row["deployment_status"] != "serving" || row["endpoint_url"] != "http://churn-classifier.kionga-apps.svc" {
		t.Fatalf("ready must report serving with the endpoint: %v", row)
	}
	harness.setStatus(t, modelDeploymentsGVR, name, map[string]any{"phase": "Failed", "conditions": []any{readyCondition("Ready", "False", "Failed", "artifact s3://models/x not found")}})
	row := modelRow(t, harness, modelID)
	if row["deployment_status"] != "failed" || row["deployment_message"] != "artifact s3://models/x not found" || row["endpoint_url"] != nil {
		t.Fatalf("failure must carry the reason: %v", row)
	}
	detail := harness.do(t, http.MethodGet, "/api/v1/models/"+modelID+"/deployment", "")
	if detail.Code != http.StatusOK || jsonBody(t, detail)["status"] != "failed" {
		t.Fatalf("deployment detail: %d %s", detail.Code, detail.Body.String())
	}
}

func TestKubernetesModelDeployFailsClosedWhenKubernetesRefuses(t *testing.T) {
	harness := newKubeHarness(t)
	projectID := createTestProject(t, harness.server)
	modelID := registerModelVersion(t, harness.server, projectID, "3")
	harness.dynamic.PrependReactor("create", "kiongamodeldeployments", rejectAll)
	deployed := harness.do(t, http.MethodPost, "/api/v1/models/"+modelID+"/deploy", `{"canary_weight":0}`)
	if deployed.Code != http.StatusBadGateway {
		t.Fatalf("deploy: %d %s", deployed.Code, deployed.Body.String())
	}
	if row := modelRow(t, harness, modelID); row["deployment_status"] == "pending" || row["deployment_status"] == "serving" {
		t.Fatalf("a refused deployment must not be recorded as started: %v", row)
	}
}

func TestKubernetesCanarySplitsInvocationsByWeight(t *testing.T) {
	harness := newKubeHarness(t)
	projectID := createTestProject(t, harness.server)
	stableID := registerModelVersion(t, harness.server, projectID, "3")
	canaryID := registerModelVersion(t, harness.server, projectID, "4")

	if refused := harness.do(t, http.MethodPost, "/api/v1/models/"+canaryID+"/deploy", `{"canary_weight":20}`); refused.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a canary without a serving stable version must be refused: %d %s", refused.Code, refused.Body.String())
	}

	var mu sync.Mutex
	counts := map[string]int{}
	serve := func(label string) *httptest.Server {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/invocations" {
				t.Errorf("unexpected path %s", r.URL.Path)
			}
			mu.Lock()
			counts[label]++
			mu.Unlock()
			_, _ = w.Write([]byte(`{"predictions":["` + label + `"]}`))
		}))
		t.Cleanup(server.Close)
		return server
	}
	stable, canary := serve("stable"), serve("canary")
	name := api.ModelDeploymentDNSName(projectID, "churn-classifier")

	if deployed := harness.do(t, http.MethodPost, "/api/v1/models/"+stableID+"/deploy", `{"canary_weight":0}`); deployed.Code != http.StatusAccepted {
		t.Fatalf("stable deploy: %d %s", deployed.Code, deployed.Body.String())
	}
	harness.setStatus(t, modelDeploymentsGVR, name, map[string]any{"url": stable.URL, "conditions": []any{readyCondition("Ready", "True", "", "")}})
	if deployed := harness.do(t, http.MethodPost, "/api/v1/models/"+canaryID+"/deploy", `{"canary_weight":20}`); deployed.Code != http.StatusAccepted {
		t.Fatalf("canary deploy: %d %s", deployed.Code, deployed.Body.String())
	}
	spec := harness.object(t, modelDeploymentsGVR, name).Object["spec"].(map[string]any)
	if spec["version"] != "3" || spec["canaryVersion"] != "4" || spec["canaryWeight"] != int64(20) {
		t.Fatalf("canary spec: %#v", spec)
	}
	// Until the canary is ready every call goes to stable.
	if predicted := harness.do(t, http.MethodPost, "/api/v1/models/"+stableID+"/predict", `{}`); predicted.Header().Get("X-Kionga-Model-Version") != "3" {
		t.Fatalf("unready canary received traffic: %s", predicted.Body.String())
	}
	harness.setStatus(t, modelDeploymentsGVR, name, map[string]any{
		"url": stable.URL, "canaryURL": canary.URL,
		"conditions": []any{readyCondition("Ready", "True", "", ""), readyCondition("CanaryReady", "True", "", "")},
	})
	mu.Lock()
	counts = map[string]int{}
	mu.Unlock()
	for i := 0; i < 100; i++ {
		id := stableID
		if i%2 == 1 {
			id = canaryID // either version's id routes through the split
		}
		if predicted := harness.do(t, http.MethodPost, "/api/v1/models/"+id+"/predict", `{"inputs":[1]}`); predicted.Code != http.StatusOK {
			t.Fatalf("predict: %d %s", predicted.Code, predicted.Body.String())
		}
	}
	if counts["canary"] != 20 || counts["stable"] != 80 {
		t.Fatalf("100 calls at weight 20 must split exactly 80/20: %v", counts)
	}
	if row := modelRow(t, harness, canaryID); row["deployment_status"] != "serving" || row["endpoint_url"] != canary.URL {
		t.Fatalf("canary status: %v", row)
	}

	// Rolling the canary back returns all traffic to stable.
	harness.do(t, http.MethodPost, "/api/v1/models/"+canaryID+"/promote", `{"stage":"staging"}`)
	if rolled := harness.do(t, http.MethodPost, "/api/v1/models/"+canaryID+"/rollback", ""); rolled.Code != http.StatusOK {
		t.Fatalf("rollback: %d %s", rolled.Code, rolled.Body.String())
	}
	spec = harness.object(t, modelDeploymentsGVR, name).Object["spec"].(map[string]any)
	if _, has := spec["canaryVersion"]; has || spec["canaryWeight"] != int64(0) {
		t.Fatalf("rollback must remove the canary: %#v", spec)
	}
}

func TestCanarySplitterIsExactAndDeterministic(t *testing.T) {
	for _, weight := range []int{0, 1, 7, 20, 50, 99, 100} {
		splitter := &canarySplitter{}
		for window := 0; window < 5; window++ {
			canary := 0
			for i := 0; i < 100; i++ {
				if splitter.toCanary("model-a", "", weight) {
					canary++
				}
			}
			if canary != weight {
				t.Fatalf("weight %d window %d: %d canary calls", weight, window, canary)
			}
		}
	}
	// Deployments keep separate sequences.
	splitter := &canarySplitter{}
	splitter.toCanary("model-a", "", 50)
	if first, second := splitter.toCanary("model-b", "", 50), splitter.toCanary("model-b", "", 50); first || !second {
		t.Fatal("a new deployment must start its own sequence")
	}
	// A routing key is sticky and independent of the counter.
	sticky := splitter.toCanary("model-a", "customer-42", 30)
	for i := 0; i < 20; i++ {
		if splitter.toCanary("model-a", "customer-42", 30) != sticky {
			t.Fatal("routing key must always pick the same variant")
		}
	}
	keyed := 0
	for i := 0; i < 10000; i++ {
		if splitter.toCanary("model-a", "key-"+strconv.Itoa(i), 30) {
			keyed++
		}
	}
	if keyed < 2700 || keyed > 3300 {
		t.Fatalf("hashed keys must spread near the weight: %d/10000 at 30", keyed)
	}
}

func metavListOptions() metav1.ListOptions { return metav1.ListOptions{} }

func rejectAll(clienttesting.Action) (bool, runtime.Object, error) {
	return true, nil, errors.New("admission webhook denied the request")
}
