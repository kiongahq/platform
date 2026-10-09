package httpapi

import (
	"net"
	"net/http"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/internal/policy"
	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
)

// Authorization is two layers:
//
//  1. The coarse gate (auth.Allowed, run by RBAC middleware on every request
//     and again here) decides whether the caller's role and service
//     assignments open this API surface at all.
//  2. Policy evaluation decides the specific action on the specific
//     resource: the caller's role baseline (a managed policy equivalent to
//     the role, scoped to assigned projects) plus every policy attached to
//     the caller or their groups. Explicit deny wins; otherwise any allow
//     allows; otherwise default deny.
//
// With no attached policies the result equals the pre-policy behaviour.

// authorizer evaluates many actions for one principal while loading their
// groups and attached policies once.
type authorizer struct {
	store     store.Repository
	principal auth.Principal
	groups    []string
	bindings  []policy.Binding
	base      policy.Context
	tags      map[string]map[string]string
}

type contextOption func(*policy.Context)

func withProfile(profile string) contextOption {
	return func(c *policy.Context) {
		if profile != "" {
			c.ResourceProfile = profile
		}
	}
}

func (s *Server) authorizerFor(r *http.Request) *authorizer {
	return newAuthorizer(s.store, principal(r), requestContext(r))
}

func newAuthorizer(repository store.Repository, value auth.Principal, base policy.Context) *authorizer {
	a := &authorizer{store: repository, principal: value, base: base, groups: []string{}, tags: map[string]map[string]string{}}
	a.bindings = baselineBindings(repository, value)
	if value.Subject != "" && !slices.Contains(value.Roles, auth.RoleService) {
		groups, attached := store.AttachedBindings(repository, value.Subject)
		a.groups, a.bindings = groups, append(a.bindings, attached...)
	}
	if a.base.ResourceProfile == "" && value.Provisioned && !privileged(value) {
		if access, err := repository.AccessFor(value.Subject); err == nil {
			a.base.ResourceProfile = access.Compute.Profile
		}
	}
	return a
}

// projectScope returns the projects a principal is limited to, or nil when
// it may see every project.
func projectScope(repository store.Repository, value auth.Principal) []string {
	allowed := allowedProjectIDs(repository, value)
	if allowed == nil {
		return nil
	}
	scope := make([]string, 0, len(allowed))
	for id, ok := range allowed {
		if ok {
			scope = append(scope, id)
		}
	}
	sort.Strings(scope)
	return scope
}

func baselineBindings(repository store.Repository, value auth.Principal) []policy.Binding {
	subject := policy.BaselineSubject{Services: value.Services, ProjectScope: projectScope(repository, value)}
	out := []policy.Binding{}
	for _, role := range value.Roles {
		if p, ok := policy.Baseline(role, subject); ok {
			out = append(out, policy.Binding{Policy: p, Source: "role:" + role})
		}
	}
	return out
}

// check evaluates one action. Suspended principals are always denied.
func (a *authorizer) check(action, resource string, options ...contextOption) policy.Decision {
	if a.principal.Disabled {
		return policy.Decision{Action: action, Resource: resource, DecidedBy: "account-suspended", Reason: "This account is suspended.", MatchedStatements: []policy.StatementRef{}, EvaluatedPolicies: []policy.PolicyRef{}}
	}
	ctx := a.base
	ctx.ResourceTags = a.resourceTags(resource)
	for _, option := range options {
		option(&ctx)
	}
	return policy.Evaluate(a.bindings, policy.Request{Action: action, Resource: resource, Context: ctx})
}

func (a *authorizer) allowed(action, resource string, options ...contextOption) bool {
	return a.check(action, resource, options...).Allowed
}

// resourceTags derives tags for project resources from project metadata so
// resource_tags conditions work without a separate tagging API.
func (a *authorizer) resourceTags(resource string) map[string]string {
	rest, ok := strings.CutPrefix(resource, policy.ResourcePrefix+"project/")
	if !ok {
		return nil
	}
	id, _, _ := strings.Cut(rest, "/")
	if id == "" || id == "*" {
		return nil
	}
	if tags, ok := a.tags[id]; ok {
		return tags
	}
	tags := map[string]string{}
	if project, err := a.store.Project(id); err == nil {
		for key, value := range map[string]string{"template": project.Template, "framework": project.Framework, "accelerator": project.Accelerator, "profile": project.RequestedProfile, "owner": project.OwnerSubject} {
			if value != "" {
				tags[key] = value
			}
		}
	}
	a.tags[id] = tags
	return tags
}

// requestContext captures condition inputs from the request. The source
// address is the TCP peer; X-Forwarded-For is trusted only when
// KIONGA_TRUSTED_PROXY=true says a proxy in front of the gateway sets it.
func requestContext(r *http.Request) policy.Context {
	ip := r.RemoteAddr
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	if os.Getenv("KIONGA_TRUSTED_PROXY") == "true" {
		if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); forwarded != "" {
			ip = forwarded
		}
	}
	return policy.Context{SourceIP: ip, Time: time.Now().UTC()}
}

// authorize combines the coarse role/service gate with policy evaluation
// for one action on one resource.
func (s *Server) authorize(r *http.Request, action, resource string, options ...contextOption) policy.Decision {
	return s.authorizerFor(r).gate(r, action, resource, options...)
}

func (a *authorizer) gate(r *http.Request, action, resource string, options ...contextOption) policy.Decision {
	if !auth.Allowed(a.principal, r.Method, r.URL.Path) {
		return policy.Decision{Action: action, Resource: resource, DecidedBy: "coarse-gate",
			Reason:            "Your role and service assignments do not open " + r.Method + " " + r.URL.Path + ". Request access from My access.",
			MatchedStatements: []policy.StatementRef{}, EvaluatedPolicies: []policy.PolicyRef{}}
	}
	return a.check(action, resource, options...)
}

// writeDenied answers 403 with the decision that denied, so clients can
// show which statement (or the default) was responsible.
func writeDenied(w http.ResponseWriter, decision policy.Decision) {
	writeJSON(w, http.StatusForbidden, map[string]any{"error": "access_denied", "message": decision.Reason, "decision": decision})
}

// runResource names the pipeline a run belongs to.
func runResource(run api.PipelineRun) string {
	pipeline := run.DefinitionID
	if pipeline == "" {
		pipeline = run.Name
	}
	return policy.PipelineResource(run.ProjectID, pipeline)
}

func definitionResource(definition api.PipelineDefinition) string {
	return policy.PipelineResource(definition.ProjectID, definition.ID)
}

// visibleRun returns the run as the caller may see it: logs are removed
// when logs:Read is denied. ok is false when pipeline:Read is denied.
func (a *authorizer) visibleRun(run api.PipelineRun) (api.PipelineRun, bool) {
	resource := runResource(run)
	if !a.allowed(policy.PipelineRead, resource) {
		return run, false
	}
	if len(run.Logs) > 0 && !a.allowed(policy.LogsRead, resource) {
		run.Logs = nil
	}
	return run, true
}

func (a *authorizer) visibleRuns(items []api.PipelineRun) []api.PipelineRun {
	out := make([]api.PipelineRun, 0, len(items))
	for _, item := range items {
		if run, ok := a.visibleRun(item); ok {
			out = append(out, run)
		}
	}
	return out
}

func (a *authorizer) visibleDefinitions(items []api.PipelineDefinition) []api.PipelineDefinition {
	out := make([]api.PipelineDefinition, 0, len(items))
	for _, item := range items {
		if a.allowed(policy.PipelineRead, definitionResource(item)) {
			out = append(out, item)
		}
	}
	return out
}

func (a *authorizer) visibleProjects(items []api.Project) []api.Project {
	out := make([]api.Project, 0, len(items))
	for _, item := range items {
		if a.allowed(policy.ProjectRead, policy.ProjectResource(item.ID)) {
			out = append(out, item)
		}
	}
	return out
}

// overrideActions lists the extra actions a run's overrides need.
func overrideActions(overrides *api.RunOverrides) []string {
	if overrides == nil {
		return nil
	}
	out := []string{}
	if len(overrides.Parameters) > 0 {
		out = append(out, policy.PipelineOverrideParameters)
	}
	resources, image := false, false
	for _, node := range overrides.Nodes {
		resources = resources || node.Resources != nil
		image = image || node.Image != ""
	}
	if resources {
		out = append(out, policy.PipelineOverrideResources)
	}
	if image {
		out = append(out, policy.PipelineOverrideImage)
	}
	return out
}
