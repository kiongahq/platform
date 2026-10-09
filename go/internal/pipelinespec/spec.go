// Package pipelinespec owns the versioned pipeline definition contract: the
// YAML document users write, its validation, and its canonical form.
//
// A definition reaches the control plane either as YAML (experts, Git) or as
// the JSON request the form editor builds. Both convert to the same
// api.UpsertPipelineDefinitionRequest, are validated by Validate, and are
// stored with a canonical YAML rendering whose SHA-256 identifies the
// revision a run executed. The package is pure: no I/O, no clocks.
package pipelinespec

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ml-ai-ops/platform/pkg/api"
)

const (
	APIVersion = "kionga.dev/v1"
	Kind       = "Pipeline"
)

// Document is the YAML shape. Field names are camelCase like Kubernetes
// manifests; Request/FromRequest map it to the snake_case API contract.
type Document struct {
	APIVersion string   `yaml:"apiVersion"`
	Kind       string   `yaml:"kind"`
	Metadata   Metadata `yaml:"metadata"`
	Spec       Spec     `yaml:"spec"`
}

type Metadata struct {
	Name        string `yaml:"name"`
	Project     string `yaml:"project"`
	Version     string `yaml:"version"`
	Description string `yaml:"description,omitempty"`
}

type Spec struct {
	ExecutionMode string          `yaml:"executionMode"`
	Parameters    map[string]any  `yaml:"parameters,omitempty"`
	Overridable   *DocOverridable `yaml:"overridable,omitempty"`
	Triggers      []DocTrigger    `yaml:"triggers,omitempty"`
	Nodes         []DocNode       `yaml:"nodes"`
	Source        *DocSource      `yaml:"source,omitempty"`
}

type DocSource struct {
	Repository string `yaml:"repository,omitempty"`
	Commit     string `yaml:"commit,omitempty"`
}

type DocOverridable struct {
	Parameters  []string `yaml:"parameters,omitempty"`
	Image       bool     `yaml:"image,omitempty"`
	Resources   bool     `yaml:"resources,omitempty"`
	Retries     bool     `yaml:"retries,omitempty"`
	Timeout     bool     `yaml:"timeout,omitempty"`
	Nodes       bool     `yaml:"nodes,omitempty"`
	Parallelism bool     `yaml:"parallelism,omitempty"`
}

type DocTrigger struct {
	Type     string `yaml:"type"`
	Cron     string `yaml:"cron,omitempty"`
	Timezone string `yaml:"timezone,omitempty"`
	Paused   bool   `yaml:"paused,omitempty"`
	Topic    string `yaml:"topic,omitempty"`
}

type DocNode struct {
	ID              string            `yaml:"id"`
	Type            string            `yaml:"type"`
	Description     string            `yaml:"description,omitempty"`
	Image           string            `yaml:"image,omitempty"`
	Function        string            `yaml:"function,omitempty"`
	Command         []string          `yaml:"command,omitempty"`
	DependsOn       []string          `yaml:"dependsOn,omitempty"`
	Inputs          []string          `yaml:"inputs,omitempty"`
	Outputs         []string          `yaml:"outputs,omitempty"`
	Environment     map[string]string `yaml:"env,omitempty"`
	ResourceProfile string            `yaml:"resourceProfile,omitempty"`
	Resources       *DocResources     `yaml:"resources,omitempty"`
	Retry           *DocRetry         `yaml:"retry,omitempty"`
	TimeoutSeconds  int               `yaml:"timeoutSeconds,omitempty"`
	When            *DocCondition     `yaml:"when,omitempty"`
}

type DocResources struct {
	CPU    string `yaml:"cpu,omitempty"`
	Memory string `yaml:"memory,omitempty"`
	GPU    int    `yaml:"gpu,omitempty"`
}

type DocRetry struct {
	Max            int `yaml:"max"`
	BackoffSeconds int `yaml:"backoffSeconds,omitempty"`
}

type DocCondition struct {
	Param  string `yaml:"param"`
	Equals string `yaml:"equals"`
}

// Request converts the document to the API request.
func (d Document) Request() api.UpsertPipelineDefinitionRequest {
	req := api.UpsertPipelineDefinitionRequest{
		ProjectID: d.Metadata.Project, Name: d.Metadata.Name, Version: d.Metadata.Version,
		Description: d.Metadata.Description, ExecutionMode: d.Spec.ExecutionMode, Parameters: d.Spec.Parameters,
	}
	if d.Spec.Source != nil {
		req.RepositoryURL, req.CommitSHA = d.Spec.Source.Repository, d.Spec.Source.Commit
	}
	if o := d.Spec.Overridable; o != nil {
		req.Overridable = &api.Overridable{Parameters: o.Parameters, Image: o.Image, Resources: o.Resources, Retries: o.Retries, Timeout: o.Timeout, Nodes: o.Nodes, Parallelism: o.Parallelism}
	}
	for _, t := range d.Spec.Triggers {
		req.Triggers = append(req.Triggers, api.PipelineTrigger{Type: t.Type, Cron: t.Cron, Timezone: t.Timezone, Paused: t.Paused, Topic: t.Topic})
	}
	for _, n := range d.Spec.Nodes {
		job := api.PipelineJob{
			Name: n.ID, Kind: n.Type, Description: n.Description, Image: n.Image, Function: n.Function,
			Command: n.Command, DependsOn: n.DependsOn, Inputs: n.Inputs, Outputs: n.Outputs,
			Environment: n.Environment, ResourceProfile: n.ResourceProfile, TimeoutSeconds: n.TimeoutSeconds,
		}
		if n.Resources != nil {
			job.Resources = api.JobResources{CPU: n.Resources.CPU, Memory: n.Resources.Memory, GPU: n.Resources.GPU}
		}
		if n.Retry != nil {
			job.Retries, job.RetryBackoffSeconds = n.Retry.Max, n.Retry.BackoffSeconds
		}
		if n.When != nil {
			job.When = &api.NodeCondition{Param: n.When.Param, Equals: n.When.Equals}
		}
		req.Jobs = append(req.Jobs, job)
	}
	return req
}

// FromRequest converts an API request to the document form.
func FromRequest(req api.UpsertPipelineDefinitionRequest) Document {
	doc := Document{APIVersion: APIVersion, Kind: Kind,
		Metadata: Metadata{Name: req.Name, Project: req.ProjectID, Version: req.Version, Description: req.Description},
		Spec:     Spec{ExecutionMode: req.ExecutionMode, Parameters: req.Parameters},
	}
	if req.RepositoryURL != "" || req.CommitSHA != "" {
		doc.Spec.Source = &DocSource{Repository: req.RepositoryURL, Commit: req.CommitSHA}
	}
	if o := req.Overridable; o != nil {
		doc.Spec.Overridable = &DocOverridable{Parameters: o.Parameters, Image: o.Image, Resources: o.Resources, Retries: o.Retries, Timeout: o.Timeout, Nodes: o.Nodes, Parallelism: o.Parallelism}
	}
	for _, t := range req.Triggers {
		doc.Spec.Triggers = append(doc.Spec.Triggers, DocTrigger{Type: t.Type, Cron: t.Cron, Timezone: t.Timezone, Paused: t.Paused, Topic: t.Topic})
	}
	for _, j := range req.Jobs {
		node := DocNode{ID: j.Name, Type: j.Kind, Description: j.Description, Image: j.Image, Function: j.Function,
			Command: j.Command, DependsOn: j.DependsOn, Inputs: j.Inputs, Outputs: j.Outputs, Environment: j.Environment,
			ResourceProfile: j.ResourceProfile, TimeoutSeconds: j.TimeoutSeconds}
		if j.Resources != (api.JobResources{}) {
			node.Resources = &DocResources{CPU: j.Resources.CPU, Memory: j.Resources.Memory, GPU: j.Resources.GPU}
		}
		if j.Retries != 0 || j.RetryBackoffSeconds != 0 {
			node.Retry = &DocRetry{Max: j.Retries, BackoffSeconds: j.RetryBackoffSeconds}
		}
		if j.When != nil {
			node.When = &DocCondition{Param: j.When.Param, Equals: j.When.Equals}
		}
		doc.Spec.Nodes = append(doc.Spec.Nodes, node)
	}
	return doc
}

// Render returns the canonical YAML for a request: stable field order,
// two-space indentation, sorted map keys. Comments are not preserved; the
// console warns before re-rendering hand-written YAML.
func Render(req api.UpsertPipelineDefinitionRequest) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	if err := encoder.Encode(FromRequest(req)); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// Hash identifies a revision by its YAML bytes.
func Hash(yamlText []byte) string {
	sum := sha256.Sum256(yamlText)
	return hex.EncodeToString(sum[:])
}

// Parse decodes YAML into a request. Decode errors and unknown fields are
// reported as issues with line numbers; semantic checks run in Validate.
func Parse(text []byte) (api.UpsertPipelineDefinitionRequest, Issues) {
	var root yaml.Node
	if err := yaml.Unmarshal(text, &root); err != nil {
		return api.UpsertPipelineDefinitionRequest{}, Issues{{Path: "", Message: cleanYAMLError(err)}}
	}
	if len(root.Content) == 0 {
		return api.UpsertPipelineDefinitionRequest{}, Issues{{Path: "", Message: "the document is empty"}}
	}
	var doc Document
	decoder := yaml.NewDecoder(bytes.NewReader(text))
	decoder.KnownFields(true)
	if err := decoder.Decode(&doc); err != nil {
		return api.UpsertPipelineDefinitionRequest{}, Issues{{Path: "", Line: yamlErrorLine(err), Message: cleanYAMLError(err)}}
	}
	var issues Issues
	if doc.APIVersion != APIVersion {
		issues = append(issues, Issue{Path: "apiVersion", Line: lineOf(&root, "apiVersion"), Message: fmt.Sprintf("apiVersion must be %q", APIVersion)})
	}
	if doc.Kind != Kind {
		issues = append(issues, Issue{Path: "kind", Line: lineOf(&root, "kind"), Message: fmt.Sprintf("kind must be %q", Kind)})
	}
	return doc.Request(), issues
}

// ParseAndValidate parses YAML and validates it, annotating issues with the
// line of the node they belong to when known.
func ParseAndValidate(text []byte) (api.UpsertPipelineDefinitionRequest, Issues) {
	var root yaml.Node
	_ = yaml.Unmarshal(text, &root)
	req, issues := Parse(text)
	if len(issues) > 0 {
		return req, issues
	}
	normalized, semantic := Validate(req)
	lines := nodeLineIndex(&root)
	for i := range semantic {
		if semantic[i].Line == 0 && semantic[i].Node != "" {
			semantic[i].Line = lines[semantic[i].Node]
		}
	}
	return normalized, semantic
}

func cleanYAMLError(err error) string {
	return strings.TrimPrefix(err.Error(), "yaml: ")
}

func yamlErrorLine(err error) int {
	var line int
	if _, scanErr := fmt.Sscanf(strings.TrimPrefix(err.Error(), "yaml: unmarshal errors:\n  "), "line %d", &line); scanErr == nil {
		return line
	}
	if _, scanErr := fmt.Sscanf(strings.TrimPrefix(err.Error(), "yaml: "), "line %d", &line); scanErr == nil {
		return line
	}
	return 0
}

func lineOf(root *yaml.Node, key string) int {
	if root == nil || len(root.Content) == 0 {
		return 0
	}
	mapping := root.Content[0]
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i].Line
		}
	}
	return 0
}

// nodeLineIndex maps node id → line of its "id" key under spec.nodes.
func nodeLineIndex(root *yaml.Node) map[string]int {
	lines := map[string]int{}
	if root == nil || len(root.Content) == 0 {
		return lines
	}
	find := func(mapping *yaml.Node, key string) *yaml.Node {
		if mapping == nil || mapping.Kind != yaml.MappingNode {
			return nil
		}
		for i := 0; i+1 < len(mapping.Content); i += 2 {
			if mapping.Content[i].Value == key {
				return mapping.Content[i+1]
			}
		}
		return nil
	}
	nodes := find(find(root.Content[0], "spec"), "nodes")
	if nodes == nil || nodes.Kind != yaml.SequenceNode {
		return lines
	}
	for _, item := range nodes.Content {
		if id := find(item, "id"); id != nil {
			lines[id.Value] = id.Line
		}
	}
	return lines
}
