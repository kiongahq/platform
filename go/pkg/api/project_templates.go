package api

import (
	"errors"
	"slices"
	"strings"
)

var projectTemplates = []ProjectTemplate{
	{
		ID: "production-ml", Version: "1.0.0", Name: "Production ML system", Category: "Machine learning",
		Description: "Train, validate, track, register, serve, and monitor a production model.",
		Frameworks:  []string{"scikit-learn", "xgboost", "pytorch"}, Accelerators: []string{"cpu", "single-gpu"},
		Capabilities:     []string{"data-contracts", "training", "evaluation-gates", "mlflow", "batch-inference", "online-serving", "monitoring"},
		RequiredServices: []string{"pipelines", "models", "features", "storage", "git", "workbench"}, RecommendedProfile: "power",
	},
	{
		ID: "distributed-training", Version: "1.0.0", Name: "Distributed deep learning", Category: "Machine learning",
		Description: "Multi-GPU training with checkpoints, mixed precision, distributed launch, and reproducible evaluation.",
		Frameworks:  []string{"pytorch-ddp"}, Accelerators: []string{"single-gpu", "multi-gpu"},
		Capabilities:     []string{"distributed-training", "gpu", "checkpointing", "mixed-precision", "mlflow", "model-serving"},
		RequiredServices: []string{"pipelines", "models", "storage", "git", "workbench"}, RecommendedProfile: "gpu",
	},
	{
		ID: "production-agent", Version: "1.0.0", Name: "Production AI agent", Category: "Agentic AI",
		Description: "A stateful LangGraph agent with tools, memory, tracing, evaluation, and an HTTP runtime.",
		Frameworks:  []string{"langgraph"}, Accelerators: []string{"cpu", "single-gpu"},
		Capabilities:     []string{"agent-graph", "tools", "checkpoints", "semantic-memory", "langfuse", "golden-evals", "runtime-api"},
		RequiredServices: []string{"agents", "storage", "git", "workbench"}, RecommendedProfile: "team",
	},
	{
		ID: "fullstack-ai", Version: "1.0.0", Name: "Full-stack AI application", Category: "Application",
		Description: "Model or agent backend, governed API, event function, browser client, containers, CI, and tests.",
		Frameworks:  []string{"fastapi-ml", "fastapi-agent"}, Accelerators: []string{"cpu", "single-gpu"},
		Capabilities:     []string{"api", "web-client", "model-or-agent", "event-function", "containers", "ci"},
		RequiredServices: []string{"models", "agents", "functions", "storage", "git", "workbench", "ide"}, RecommendedProfile: "power",
	},
	{
		ID: "blank-python", Version: "1.0.0", Name: "Blank expert workspace", Category: "Foundation",
		Description: "A typed, container-ready Python package with testing, linting, CI, and platform configuration.",
		Frameworks:  []string{"python"}, Accelerators: []string{"cpu", "single-gpu", "multi-gpu"},
		Capabilities:     []string{"python-package", "containers", "ci", "tests"},
		RequiredServices: []string{"git", "workbench"}, RecommendedProfile: "starter",
	},
}

// ProjectTemplates returns independent slices so callers cannot mutate the
// canonical catalog.
func ProjectTemplates() []ProjectTemplate {
	result := make([]ProjectTemplate, len(projectTemplates))
	for index, template := range projectTemplates {
		template.Frameworks = append([]string(nil), template.Frameworks...)
		template.Accelerators = append([]string(nil), template.Accelerators...)
		template.Capabilities = append([]string(nil), template.Capabilities...)
		template.RequiredServices = append([]string(nil), template.RequiredServices...)
		result[index] = template
	}
	return result
}

func ResolveProjectTemplate(id string) (ProjectTemplate, error) {
	return ResolveProjectTemplateVersion(id, "")
}

// ResolveProjectTemplateVersion resolves an immutable catalog contract. An
// empty version selects the first (current) entry, while a supplied version
// must match exactly so old projects never silently regenerate from new code.
func ResolveProjectTemplateVersion(id, version string) (ProjectTemplate, error) {
	id = canonicalTemplateID(strings.TrimSpace(id))
	version = strings.TrimSpace(version)
	for _, template := range ProjectTemplates() {
		if template.ID == id && (version == "" || template.Version == version) {
			return template, nil
		}
	}
	return ProjectTemplate{}, errors.New("unknown project template")
}

func NormalizeProjectRequest(req *CreateProjectRequest) (ProjectTemplate, error) {
	if req.Template == "" {
		req.Template = "production-ml"
	}
	template, err := ResolveProjectTemplateVersion(req.Template, req.TemplateVersion)
	if err != nil {
		return ProjectTemplate{}, err
	}
	req.Template = template.ID
	req.TemplateVersion = template.Version
	if req.Framework == "" {
		req.Framework = template.Frameworks[0]
	}
	if !slices.Contains(template.Frameworks, req.Framework) {
		return ProjectTemplate{}, errors.New("framework is not supported by the selected template")
	}
	if req.Accelerator == "" {
		req.Accelerator = template.Accelerators[0]
	}
	if !slices.Contains(template.Accelerators, req.Accelerator) {
		return ProjectTemplate{}, errors.New("accelerator is not supported by the selected template")
	}
	if template.ID == "production-ml" && req.Framework == "scikit-learn" && req.Accelerator != "cpu" {
		return ProjectTemplate{}, errors.New("scikit-learn production projects support only the cpu accelerator")
	}
	if req.RequestedProfile == "" {
		req.RequestedProfile = template.RecommendedProfile
	}
	if !slices.Contains([]string{"starter", "team", "power", "gpu", "custom"}, req.RequestedProfile) {
		return ProjectTemplate{}, errors.New("requested_profile must be starter, team, power, gpu, or custom")
	}
	return template, nil
}

func canonicalTemplateID(value string) string {
	switch value {
	case "tabular-classification", "forecasting", "ml":
		return "production-ml"
	case "rag-agent", "agent":
		return "production-agent"
	case "fullstack":
		return "fullstack-ai"
	case "blank", "python", "api", "function":
		return "blank-python"
	default:
		return value
	}
}
