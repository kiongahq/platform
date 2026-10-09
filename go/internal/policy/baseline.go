package policy

import "slices"

// Roles mirrored from the auth package. They are repeated here so the
// evaluator stays dependency free.
const (
	RoleAdmin    = "admin"
	RoleOperator = "operator"
	RoleEngineer = "engineer"
	RoleViewer   = "viewer"
	RoleUser     = "user"
	RoleService  = "service"
)

// BaselineSubject is what a role baseline needs to know about a principal.
type BaselineSubject struct {
	// Services are the console services assigned to a normal user (or the
	// scopes of an API token). Ignored for other roles.
	Services []string
	// ProjectScope lists the projects the principal is limited to (assigned
	// plus owned). Nil means unscoped: every project.
	ProjectScope []string
}

// BaselineID returns the managed policy ID of a role baseline.
func BaselineID(role string) string { return reservedPrefix + role }

var baselineDescriptions = map[string]string{
	RoleAdmin:    "Full platform administration. Equivalent to the admin role.",
	RoleOperator: "Full platform operation, including user and policy administration. Equivalent to the operator role.",
	RoleEngineer: "Read everything in scope; build projects, pipelines and features; open workspaces. Scoped to assigned projects when the profile lists any.",
	RoleViewer:   "Read-only access to projects, pipelines, logs and features. Scoped to assigned projects when the profile lists any.",
	RoleUser:     "Normal user: only the services an administrator assigned, only on assigned and owned projects.",
	RoleService:  "In-platform machine identity reporting state back to the control plane.",
}

// BuiltIns returns the role baselines as managed policies, unscoped and with
// every service, for display. Evaluation uses Baseline, which scopes them.
func BuiltIns() []Policy {
	all := []string{"projects", "pipelines", "features", "workbench", "ide"}
	out := []Policy{}
	for _, role := range []string{RoleAdmin, RoleOperator, RoleEngineer, RoleViewer, RoleUser, RoleService} {
		p, _ := Baseline(role, BaselineSubject{Services: all})
		out = append(out, p)
	}
	return out
}

// Baseline returns the managed policy that reproduces role's behaviour for
// one principal. ok is false for an unknown role.
func Baseline(role string, subject BaselineSubject) (Policy, bool) {
	p := Policy{ID: BaselineID(role), Name: "Role baseline: " + role, Description: baselineDescriptions[role], Version: 1, Managed: true}
	scope := scopePatterns(subject.ProjectScope)
	workspaces := func(kinds ...string) []string {
		out := []string{}
		for _, kind := range kinds {
			out = append(out, WorkspaceResource("", kind))
			if subject.ProjectScope == nil {
				out = append(out, WorkspaceResource("*", kind))
				continue
			}
			for _, id := range subject.ProjectScope {
				out = append(out, WorkspaceResource(id, kind))
			}
		}
		return out
	}
	allPipeline := []string{PipelineRead, PipelineWrite, PipelineRun, PipelineOverrideParameters, PipelineOverrideResources, PipelineOverrideImage, LogsRead}
	switch role {
	case RoleAdmin, RoleOperator:
		p.Statements = []Statement{{Sid: "FullAccess", Effect: Allow, Actions: []string{"*"}, Resources: []string{"*"}}}
	case RoleService:
		p.Statements = []Statement{{Sid: "ServiceIdentity", Effect: Allow, Actions: []string{"*"}, Resources: []string{"*"}}}
	case RoleEngineer:
		p.Statements = []Statement{
			{Sid: "ReadProjects", Effect: Allow, Actions: []string{ProjectRead}, Resources: scope},
			{Sid: "CreateProjects", Effect: Allow, Actions: []string{ProjectCreate}, Resources: []string{ProjectResource("*")}},
			{Sid: "BuildPipelines", Effect: Allow, Actions: allPipeline, Resources: scope},
			{Sid: "BuildFeatures", Effect: Allow, Actions: []string{FeatureRead, FeatureWrite}, Resources: []string{FeatureResource("*")}},
			{Sid: "OpenWorkspaces", Effect: Allow, Actions: []string{WorkspaceOpen}, Resources: workspaces("workbench", "ide")},
		}
	case RoleViewer:
		p.Statements = []Statement{
			{Sid: "ReadProjects", Effect: Allow, Actions: []string{ProjectRead, PipelineRead, LogsRead}, Resources: scope},
			{Sid: "ReadFeatures", Effect: Allow, Actions: []string{FeatureRead}, Resources: []string{FeatureResource("*")}},
		}
	case RoleUser:
		p.Statements = []Statement{}
		has := func(service string) bool { return slices.Contains(subject.Services, service) }
		if has("projects") {
			p.Statements = append(p.Statements,
				Statement{Sid: "ServiceProjectsRead", Effect: Allow, Actions: []string{ProjectRead}, Resources: scope},
				Statement{Sid: "ServiceProjectsCreate", Effect: Allow, Actions: []string{ProjectCreate}, Resources: []string{ProjectResource("*")}})
		}
		if has("pipelines") {
			p.Statements = append(p.Statements, Statement{Sid: "ServicePipelines", Effect: Allow, Actions: allPipeline, Resources: scope})
		}
		if has("features") {
			p.Statements = append(p.Statements, Statement{Sid: "ServiceFeatures", Effect: Allow, Actions: []string{FeatureRead, FeatureWrite}, Resources: []string{FeatureResource("*")}})
		}
		for _, kind := range []string{"workbench", "ide"} {
			if has(kind) {
				p.Statements = append(p.Statements, Statement{Sid: "ServiceWorkspace" + map[string]string{"workbench": "Workbench", "ide": "IDE"}[kind], Effect: Allow, Actions: []string{WorkspaceOpen}, Resources: workspaces(kind)})
			}
		}
	default:
		return Policy{}, false
	}
	return p, true
}

// scopePatterns turns a project scope into resource patterns covering each
// project and everything inside it. Nil scope covers every project; an
// empty, non-nil scope covers nothing (the statement then never matches).
func scopePatterns(scope []string) []string {
	if scope == nil {
		return []string{ProjectResource("*")}
	}
	out := []string{}
	for _, id := range scope {
		out = append(out, ProjectResource(id), ProjectResource(id)+"/*")
	}
	return out
}
