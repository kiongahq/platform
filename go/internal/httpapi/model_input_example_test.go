package httpapi

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/kiongahq/platform/internal/auth"
)

// stubMLflow serves GET /model-versions/get-artifact for one registered model
// version from a map of file name to content, and records what was asked.
type stubMLflow struct {
	*httptest.Server
	mu    sync.Mutex
	paths []string
	auth  []string
}

func newStubMLflow(t *testing.T, files map[string]string) *stubMLflow {
	t.Helper()
	stub := &stubMLflow{}
	stub.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/model-versions/get-artifact" {
			t.Errorf("unexpected MLflow call %s %s", r.Method, r.URL.Path)
		}
		query := r.URL.Query()
		stub.mu.Lock()
		stub.paths = append(stub.paths, query.Get("path"))
		stub.auth = append(stub.auth, r.Header.Get("Authorization"))
		stub.mu.Unlock()
		if query.Get("name") != "churn-classifier" || query.Get("version") != "3" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error_code":"RESOURCE_DOES_NOT_EXIST","message":"Model Version not found"}`))
			return
		}
		content, found := files[query.Get("path")]
		if !found {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// "status:<code>" stands for MLflow failing on that one file.
		if code, failing := strings.CutPrefix(content, "status:"); failing {
			status, _ := strconv.Atoi(code)
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`upstream said: confidential detail`))
			return
		}
		_, _ = w.Write([]byte(content))
	}))
	t.Cleanup(stub.Close)
	t.Setenv("MLFLOW_URL", stub.URL)
	return stub
}

func (s *stubMLflow) asked() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.paths...)
}

const mlmodelSignature = `signature:
  inputs: '[{"type": "double", "name": "tenure", "required": true}, {"type": "long", "name": "seats", "required": true}, {"type": "string", "name": "plan", "required": false}, {"type": "boolean", "name": "trial", "required": true}]'
  outputs: '[{"type": "tensor", "tensor-spec": {"dtype": "int64", "shape": [-1]}}]'
  params: null
`

const mlmodelWithExample = `artifact_path: model
flavors:
  python_function:
    loader_module: mlflow.sklearn
saved_input_example_info:
  artifact_path: input_example.json
  serving_input_path: serving_input_example.json
  pandas_orient: split
  type: dataframe
` + mlmodelSignature

type inputExampleBody struct {
	ModelID string         `json:"model_id"`
	Name    string         `json:"name"`
	Version string         `json:"version"`
	Source  string         `json:"source"`
	Payload map[string]any `json:"payload"`
	Error   string         `json:"error"`
	Message string         `json:"message"`
	Steps   []string       `json:"steps"`
}

func getInputExample(t *testing.T, server http.Handler, modelID string) (*httptest.ResponseRecorder, inputExampleBody) {
	t.Helper()
	response := httptest.NewRecorder()
	server.ServeHTTP(response, newTestRequest(http.MethodGet, "/api/v1/models/"+modelID+"/input-example", nil))
	var body inputExampleBody
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %d %s", response.Code, response.Body.String())
	}
	return response, body
}

func TestInputExampleReturnsTheSavedServingExample(t *testing.T) {
	stub := newStubMLflow(t, map[string]string{
		"MLmodel":                    mlmodelWithExample,
		"serving_input_example.json": "{\n  \"dataframe_split\": {\"columns\": [\"tenure\", \"seats\"], \"data\": [[12.5, 4]]}\n}",
		"input_example.json":         `{"columns": ["tenure", "seats"], "data": [[12.5, 4]]}`,
	})
	t.Setenv("MLFLOW_TOKEN", "mlflow-secret-token")
	server := testServer()
	modelID := registerDeployableModel(t, server)
	response, body := getInputExample(t, server, modelID)
	if response.Code != http.StatusOK || body.Source != "input_example" || body.ModelID != modelID || body.Name != "churn-classifier" || body.Version != "3" {
		t.Fatalf("unexpected answer: %d %s", response.Code, response.Body.String())
	}
	want := `{"dataframe_split":{"columns":["tenure","seats"],"data":[[12.5,4]]}}`
	if got, _ := json.Marshal(body.Payload); string(got) != want {
		t.Fatalf("payload = %s, want %s", got, want)
	}
	if asked := stub.asked(); len(asked) != 2 || asked[0] != "MLmodel" || asked[1] != "serving_input_example.json" {
		t.Fatalf("MLflow was asked for %v", asked)
	}
	for _, header := range stub.auth {
		if header != "Bearer mlflow-secret-token" {
			t.Fatalf("MLflow call carried Authorization %q", header)
		}
	}
	if strings.Contains(response.Body.String(), "mlflow-secret-token") {
		t.Fatal("the response leaked the MLflow token")
	}
}

// Models logged before MLflow wrote serving_input_example.json carry only the
// raw example; it is wrapped by its declared type.
func TestInputExampleWrapsTheRawSavedExampleByType(t *testing.T) {
	for name, tc := range map[string]struct{ info, example, want string }{
		"dataframe split":   {"type: dataframe\n  pandas_orient: split", `{"columns": ["a"], "index": [0], "data": [[1.5]]}`, `{"dataframe_split":{"columns":["a"],"data":[[1.5]]}}`},
		"dataframe records": {"type: dataframe\n  pandas_orient: records", `[{"a": 1.5}]`, `{"dataframe_records":[{"a":1.5}]}`},
		"ndarray keyed":     {"type: ndarray\n  format: tf-serving", `{"inputs": [[1, 2]]}`, `{"inputs":[[1,2]]}`},
		"ndarray bare":      {"type: ndarray", `[[1, 2]]`, `{"inputs":[[1,2]]}`},
		"json object":       {"type: json_object", `{"messages": [{"role": "user", "content": "hi"}]}`, `{"inputs":{"messages":[{"content":"hi","role":"user"}]}}`},
	} {
		t.Run(name, func(t *testing.T) {
			newStubMLflow(t, map[string]string{
				"MLmodel":            "saved_input_example_info:\n  artifact_path: input_example.json\n  " + tc.info + "\n",
				"input_example.json": tc.example,
			})
			server := testServer()
			response, body := getInputExample(t, server, registerDeployableModel(t, server))
			if got, _ := json.Marshal(body.Payload); response.Code != http.StatusOK || body.Source != "input_example" || string(got) != tc.want {
				t.Fatalf("got %d source=%q payload=%s, want %s", response.Code, body.Source, got, tc.want)
			}
		})
	}
}

func TestInputExampleDerivesASkeletonFromTheSignature(t *testing.T) {
	stub := newStubMLflow(t, map[string]string{"MLmodel": mlmodelSignature})
	server := testServer()
	response, body := getInputExample(t, server, registerDeployableModel(t, server))
	if response.Code != http.StatusOK || body.Source != "signature" {
		t.Fatalf("unexpected answer: %d %s", response.Code, response.Body.String())
	}
	want := `{"dataframe_split":{"columns":["tenure","seats","plan","trial"],"data":[[0.5,0,"string",false]]}}`
	if got, _ := json.Marshal(body.Payload); string(got) != want {
		t.Fatalf("payload = %s, want %s", got, want)
	}
	if asked := stub.asked(); len(asked) != 1 || asked[0] != "MLmodel" {
		t.Fatalf("MLflow was asked for %v", asked)
	}
}

func TestSignatureSkeletonShapes(t *testing.T) {
	for name, tc := range map[string]struct{ inputs, want string }{
		"unnamed columns use positions": {`[{"type": "double"}, {"type": "long"}]`, `{"dataframe_split":{"columns":[0,1],"data":[[0.5,0]]}}`},
		"nested column types": {`[{"type": "array", "items": {"type": "string"}, "name": "tags"}, {"type": "object", "properties": {"age": {"type": "long"}}, "name": "user"}, {"type": "datetime", "name": "at"}]`,
			`{"dataframe_split":{"columns":["tags","user","at"],"data":[[["string"],{"age":0},"2024-01-01T00:00:00"]]}}`},
		"one unnamed tensor": {`[{"type": "tensor", "tensor-spec": {"dtype": "float64", "shape": [-1, 3]}}]`, `{"inputs":[[0.5,0.5,0.5]]}`},
		"named tensors":      {`[{"type": "tensor", "name": "ids", "tensor-spec": {"dtype": "int64", "shape": [-1, 2]}}, {"type": "tensor", "name": "mask", "tensor-spec": {"dtype": "bool", "shape": [-1]}}]`, `{"inputs":{"ids":[[0,0]],"mask":[false]}}`},
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := signatureSkeleton(tc.inputs)
			if !ok || string(got) != tc.want {
				t.Fatalf("got %s (ok=%v), want %s", got, ok, tc.want)
			}
		})
	}
	for name, inputs := range map[string]string{
		"empty":                `[]`,
		"not a list":           `{"type": "double"}`,
		"image-sized tensor":   `[{"type": "tensor", "tensor-spec": {"dtype": "uint8", "shape": [-1, 224, 224, 3]}}]`,
		"two unnamed tensors":  `[{"type": "tensor", "tensor-spec": {"dtype": "int64", "shape": [-1]}}, {"type": "tensor", "tensor-spec": {"dtype": "int64", "shape": [-1]}}]`,
		"columns with tensors": `[{"type": "double", "name": "a"}, {"type": "tensor", "tensor-spec": {"dtype": "int64", "shape": [-1]}}]`,
	} {
		t.Run(name, func(t *testing.T) {
			if got, ok := signatureSkeleton(inputs); ok {
				t.Fatalf("expected no skeleton, got %s", got)
			}
		})
	}
}

// An unreadable saved example falls back to the signature, marked as derived.
func TestInputExampleFallsBackToTheSignatureWhenTheExampleIsUnreadable(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"missing":   {"MLmodel": mlmodelWithExample},
		"not JSON":  {"MLmodel": mlmodelWithExample, "serving_input_example.json": "not json", "input_example.json": "not json"},
		"oversized": {"MLmodel": mlmodelWithExample, "serving_input_example.json": `{"inputs":"` + strings.Repeat("x", maxInputExampleBytes) + `"}`, "input_example.json": `{"columns":["a"],"data":[["` + strings.Repeat("x", maxInputExampleBytes) + `"]]}`},
	} {
		t.Run(name, func(t *testing.T) {
			newStubMLflow(t, files)
			server := testServer()
			response, body := getInputExample(t, server, registerDeployableModel(t, server))
			if response.Code != http.StatusOK || body.Source != "signature" {
				t.Fatalf("unexpected answer: %d source=%q", response.Code, body.Source)
			}
			if response.Body.Len() > maxInputExampleBytes {
				t.Fatalf("response is %d bytes", response.Body.Len())
			}
		})
	}
}

// MLflow refusing or failing on the example file is an MLflow failure. It is
// reported, never hidden behind a skeleton derived from the signature.
func TestInputExampleReportsMLflowFailingOnTheExampleFile(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"serving example refused": {"MLmodel": mlmodelWithExample, "serving_input_example.json": "status:401", "input_example.json": `{"columns": ["a"], "data": [[1]]}`},
		"serving example failing": {"MLmodel": mlmodelWithExample, "serving_input_example.json": "status:500", "input_example.json": `{"columns": ["a"], "data": [[1]]}`},
		"raw example forbidden":   {"MLmodel": mlmodelWithExample, "input_example.json": "status:403"},
		"raw example failing":     {"MLmodel": mlmodelWithExample, "input_example.json": "status:503"},
		"raw example redirected":  {"MLmodel": mlmodelWithExample, "input_example.json": "status:302"},
	} {
		t.Run(name, func(t *testing.T) {
			newStubMLflow(t, files)
			server := testServer()
			response, body := getInputExample(t, server, registerDeployableModel(t, server))
			if response.Code != http.StatusBadGateway || body.Error != "mlflow_unavailable" || body.Source != "" || body.Payload != nil {
				t.Fatalf("unexpected answer: %d %s", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "confidential") {
				t.Fatalf("the response repeats the MLflow body: %s", response.Body.String())
			}
		})
	}
}

// The example path comes from the artifact; it must not address anything but
// a file beside MLmodel.
func TestInputExampleRefusesUnsafeExamplePaths(t *testing.T) {
	for _, path := range []string{"../secret.json", "/etc/passwd", "a/b.json", `a\b.json`, "x?path=MLmodel", ".."} {
		stub := newStubMLflow(t, map[string]string{
			"MLmodel": "saved_input_example_info:\n  artifact_path: '" + path + "'\n  serving_input_path: '" + path + "'\n  type: json_object\n",
			path:      `{"inputs": 1}`,
		})
		server := testServer()
		response, body := getInputExample(t, server, registerDeployableModel(t, server))
		if response.Code != http.StatusNotFound || body.Error != "input_example_unavailable" {
			t.Fatalf("%q: unexpected answer: %d %s", path, response.Code, response.Body.String())
		}
		if asked := stub.asked(); len(asked) != 1 {
			t.Fatalf("%q: MLflow was asked for %v", path, asked)
		}
	}
}

func TestInputExampleUnavailableWhenTheModelHasNeither(t *testing.T) {
	newStubMLflow(t, map[string]string{"MLmodel": "artifact_path: model\nflavors:\n  sklearn:\n    pickled_model: model.pkl\n"})
	server := testServer()
	response, body := getInputExample(t, server, registerDeployableModel(t, server))
	if response.Code != http.StatusNotFound || body.Error != "input_example_unavailable" || !strings.Contains(body.Message, "no readable saved input example and no signature") {
		t.Fatalf("unexpected answer: %d %s", response.Code, response.Body.String())
	}
}

func TestInputExampleUnavailableWhenMLflowDoesNotKnowTheVersion(t *testing.T) {
	newStubMLflow(t, map[string]string{"MLmodel": mlmodelSignature})
	server := testServer()
	projectID := createTestProject(t, server)
	registered := httptest.NewRecorder()
	server.ServeHTTP(registered, newTestRequest(http.MethodPost, "/api/v1/models", strings.NewReader(`{"project_id":"`+projectID+`","name":"other-model","version":"1","artifact_uri":"models:/other-model/1","metrics":{"accuracy":0.93}}`)))
	if registered.Code != http.StatusCreated {
		t.Fatalf("model registration failed: %d %s", registered.Code, registered.Body.String())
	}
	modelID := strings.Split(strings.Split(registered.Body.String(), `"id":"`)[1], `"`)[0]
	response, body := getInputExample(t, server, modelID)
	if response.Code != http.StatusNotFound || body.Error != "input_example_unavailable" {
		t.Fatalf("unexpected answer: %d %s", response.Code, response.Body.String())
	}
}

func TestInputExampleNotConfiguredWithoutMLflowURL(t *testing.T) {
	t.Setenv("MLFLOW_URL", "")
	server := testServer()
	response, body := getInputExample(t, server, registerDeployableModel(t, server))
	if response.Code != http.StatusConflict || body.Error != "not_configured" || len(body.Steps) == 0 || !strings.Contains(body.Message, "MLFLOW_URL") {
		t.Fatalf("unexpected answer: %d %s", response.Code, response.Body.String())
	}
}

func TestInputExampleWhenMLflowIsDown(t *testing.T) {
	stub := newStubMLflow(t, nil)
	address := stub.URL
	stub.Close()
	server := testServer()
	response, body := getInputExample(t, server, registerDeployableModel(t, server))
	if response.Code != http.StatusBadGateway || body.Error != "mlflow_unavailable" {
		t.Fatalf("unexpected answer: %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), strings.TrimPrefix(address, "http://")) {
		t.Fatalf("the response names the MLflow address: %s", response.Body.String())
	}
}

func TestInputExampleWhenMLflowRefusesTheGateway(t *testing.T) {
	mlflow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`upstream said: confidential detail`))
	}))
	defer mlflow.Close()
	t.Setenv("MLFLOW_URL", mlflow.URL)
	server := testServer()
	response, body := getInputExample(t, server, registerDeployableModel(t, server))
	if response.Code != http.StatusBadGateway || body.Error != "mlflow_unavailable" || !strings.Contains(body.Message, "MLFLOW_TOKEN") {
		t.Fatalf("unexpected answer: %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "confidential") {
		t.Fatalf("the response repeats the MLflow body: %s", response.Body.String())
	}
}

func TestInputExampleRefusesAnOversizedMLmodel(t *testing.T) {
	newStubMLflow(t, map[string]string{"MLmodel": mlmodelSignature + "padding: '" + strings.Repeat("x", maxMLmodelBytes) + "'\n"})
	server := testServer()
	response, body := getInputExample(t, server, registerDeployableModel(t, server))
	if response.Code != http.StatusNotFound || body.Error != "input_example_unavailable" || !strings.Contains(body.Message, "larger") {
		t.Fatalf("unexpected answer: %d %.300s", response.Code, response.Body.String())
	}
}

func TestInputExampleUnparsableMLmodel(t *testing.T) {
	newStubMLflow(t, map[string]string{"MLmodel": "signature: [unterminated\n  secret-artifact-content"})
	server := testServer()
	response, body := getInputExample(t, server, registerDeployableModel(t, server))
	if response.Code != http.StatusNotFound || body.Error != "input_example_unavailable" || strings.Contains(response.Body.String(), "secret-artifact-content") {
		t.Fatalf("unexpected answer: %d %s", response.Code, response.Body.String())
	}
}

func TestInputExampleNeverLogsArtifactContents(t *testing.T) {
	var logged bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logged)
	defer log.SetOutput(previous)
	for _, files := range []map[string]string{
		{"MLmodel": mlmodelWithExample, "serving_input_example.json": `{"inputs": "private-example-value"}`},
		{"MLmodel": "private-example-value: [broken"},
		{"MLmodel": mlmodelWithExample, "serving_input_example.json": `private-example-value`},
	} {
		newStubMLflow(t, files)
		server := testServer()
		getInputExample(t, server, registerDeployableModel(t, server))
	}
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error_code":"private-example-value","message":"private-example-value"}`))
	}))
	defer failing.Close()
	t.Setenv("MLFLOW_URL", failing.URL)
	server := testServer()
	if response, _ := getInputExample(t, server, registerDeployableModel(t, server)); response.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", response.Code)
	}
	if !strings.Contains(logged.String(), "model input example:") {
		t.Fatalf("the MLflow failure was not logged: %q", logged.String())
	}
	if strings.Contains(logged.String(), "private-example-value") {
		t.Fatalf("artifact or response content was logged: %s", logged.String())
	}
}

func TestInputExampleHubReferenceNeverContactsMLflow(t *testing.T) {
	stub := newStubMLflow(t, map[string]string{"MLmodel": mlmodelSignature})
	server := testServer()
	projectID := createTestProject(t, server)
	registered := httptest.NewRecorder()
	server.ServeHTTP(registered, newTestRequest(http.MethodPost, "/api/v1/models", strings.NewReader(`{"project_id":"`+projectID+`","name":"churn-classifier","version":"3","artifact_uri":"hf://org/model@abc","source":{"provider":"huggingface","repository":"org/model","revision":"0123456789abcdef0123456789abcdef01234567"},"metrics":{"accuracy":0.93}}`)))
	if registered.Code != http.StatusCreated {
		t.Skipf("the registry refused a Hub-sourced record through this route: %d %s", registered.Code, registered.Body.String())
	}
	modelID := strings.Split(strings.Split(registered.Body.String(), `"id":"`)[1], `"`)[0]
	response, body := getInputExample(t, server, modelID)
	if response.Code != http.StatusNotFound || body.Error != "input_example_unavailable" || len(stub.asked()) != 0 {
		t.Fatalf("unexpected answer: %d %s (MLflow asked %v)", response.Code, response.Body.String(), stub.asked())
	}
}

// Same access rules as the neighbouring model routes: read access to the
// models service, and the model's project must be assigned to the caller.
func TestInputExampleAccess(t *testing.T) {
	stub := newStubMLflow(t, map[string]string{"MLmodel": mlmodelSignature})
	server := testServer()
	modelID := registerDeployableModel(t, server)
	path := "/api/v1/models/" + modelID + "/input-example"

	for name, tc := range map[string]struct {
		principal auth.Principal
		want      bool
	}{
		"viewer reads":                  {auth.Principal{Roles: []string{"viewer"}}, true},
		"user with the models service":  {auth.Principal{Roles: []string{"user"}, Provisioned: true, Services: []string{"models"}}, true},
		"user without the service":      {auth.Principal{Roles: []string{"user"}, Provisioned: true, Services: []string{"agents"}}, false},
		"unprovisioned user":            {auth.Principal{Roles: []string{"user"}, Services: []string{"models"}}, false},
		"disabled account":              {auth.Principal{Roles: []string{"admin"}, Disabled: true}, false},
		"no role":                       {auth.Principal{}, false},
		"same rule as the predict read": {auth.Principal{Roles: []string{"engineer"}}, auth.Allowed(auth.Principal{Roles: []string{"engineer"}}, http.MethodGet, "/api/v1/models/"+modelID+"/deployment")},
	} {
		if got := auth.Allowed(tc.principal, http.MethodGet, path); got != tc.want {
			t.Errorf("%s: allowed = %v, want %v", name, got, tc.want)
		}
	}

	// A caller whose projects do not include the model's gets the same 404 as
	// an unknown model, before MLflow is contacted.
	before := len(stub.asked())
	for _, id := range []string{modelID, "mdl-does-not-exist"} {
		request := newTestRequest(http.MethodGet, "/api/v1/models/"+id+"/input-example", nil)
		request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{Subject: "mallory", Roles: []string{"user"}, Provisioned: true, Services: []string{"models"}, ProjectIDs: []string{"another-project"}}))
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `"not_found"`) {
			t.Fatalf("%s: expected 404 not_found, got %d %s", id, response.Code, response.Body.String())
		}
	}
	if len(stub.asked()) != before {
		t.Fatal("an unauthorized caller reached MLflow")
	}
}

// The gateway enforces the role check on the route itself.
func TestInputExampleRouteEnforcesTheModelsService(t *testing.T) {
	stub := newStubMLflow(t, map[string]string{"MLmodel": mlmodelSignature})
	server := testServer()
	modelID := registerDeployableModel(t, server)
	before := len(stub.asked())
	t.Setenv("KIONGA_LOCAL_ROLE", "user")
	denied := httptest.NewRecorder()
	testServer().ServeHTTP(denied, newTestRequest(http.MethodGet, "/api/v1/models/"+modelID+"/input-example", nil))
	if denied.Code != http.StatusForbidden {
		t.Fatalf("an unprovisioned user must be refused, got %d %s", denied.Code, denied.Body.String())
	}
	if len(stub.asked()) != before {
		t.Fatal("a refused caller reached MLflow")
	}
}

func TestInputExampleIsDocumented(t *testing.T) {
	if _, found := openapiPaths["/api/v1/models/{id}/input-example"]; !found {
		t.Fatal("the route is missing from the OpenAPI document")
	}
}
