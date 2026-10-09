package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
)

func definitionServer(t *testing.T) (http.Handler, *store.Store, string) {
	t.Helper()
	data := store.New()
	project, err := data.CreateProject(api.CreateProjectRequest{Name: "Definitions project"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	return New(data, nil), data, project.ID
}

func do(t *testing.T, handler http.Handler, method, path, body string, roles ...string) *httptest.ResponseRecorder {
	t.Helper()
	if len(roles) == 0 {
		roles = []string{auth.RoleAdmin}
	}
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{Subject: "admin", Roles: roles}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

const apiYAML = `apiVersion: kionga.dev/v1
kind: Pipeline
metadata: {name: branched, project: PROJECT, version: "1"}
spec:
  executionMode: prefect
  triggers:
    - {type: schedule, cron: "0 3 * * *", timezone: Europe/Berlin}
  nodes:
    - {id: extract, type: container, image: busybox}
    - {id: left, type: container, image: busybox, dependsOn: [extract]}
    - {id: right, type: container, image: busybox, dependsOn: [extract]}
    - {id: join, type: container, image: busybox, dependsOn: [left, right]}
`

func TestDefinitionYAMLLifecycle(t *testing.T) {
	handler, data, projectID := definitionServer(t)
	text := strings.ReplaceAll(apiYAML, "PROJECT", projectID)
	body, _ := json.Marshal(map[string]string{"yaml": text})

	validated := do(t, handler, "POST", "/api/v1/pipelines/validate", string(body))
	var check validationResponse
	_ = json.Unmarshal(validated.Body.Bytes(), &check)
	if !check.Valid || len(check.Layers) != 3 || len(check.Layers[1]) != 2 || check.SHA256 == "" {
		t.Fatalf("validate: %s", validated.Body)
	}

	created := do(t, handler, "POST", "/api/v1/pipelines/definitions/yaml", string(body))
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body)
	}
	var definition api.PipelineDefinition
	_ = json.Unmarshal(created.Body.Bytes(), &definition)
	if definition.Revision != 1 || definition.Triggers[0].NextRunAt == nil {
		t.Fatalf("definition: %+v", definition)
	}

	paused := do(t, handler, "POST", "/api/v1/pipelines/definitions/"+definition.ID+"/triggers/0/pause", "")
	definition = api.PipelineDefinition{}
	_ = json.Unmarshal(paused.Body.Bytes(), &definition)
	if paused.Code != http.StatusOK || !definition.Triggers[0].Paused || definition.Triggers[0].NextRunAt != nil || definition.Revision != 2 {
		t.Fatalf("pause: %d %+v", paused.Code, definition)
	}

	rollback := do(t, handler, "POST", "/api/v1/pipelines/definitions/"+definition.ID+"/revisions/1/rollback", "")
	definition = api.PipelineDefinition{}
	_ = json.Unmarshal(rollback.Body.Bytes(), &definition)
	if rollback.Code != http.StatusOK || definition.Revision != 3 || definition.Triggers[0].Paused || definition.Triggers[0].NextRunAt == nil {
		t.Fatalf("rollback: %d %+v", rollback.Code, definition)
	}
	revisions := store.PipelineRevisions(data, definition.ID)
	if len(revisions) != 3 || revisions[0].Message != "Roll back to revision 1" || revisions[0].SHA256 != revisions[2].SHA256 {
		t.Fatalf("revisions: %+v", revisions)
	}

	fetched := do(t, handler, "GET", "/api/v1/pipelines/definitions/"+definition.ID+"/yaml", "")
	if !strings.Contains(fetched.Body.String(), "Europe/Berlin") {
		t.Fatalf("yaml: %s", fetched.Body)
	}
}

func TestDefinitionSaveReturnsFieldIssues(t *testing.T) {
	handler, _, projectID := definitionServer(t)
	text := strings.ReplaceAll(apiYAML, "PROJECT", projectID)
	text = strings.Replace(text, "dependsOn: [left, right]", "dependsOn: [left, nowhere]", 1)
	body, _ := json.Marshal(map[string]string{"yaml": text})
	response := do(t, handler, "POST", "/api/v1/pipelines/definitions/yaml", string(body))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d", response.Code)
	}
	var payload struct {
		Details []struct {
			Node string `json:"node"`
			Line int    `json:"line"`
		} `json:"details"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &payload)
	if len(payload.Details) != 1 || payload.Details[0].Node != "join" || payload.Details[0].Line != 12 {
		t.Fatalf("details: %s", response.Body)
	}
	cyclic := `{"project_id":"` + projectID + `","name":"c","version":"1","jobs":[{"name":"a","kind":"container","image":"x","depends_on":["b"]},{"name":"b","kind":"container","image":"x","depends_on":["a"]}]}`
	response = do(t, handler, "POST", "/api/v1/pipelines/definitions", cyclic)
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "dependency cycle") {
		t.Fatalf("cycle: %d %s", response.Code, response.Body)
	}
}

func TestDefinitionsAreProjectIsolated(t *testing.T) {
	handler, data, projectID := definitionServer(t)
	text := strings.ReplaceAll(apiYAML, "PROJECT", projectID)
	body, _ := json.Marshal(map[string]string{"yaml": text})
	created := do(t, handler, "POST", "/api/v1/pipelines/definitions/yaml", string(body))
	var definition api.PipelineDefinition
	_ = json.Unmarshal(created.Body.Bytes(), &definition)
	if _, err := data.UpsertUserAccess("outsider", api.UpsertUserAccessRequest{Role: "user", Services: []string{"pipelines"}}, "admin"); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/api/v1/pipelines/definitions/"+definition.ID+"/revisions", nil)
	request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{Subject: "outsider", Roles: []string{auth.RoleUser}}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("outsider saw another project's revisions: %d", response.Code)
	}
}
