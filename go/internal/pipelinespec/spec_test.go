package pipelinespec

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kiongahq/platform/pkg/api"
)

const branchedYAML = `apiVersion: kionga.dev/v1
kind: Pipeline
metadata:
  name: churn
  project: prj-1
  version: "3"
spec:
  executionMode: prefect
  parameters:
    window: daily
  overridable:
    parameters: [window]
    resources: true
  triggers:
    - type: schedule
      cron: "30 2 * * *"
      timezone: Africa/Nairobi
  nodes:
    - id: extract
      type: container
      image: python:3.11-slim
    - id: features-a
      type: container
      image: python:3.11-slim
      dependsOn: [extract]
    - id: features-b
      type: container
      image: python:3.11-slim
      dependsOn: [extract]
      when: {param: window, equals: daily}
    - id: train
      type: container
      image: python:3.11-slim
      dependsOn: [features-a, features-b]
      timeoutSeconds: 600
      retry: {max: 2, backoffSeconds: 30}
`

func TestParseValidateRenderRoundTrip(t *testing.T) {
	req, issues := ParseAndValidate([]byte(branchedYAML))
	if len(issues) > 0 {
		t.Fatalf("valid document rejected: %+v", issues)
	}
	if len(req.Jobs) != 4 || req.Jobs[3].TimeoutSeconds != 600 || req.Jobs[3].Retries != 2 || req.Jobs[2].When.Equals != "daily" {
		t.Fatalf("decoded request: %+v", req.Jobs)
	}
	rendered, err := Render(req)
	if err != nil {
		t.Fatal(err)
	}
	again, issues := ParseAndValidate(rendered)
	if len(issues) > 0 {
		t.Fatalf("rendered YAML invalid: %+v\n%s", issues, rendered)
	}
	second, _ := Render(again)
	if string(second) != string(rendered) {
		t.Fatalf("canonical rendering is not stable:\n%s\n---\n%s", rendered, second)
	}
	h1, _, _ := SpecHash(req)
	h2, _, _ := SpecHash(again)
	if h1 != h2 || len(h1) != 64 {
		t.Fatalf("hash unstable: %s %s", h1, h2)
	}
	layers := Layers(req.Jobs)
	if len(layers) != 3 || len(layers[1]) != 2 {
		t.Fatalf("fan-out/fan-in layers: %v", layers)
	}
}

func TestValidationReportsEveryIssueWithNodeAndLine(t *testing.T) {
	text := strings.NewReplacer(
		`dependsOn: [features-a, features-b]`, `dependsOn: [features-a, missing]`,
		`cron: "30 2 * * *"`, `cron: "99 * * * *"`,
		`timeoutSeconds: 600`, `timeoutSeconds: 999999`,
	).Replace(branchedYAML)
	_, issues := ParseAndValidate([]byte(text))
	want := map[string]bool{"missing": false, "cron": false, "timeout": false}
	for _, issue := range issues {
		switch {
		case strings.Contains(issue.Message, `"missing", which does not exist`):
			want["missing"] = issue.Node == "train" && issue.Line > 0
		case strings.Contains(issue.Message, "cron"):
			want["cron"] = issue.Path == "spec.triggers[0].cron"
		case strings.Contains(issue.Message, "timeout"):
			want["timeout"] = issue.Node == "train"
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("issue %s not reported correctly: %+v", name, issues)
		}
	}
}

func TestCycleIsReportedAsAPath(t *testing.T) {
	jobs := []api.PipelineJob{
		{Name: "a", Kind: "container", Image: "x", DependsOn: []string{"c"}},
		{Name: "b", Kind: "container", Image: "x", DependsOn: []string{"a"}},
		{Name: "c", Kind: "container", Image: "x", DependsOn: []string{"b"}},
		{Name: "d", Kind: "container", Image: "x"},
	}
	_, issues := Validate(api.UpsertPipelineDefinitionRequest{ProjectID: "p", Name: "n", Version: "1", Jobs: jobs})
	if len(issues) != 1 || !strings.Contains(issues[0].Message, "dependency cycle: ") {
		t.Fatalf("issues: %+v", issues)
	}
	path := strings.TrimPrefix(issues[0].Message, "dependency cycle: ")
	parts := strings.Split(path, " → ")
	if len(parts) != 4 || parts[0] != parts[3] {
		t.Fatalf("cycle path %q", path)
	}
	// Every consecutive pair must be a real dependency edge (parent → child).
	deps := map[string][]string{"a": {"c"}, "b": {"a"}, "c": {"b"}}
	for i := 0; i < 3; i++ {
		parent, child := parts[i], parts[i+1]
		found := false
		for _, d := range deps[child] {
			found = found || d == parent
		}
		if !found {
			t.Fatalf("%s → %s is not an edge in %q", parent, child, path)
		}
	}
}

func TestUnknownFieldsAndWrongKindAreRejected(t *testing.T) {
	_, issues := Parse([]byte(strings.Replace(branchedYAML, "dependsOn: [extract]\n    - id: features-b", "dependson: [extract]\n    - id: features-b", 1)))
	if len(issues) == 0 || !strings.Contains(issues[0].Message, "dependson") || issues[0].Line == 0 {
		t.Fatalf("unknown field: %+v", issues)
	}
	_, issues = Parse([]byte(strings.Replace(branchedYAML, "kind: Pipeline", "kind: Job", 1)))
	if len(issues) == 0 || issues[0].Path != "kind" {
		t.Fatalf("wrong kind: %+v", issues)
	}
	_, issues = Parse([]byte("   "))
	if len(issues) != 1 {
		t.Fatalf("empty: %+v", issues)
	}
}

func TestModeAndOverridableConsistency(t *testing.T) {
	_, issues := Validate(api.UpsertPipelineDefinitionRequest{
		ProjectID: "p", Name: "n", Version: "1", ExecutionMode: "functions",
		Overridable: &api.Overridable{Parameters: []string{"undeclared"}},
		Jobs:        []api.PipelineJob{{Name: "a", Kind: "container", Image: "x"}},
	})
	text := issues.Error()
	if !strings.Contains(text, "executionMode is functions") || !strings.Contains(issues[len(issues)-1].Message, "undeclared") {
		t.Fatalf("issues: %+v", issues)
	}
}

func TestNextRunHonorsTimezone(t *testing.T) {
	from := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	next, err := NextRun("30 2 * * *", "Africa/Nairobi", from)
	if err != nil {
		t.Fatal(err)
	}
	// 02:30 in Nairobi (UTC+3) is 23:30 UTC the previous day; after
	// midnight UTC the next slot is 23:30 UTC on Oct 9.
	if want := time.Date(2026, 10, 9, 23, 30, 0, 0, time.UTC); !next.Equal(want) {
		t.Fatalf("next %s, want %s", next, want)
	}
	if _, err := NextRun("@hourly", "UTC", from); err == nil {
		t.Fatal("descriptor accepted")
	}
	if _, err := NextRun("* * * * *", "Mars/Olympus", from); err == nil {
		t.Fatal("bad timezone accepted")
	}
}

// The shared example also validates against contracts/pipeline/v1.schema.json
// (python/tests/test_pipeline_schema.py), keeping the two in step.
func TestSharedExampleIsValid(t *testing.T) {
	text, err := os.ReadFile("../../../contracts/pipeline/examples/branched.kionga.yaml")
	if err != nil {
		t.Fatal(err)
	}
	req, issues := ParseAndValidate(text)
	if len(issues) > 0 {
		t.Fatalf("example rejected: %+v", issues)
	}
	if len(req.Jobs) != 4 || len(Layers(req.Jobs)) != 3 {
		t.Fatalf("example graph: %d nodes", len(req.Jobs))
	}
}
