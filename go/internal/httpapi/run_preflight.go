package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/ml-ai-ops/platform/internal/auth"
	"github.com/ml-ai-ops/platform/internal/pipelinespec"
	"github.com/ml-ai-ops/platform/pkg/api"
)

func init() {
	registerRoutes(func(s *Server, mux *http.ServeMux) {
		mux.HandleFunc("POST /api/v1/pipelines/preflight", s.preflightRun)
	})
}

// digestImage requires an immutable reference: name@sha256:<64 hex>.
var digestImage = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:-]*@sha256:[a-f0-9]{64}$`)

// PreflightCheck is one rule evaluated before a manual run starts.
type PreflightCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"` // pass | fail | warn
	Message string `json:"message"`
	Node    string `json:"node,omitempty"`
	Field   string `json:"field,omitempty"`
}

// FieldPolicy explains whether one field may change for a run and why.
type FieldPolicy struct {
	Field      string `json:"field"`
	Node       string `json:"node,omitempty"`
	Default    any    `json:"default"`
	Effective  any    `json:"effective"`
	Overridden bool   `json:"overridden"`
	Locked     bool   `json:"locked"`
	Reason     string `json:"reason,omitempty"`
}

// PreflightResult is returned by POST /pipelines/preflight and used by
// submit, so the console summary and the enforced rules cannot diverge.
type PreflightResult struct {
	Allowed        bool                    `json:"allowed"`
	Checks         []PreflightCheck        `json:"checks"`
	Fields         []FieldPolicy           `json:"fields"`
	Parameters     map[string]any          `json:"parameters"`
	Nodes          []api.PipelineJob       `json:"nodes"`
	ReusedNodes    []string                `json:"reused_nodes,omitempty"`
	PolicyDecision string                  `json:"policy_decision"`
	Definition     *api.PipelineDefinition `json:"definition,omitempty"`
}

func (r *PreflightResult) add(name, status, message string) {
	r.Checks = append(r.Checks, PreflightCheck{Name: name, Status: status, Message: message})
	if status == "fail" {
		r.Allowed = false
	}
}

func allowedRegistries() []string {
	return csvEnv("KIONGA_IMAGE_REGISTRIES")
}

func csvEnv(name string) []string {
	var out []string
	for _, value := range strings.Split(os.Getenv(name), ",") {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

// preflight evaluates a submission against the definition's overridable
// fields, image rules, quotas and graph semantics. It never mutates state.
func (s *Server) preflight(principal auth.Principal, req api.SubmitPipelineRequest) PreflightResult {
	result := PreflightResult{Allowed: true, Parameters: map[string]any{}, PolicyDecision: "role-baseline:" + strings.Join(principal.Roles, ",")}
	if !projectAllowed(s.store, principal, req.ProjectID) {
		result.add("project access", "fail", "This project is not assigned to you.")
		return result
	}
	result.add("project access", "pass", "You can run pipelines in this project.")
	if err := enforceRunQuota(s.store, principal); err != nil {
		result.add("concurrent runs", "fail", err.Error())
	} else {
		result.add("concurrent runs", "pass", "Within your concurrent run quota.")
	}
	engineReady := s.prefectConfigured() || (req.DefinitionID != "" && kubernetesExecutorEnabled())
	if req.DefinitionID == "" {
		if engineReady {
			result.add("engine", "pass", "The pipeline engine is configured.")
		} else {
			result.add("engine", "fail", "No pipeline engine is configured (set PREFECT_API_URL or activate a Prefect connection on the Platform page).")
		}
		if req.Overrides != nil {
			result.add("overrides", "fail", "The built-in training pipeline does not accept overrides; define a flow to configure runs.")
		}
		for key, value := range req.Parameters {
			result.Parameters[key] = value
		}
		return result
	}
	definition, err := s.store.PipelineDefinition(req.DefinitionID)
	if err != nil || definition.ProjectID != req.ProjectID {
		result.add("definition", "fail", "The flow does not exist in this project.")
		return result
	}
	result.Definition = &definition
	overridable := api.Overridable{}
	if definition.Overridable != nil {
		overridable = *definition.Overridable
	}
	overrides := api.RunOverrides{}
	if req.Overrides != nil {
		overrides = *req.Overrides
	}

	// Parameters: defaults, then only declared-overridable run values.
	allowedParams := map[string]bool{}
	for _, name := range overridable.Parameters {
		allowedParams[name] = true
	}
	for key, value := range definition.Parameters {
		result.Parameters[key] = value
	}
	requested := map[string]any{}
	for key, value := range req.Parameters {
		requested[key] = value
	}
	for key, value := range overrides.Parameters {
		requested[key] = value
	}
	names := map[string]bool{}
	for key := range definition.Parameters {
		names[key] = true
	}
	for key := range requested {
		names[key] = true
	}
	// Flows that declare no parameters predate parameter contracts: their run
	// parameters pass through unchecked (and are reported as such).
	if len(definition.Parameters) == 0 {
		for key, value := range requested {
			result.Parameters[key] = value
		}
		if len(requested) > 0 {
			result.add("parameters", "warn", "This flow declares no parameters, so run parameters are passed through unchecked. Declare them in spec.parameters to validate and lock them.")
		}
		names = map[string]bool{}
	}
	for _, key := range sortedNames(names) {
		def, hasDefault := definition.Parameters[key]
		value, changed := requested[key]
		field := FieldPolicy{Field: "parameters." + key, Default: def, Effective: def, Locked: !allowedParams[key]}
		if field.Locked {
			field.Reason = "Not listed in spec.overridable.parameters for this flow."
		}
		if changed && fmt.Sprint(value) != fmt.Sprint(def) {
			switch {
			case !hasDefault:
				result.Checks = append(result.Checks, PreflightCheck{Name: "parameters", Status: "fail", Field: field.Field, Message: fmt.Sprintf("Parameter %q is not declared by this flow.", key)})
				result.Allowed = false
			case field.Locked:
				result.Checks = append(result.Checks, PreflightCheck{Name: "parameters", Status: "fail", Field: field.Field, Message: fmt.Sprintf("Parameter %q cannot be overridden for this flow.", key)})
				result.Allowed = false
			default:
				field.Effective, field.Overridden = value, true
				result.Parameters[key] = value
			}
		}
		result.Fields = append(result.Fields, field)
	}

	// Node overrides.
	byName := map[string]api.PipelineJob{}
	for _, job := range definition.Jobs {
		byName[job.Name] = job
	}
	fail := func(node, field, message string) {
		result.Checks = append(result.Checks, PreflightCheck{Name: "node overrides", Status: "fail", Node: node, Field: field, Message: message})
		result.Allowed = false
	}
	registries := allowedRegistries()
	for _, name := range sortedNodeNames(overrides.Nodes) {
		override := overrides.Nodes[name]
		job, ok := byName[name]
		if !ok {
			fail(name, "", fmt.Sprintf("Node %q does not exist in this flow.", name))
			continue
		}
		if override.Image != "" && override.Image != job.Image {
			switch {
			case !overridable.Image:
				fail(name, "image", "This flow does not allow image overrides.")
			case job.Kind != "container":
				fail(name, "image", "Only container nodes have images.")
			case !digestImage.MatchString(override.Image):
				fail(name, "image", "Override images must be pinned by digest (name@sha256:…), so the run is reproducible.")
			case len(registries) > 0 && !hasRegistryPrefix(override.Image, registries):
				fail(name, "image", "Image registry is not approved. Approved: "+strings.Join(registries, ", "))
			}
		}
		if override.Resources != nil && !overridable.Resources {
			fail(name, "resources", "This flow does not allow resource overrides.")
		}
		if (override.Retries != nil || override.TimeoutSeconds > 0) && !overridable.Retries && !overridable.Timeout {
			fail(name, "retries", "This flow does not allow retry or timeout overrides.")
		}
		if override.Retries != nil && (*override.Retries < 0 || *override.Retries > pipelinespec.MaxRetries) {
			fail(name, "retries", fmt.Sprintf("Retries must be between 0 and %d.", pipelinespec.MaxRetries))
		}
		if override.TimeoutSeconds > pipelinespec.MaxTimeoutSeconds {
			fail(name, "timeout_seconds", fmt.Sprintf("Timeout cannot exceed %d seconds.", pipelinespec.MaxTimeoutSeconds))
		}
		if len(override.Environment) > 0 {
			fail(name, "environment", "Environment overrides are not supported for manual runs; declare a parameter instead.")
		}
	}
	if overrides.MaxParallelism != 0 {
		switch {
		case !overridable.Parallelism:
			result.add("parallelism", "fail", "This flow does not allow parallelism overrides.")
		case overrides.MaxParallelism < 1 || overrides.MaxParallelism > 64:
			result.add("parallelism", "fail", "Parallelism must be between 1 and 64.")
		}
	}
	if overrides.Priority != "" {
		result.add("priority", "fail", "The configured executor does not support run priorities, so priority cannot be set.")
	}

	// Node selection and rerun-from-failure.
	if len(overrides.SelectedNodes) > 0 || overrides.RerunFromRunID != "" {
		if !overridable.Nodes {
			result.add("node selection", "fail", "This flow does not allow running a subset of nodes.")
		}
	}
	for _, name := range overrides.SelectedNodes {
		if _, ok := byName[name]; !ok {
			result.add("node selection", "fail", fmt.Sprintf("Selected node %q does not exist.", name))
		}
	}
	if overrides.RerunFromRunID != "" {
		previous, err := s.store.Run(overrides.RerunFromRunID)
		switch {
		case err != nil || previous.ProjectID != req.ProjectID:
			result.add("rerun from failure", "fail", "The previous run was not found in this project.")
		case previous.DefinitionID != definition.ID:
			result.add("rerun from failure", "fail", "The previous run used a different flow.")
		case previous.Status != "failed" && previous.Status != "cancelled":
			result.add("rerun from failure", "fail", "Only failed or cancelled runs can be resumed.")
		default:
			result.ReusedNodes = reusableNodes(previous)
			result.add("rerun from failure", "pass", fmt.Sprintf("%d succeeded node(s) are reused from %s; the rest run again.", len(result.ReusedNodes), previous.ID))
		}
	}

	effective := ApplyNodeOverrides(definition.Jobs, &overrides)
	result.Nodes = effective
	for _, job := range definition.Jobs {
		if override, ok := overrides.Nodes[job.Name]; ok {
			if override.Image != "" {
				result.Fields = append(result.Fields, FieldPolicy{Field: "image", Node: job.Name, Default: job.Image, Effective: override.Image, Overridden: true, Locked: !overridable.Image, Reason: lockReason(!overridable.Image, "image")})
			}
			if override.Resources != nil {
				result.Fields = append(result.Fields, FieldPolicy{Field: "resources", Node: job.Name, Default: job.Resources, Effective: *override.Resources, Overridden: true, Locked: !overridable.Resources, Reason: lockReason(!overridable.Resources, "resources")})
			}
		}
	}
	if err := enforcePipelineResources(s.store, principal, effective); err != nil {
		result.add("capacity", "fail", err.Error())
	} else {
		result.add("capacity", "pass", "The widest parallel stage fits your CPU, memory and GPU grant.")
	}
	var unpinned []string
	for _, job := range effective {
		if job.Kind == "container" && !strings.Contains(job.Image, "@sha256:") {
			unpinned = append(unpinned, job.Name)
		}
	}
	if len(unpinned) > 0 {
		result.add("image pinning", "warn", "Not pinned by digest: "+strings.Join(unpinned, ", ")+". The runner records the digest it actually pulled.")
	} else {
		result.add("image pinning", "pass", "Every container image is pinned by digest.")
	}
	switch {
	case definition.ExecutionMode == "functions" && s.openfaas() == nil:
		result.add("engine", "fail", "OpenFaaS is not configured (set OPENFAAS_URL or activate an OpenFaaS connection on the Platform page).")
	case definition.ExecutionMode != "functions" && !engineReady:
		result.add("engine", "fail", "No pipeline engine is configured (set PREFECT_API_URL or activate a Prefect connection on the Platform page).")
	default:
		result.add("engine", "pass", "An execution engine is configured for this flow.")
	}
	return result
}

func lockReason(locked bool, field string) string {
	if !locked {
		return ""
	}
	return "This flow does not allow " + field + " overrides."
}

func hasRegistryPrefix(image string, registries []string) bool {
	for _, registry := range registries {
		if strings.HasPrefix(image, strings.TrimSuffix(registry, "/")+"/") {
			return true
		}
	}
	return false
}

// reusableNodes are nodes that succeeded in the previous run; their recorded
// outputs are passed to the new run instead of re-executing them.
func reusableNodes(previous api.PipelineRun) []string {
	var out []string
	for _, step := range previous.Steps {
		if step.Status == "succeeded" {
			out = append(out, step.Name)
		}
	}
	sort.Strings(out)
	return out
}

func sortedNames(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func sortedNodeNames(nodes map[string]api.NodeOverride) []string {
	out := make([]string, 0, len(nodes))
	for name := range nodes {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (s *Server) preflightRun(w http.ResponseWriter, r *http.Request) {
	var req api.SubmitPipelineRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	result := s.preflight(principal(r), req)
	result.Definition = nil
	writeJSON(w, http.StatusOK, result)
}
