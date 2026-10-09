package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ml-ai-ops/platform/internal/auth"
	"github.com/ml-ai-ops/platform/internal/policy"
	"github.com/ml-ai-ops/platform/internal/store"
)

func init() {
	summary := func(text string) map[string]any { return map[string]any{"summary": text} }
	for path, item := range map[string]map[string]any{
		"/api/v1/iam/catalog":                                 {"get": summary("Action catalog, resource formats, condition keys and profiles")},
		"/api/v1/iam/explain":                                 {"get": summary("Explain whether a principal may perform an action on a resource (self, or anyone for administrators)")},
		"/api/v1/iam/effective":                               {"get": summary("Every policy that applies to a principal and, per action, the resources allowed and denied")},
		"/api/v1/admin/iam/groups":                            {"get": summary("List groups"), "post": summary("Create a group")},
		"/api/v1/admin/iam/groups/{id}":                       {"get": summary("Read a group"), "put": summary("Replace a group's name, description and members"), "delete": summary("Delete a group without attachments")},
		"/api/v1/admin/iam/policies":                          {"get": summary("List built-in role baselines and custom policies"), "post": summary("Create a policy (version 1)")},
		"/api/v1/admin/iam/policies/{id}":                     {"get": summary("Read a policy"), "put": summary("Update a policy; bumps its version and records an immutable revision"), "delete": summary("Delete a detached policy")},
		"/api/v1/admin/iam/policies/{id}/revisions":           {"get": summary("List a policy's immutable revisions")},
		"/api/v1/admin/iam/policies/{id}/revisions/{version}": {"get": summary("Read one policy revision")},
		"/api/v1/admin/iam/attachments":                       {"get": summary("List policy attachments"), "post": summary("Attach a policy to a user or group (anti-escalation enforced)")},
		"/api/v1/admin/iam/attachments/{id}":                  {"delete": summary("Detach a policy")},
	} {
		openapiPaths[path] = item
	}
	registerRoutes(func(s *Server, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/iam/catalog", s.iamCatalog)
		mux.HandleFunc("GET /api/v1/iam/explain", s.iamExplain)
		mux.HandleFunc("GET /api/v1/iam/effective", s.iamEffective)
		mux.HandleFunc("GET /api/v1/admin/iam/groups", s.iamGroups)
		mux.HandleFunc("POST /api/v1/admin/iam/groups", s.iamSaveGroup)
		mux.HandleFunc("GET /api/v1/admin/iam/groups/{id}", s.iamGroup)
		mux.HandleFunc("PUT /api/v1/admin/iam/groups/{id}", s.iamSaveGroup)
		mux.HandleFunc("DELETE /api/v1/admin/iam/groups/{id}", s.iamDeleteGroup)
		mux.HandleFunc("GET /api/v1/admin/iam/policies", s.iamPolicies)
		mux.HandleFunc("POST /api/v1/admin/iam/policies", s.iamSavePolicy)
		mux.HandleFunc("GET /api/v1/admin/iam/policies/{id}", s.iamPolicy)
		mux.HandleFunc("PUT /api/v1/admin/iam/policies/{id}", s.iamSavePolicy)
		mux.HandleFunc("DELETE /api/v1/admin/iam/policies/{id}", s.iamDeletePolicy)
		mux.HandleFunc("GET /api/v1/admin/iam/policies/{id}/revisions", s.iamPolicyRevisions)
		mux.HandleFunc("GET /api/v1/admin/iam/policies/{id}/revisions/{version}", s.iamPolicyRevision)
		mux.HandleFunc("GET /api/v1/admin/iam/attachments", s.iamAttachments)
		mux.HandleFunc("POST /api/v1/admin/iam/attachments", s.iamAttach)
		mux.HandleFunc("DELETE /api/v1/admin/iam/attachments/{id}", s.iamDetach)
	})
}

func writeFieldIssues(w http.ResponseWriter, issues policy.Issues) {
	writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "validation_error", "message": issues.Error(), "details": issues})
}

func profileNames() []string {
	out := []string{}
	for _, profile := range store.ResourceProfiles() {
		out = append(out, profile.Name)
	}
	return out
}

// ---- catalog, explain, effective --------------------------------------------

func (s *Server) iamCatalog(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"actions":    policy.Catalog(),
		"namespaces": policy.Namespaces(),
		"resources": []map[string]string{
			{"pattern": "kionga:project/<project>", "example": policy.ProjectResource("prj-demo")},
			{"pattern": "kionga:project/<project>/pipeline/<pipeline>", "example": policy.PipelineResource("prj-demo", "train")},
			{"pattern": "kionga:project/<project>/workspace/<kind>", "example": policy.WorkspaceResource("prj-demo", "workbench")},
			{"pattern": "kionga:workspace/<kind>", "example": policy.WorkspaceResource("", "ide")},
			{"pattern": "kionga:feature/<name>", "example": policy.FeatureResource("customer-features")},
			{"pattern": "kionga:user/<subject>", "example": policy.UserResource("alice")},
			{"pattern": "kionga:group/<id>", "example": policy.GroupResource("ml-team")},
			{"pattern": "kionga:policy/<id>", "example": policy.PolicyResource("read-only")},
			{"pattern": "kionga:blog/<id>", "example": policy.BlogResource("post-1")},
			{"pattern": "kionga:connection/<id>", "example": policy.ConnectionResource("conn-1")},
		},
		"conditions": []map[string]string{
			{"key": "ip_cidr", "description": "Source address must be in one of these networks."},
			{"key": "utc_hours", "description": "{start, end}: UTC hour window, end exclusive; start > end wraps midnight."},
			{"key": "resource_tags", "description": "Project tags must all match: template, framework, accelerator, profile, owner."},
			{"key": "resource_profile", "description": "request.resource_profile must be one of these profiles."},
		},
		"profiles": profileNames(),
	})
}

// principalForSubject reconstructs another subject's principal from their
// access profile. Roles that come only from an identity provider's token
// are not visible here; the bootstrap local administrator is.
func (s *Server) principalForSubject(subject string) (auth.Principal, string) {
	if access, err := s.store.AccessFor(subject); err == nil {
		return auth.Principal{Subject: subject, Email: access.Email, Roles: []string{access.Role}, Services: access.Services, ProjectIDs: access.ProjectIDs, Disabled: access.Disabled, Provisioned: true}, "profile"
	}
	if localAccountsEnabled() && subject == bootstrapUsername() {
		role := os.Getenv("MLAIOPS_LOCAL_ROLE")
		if role == "" {
			role = auth.RoleAdmin
		}
		return auth.Principal{Subject: subject, Roles: []string{role}}, "bootstrap"
	}
	return auth.Principal{Subject: subject, Roles: []string{auth.RoleUser}}, "unprovisioned"
}

func bootstrapUsername() string {
	if name := os.Getenv("MLAIOPS_LOCAL_USERNAME"); name != "" {
		return name
	}
	return "admin"
}

// subjectAuthorizer resolves the principal an IAM read asks about. Callers
// may always ask about themselves; asking about anyone else needs
// policy:Read on that user.
func (s *Server) subjectAuthorizer(w http.ResponseWriter, r *http.Request, base policy.Context) (*authorizer, string, bool) {
	caller := principal(r)
	target := strings.TrimSpace(r.URL.Query().Get("principal"))
	if target == "" || target == caller.Subject {
		return newAuthorizer(s.store, caller, base), "session", true
	}
	if decision := s.authorizerFor(r).check(policy.PolicyRead, policy.UserResource(target)); !decision.Allowed {
		decision.Reason = "Only administrators can explain another principal's access. " + decision.Reason
		writeDenied(w, decision)
		return nil, "", false
	}
	value, source := s.principalForSubject(target)
	return newAuthorizer(s.store, value, base), source, true
}

func principalSummary(a *authorizer, source string) map[string]any {
	p := a.principal
	return map[string]any{
		"subject": p.Subject, "roles": p.Roles, "groups": a.groups, "services": p.Services,
		"project_scope": projectScope(a.store, p), "disabled": p.Disabled, "provisioned": p.Provisioned, "source": source,
	}
}

// iamExplain answers "may principal do action on resource, and why?". The
// optional ip, at (RFC 3339) and resource_profile parameters simulate the
// request context; they default to the caller's real context.
func (s *Server) iamExplain(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	var issues policy.Issues
	action, known := policy.CanonicalAction(query.Get("action"))
	if !known {
		issues = append(issues, policy.Issue{Field: "action", Message: fmt.Sprintf("%q is not a catalog action; see GET /api/v1/iam/catalog", query.Get("action"))})
	}
	resource := strings.TrimSpace(query.Get("resource"))
	if resource == "" {
		issues = append(issues, policy.Issue{Field: "resource", Message: "resource is required, for example " + policy.ProjectResource("prj-demo")})
	} else if err := policy.ValidResource(resource); err != nil {
		issues = append(issues, policy.Issue{Field: "resource", Message: err.Error()})
	}
	ctx := requestContext(r)
	if ip := strings.TrimSpace(query.Get("ip")); ip != "" {
		ctx.SourceIP = ip
	}
	if at := strings.TrimSpace(query.Get("at")); at != "" {
		parsed, err := time.Parse(time.RFC3339, at)
		if err != nil {
			issues = append(issues, policy.Issue{Field: "at", Message: "must be an RFC 3339 time such as 2026-01-02T15:04:05Z"})
		}
		ctx.Time = parsed.UTC()
	}
	if profile := strings.TrimSpace(query.Get("resource_profile")); profile != "" {
		if !slices.Contains(profileNames(), profile) {
			issues = append(issues, policy.Issue{Field: "resource_profile", Message: "unknown resource profile; use one of " + strings.Join(profileNames(), ", ")})
		}
		ctx.ResourceProfile = profile
	}
	if len(issues) > 0 {
		writeFieldIssues(w, issues)
		return
	}
	a, source, ok := s.subjectAuthorizer(w, r, ctx)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"principal": principalSummary(a, source), "decision": a.check(action, resource)})
}

// iamEffective lists every policy that applies to a principal and, per
// catalog action, the resource patterns allowed and denied.
func (s *Server) iamEffective(w http.ResponseWriter, r *http.Request) {
	a, source, ok := s.subjectAuthorizer(w, r, requestContext(r))
	if !ok {
		return
	}
	type grant struct {
		Pattern     string `json:"pattern"`
		Statement   string `json:"statement"`
		Source      string `json:"source"`
		Conditional bool   `json:"conditional,omitempty"`
	}
	type actionSummary struct {
		Action string  `json:"action"`
		Allow  []grant `json:"allow"`
		Deny   []grant `json:"deny"`
	}
	actions := []actionSummary{}
	for _, info := range policy.Catalog() {
		summary := actionSummary{Action: info.Action, Allow: []grant{}, Deny: []grant{}}
		for _, binding := range a.bindings {
			for i, statement := range binding.Policy.Statements {
				if !slices.ContainsFunc(statement.Actions, func(pattern string) bool { return policy.MatchAction(pattern, info.Action) }) {
					continue
				}
				sid := statement.Sid
				if sid == "" {
					sid = "Statement" + strconv.Itoa(i+1)
				}
				for _, resource := range statement.Resources {
					item := grant{Pattern: resource, Statement: binding.Policy.ID + "#" + sid, Source: binding.Source, Conditional: statement.Conditions != nil}
					if strings.EqualFold(string(statement.Effect), string(policy.Deny)) {
						summary.Deny = append(summary.Deny, item)
					} else {
						summary.Allow = append(summary.Allow, item)
					}
				}
			}
		}
		actions = append(actions, summary)
	}
	writeJSON(w, http.StatusOK, map[string]any{"principal": principalSummary(a, source), "policies": a.bindings, "actions": actions})
}

// ---- groups -------------------------------------------------------------------

type groupRequest struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Members     []string `json:"members"`
}

func (s *Server) iamGroups(w http.ResponseWriter, r *http.Request) {
	if decision := s.authorize(r, policy.PolicyRead, policy.GroupResource("*")); !decision.Allowed {
		writeDenied(w, decision)
		return
	}
	items := store.Groups(s.store)
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

func (s *Server) iamGroup(w http.ResponseWriter, r *http.Request) {
	if decision := s.authorize(r, policy.PolicyRead, policy.GroupResource(r.PathValue("id"))); !decision.Allowed {
		writeDenied(w, decision)
		return
	}
	group, err := store.GetDoc[store.Group](s.store, store.GroupKind, r.PathValue("id"))
	writeMutation(w, group, err, http.StatusOK)
}

func (s *Server) iamSaveGroup(w http.ResponseWriter, r *http.Request) {
	var req groupRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	create := r.PathValue("id") == ""
	id := r.PathValue("id")
	if create {
		id = strings.TrimSpace(req.ID)
		if id == "" {
			id = policy.Slug(req.Name)
		}
	}
	var issues policy.Issues
	if !policy.ValidID(id) {
		issues = append(issues, policy.Issue{Field: "id", Message: "use 2-63 lowercase letters, digits or hyphens"})
	}
	req.Name, req.Description = strings.TrimSpace(req.Name), strings.TrimSpace(req.Description)
	if len(req.Name) < 2 || len(req.Name) > 80 {
		issues = append(issues, policy.Issue{Field: "name", Message: "must be 2-80 characters"})
	}
	if len(req.Description) > 500 {
		issues = append(issues, policy.Issue{Field: "description", Message: "must be at most 500 characters"})
	}
	if len(req.Members) > 500 {
		issues = append(issues, policy.Issue{Field: "members", Message: "at most 500 members are allowed"})
	}
	for i, member := range req.Members {
		if strings.TrimSpace(member) == "" || len(member) > 256 {
			issues = append(issues, policy.Issue{Field: fmt.Sprintf("members[%d]", i), Message: "must be a non-empty subject of at most 256 characters"})
		}
	}
	if len(issues) > 0 {
		writeFieldIssues(w, issues)
		return
	}
	caller := s.authorizerFor(r)
	if decision := caller.gate(r, policy.PolicyWrite, policy.GroupResource(id)); !decision.Allowed {
		writeDenied(w, decision)
		return
	}
	// Adding a member hands them every policy attached to the group, so the
	// actor must already hold what those policies grant.
	if !create {
		if escalations, decision := s.escalations(caller, s.groupPolicies(id)); len(escalations) > 0 {
			writeEscalation(w, escalations, decision)
			return
		}
	}
	group, err := store.SaveGroup(s.store, store.Group{ID: id, Name: req.Name, Description: req.Description, Members: req.Members}, create, actor(r))
	status := http.StatusOK
	if create {
		status = http.StatusCreated
	}
	writeMutation(w, group, err, status)
}

func (s *Server) iamDeleteGroup(w http.ResponseWriter, r *http.Request) {
	if decision := s.authorize(r, policy.PolicyWrite, policy.GroupResource(r.PathValue("id"))); !decision.Allowed {
		writeDenied(w, decision)
		return
	}
	if err := store.DeleteGroup(s.store, r.PathValue("id"), actor(r)); err != nil {
		writeMutation(w, struct{}{}, err, http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) groupPolicies(groupID string) []policy.Policy {
	out := []policy.Policy{}
	for _, attachment := range store.AttachmentsFor(s.store, store.PrincipalGroup, groupID) {
		if p, err := store.GetDoc[policy.Policy](s.store, store.PolicyKind, attachment.PolicyID); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// ---- policies -------------------------------------------------------------------

type policyRequest struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Statements  []policy.Statement `json:"statements"`
	// Version, when set on update, must equal the stored version.
	Version int `json:"version"`
}

func (s *Server) iamPolicies(w http.ResponseWriter, r *http.Request) {
	if decision := s.authorize(r, policy.PolicyRead, policy.PolicyResource("*")); !decision.Allowed {
		writeDenied(w, decision)
		return
	}
	items := append(policy.BuiltIns(), store.Policies(s.store)...)
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

func builtIn(id string) (policy.Policy, bool) {
	for _, p := range policy.BuiltIns() {
		if p.ID == id {
			return p, true
		}
	}
	return policy.Policy{}, false
}

func (s *Server) iamPolicy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if decision := s.authorize(r, policy.PolicyRead, policy.PolicyResource(id)); !decision.Allowed {
		writeDenied(w, decision)
		return
	}
	if p, ok := builtIn(id); ok {
		writeJSON(w, http.StatusOK, p)
		return
	}
	p, err := store.GetDoc[policy.Policy](s.store, store.PolicyKind, id)
	writeMutation(w, p, err, http.StatusOK)
}

func (s *Server) iamSavePolicy(w http.ResponseWriter, r *http.Request) {
	var req policyRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	create := r.PathValue("id") == ""
	id := r.PathValue("id")
	if create {
		id = strings.TrimSpace(req.ID)
		if id == "" {
			id = policy.Slug(req.Name)
		}
	}
	p := policy.Policy{ID: id, Name: req.Name, Description: req.Description, Statements: req.Statements}
	issues := policy.Normalize(&p, profileNames())
	switch {
	case policy.ReservedID(id):
		issues = append(policy.Issues{{Field: "id", Message: "the kionga- prefix is reserved for built-in role baselines, which cannot be changed"}}, issues...)
	case !policy.ValidID(id):
		issues = append(policy.Issues{{Field: "id", Message: "use 2-63 lowercase letters, digits or hyphens"}}, issues...)
	}
	if len(issues) > 0 {
		writeFieldIssues(w, issues)
		return
	}
	caller := s.authorizerFor(r)
	if decision := caller.gate(r, policy.PolicyWrite, policy.PolicyResource(id)); !decision.Allowed {
		writeDenied(w, decision)
		return
	}
	if create {
		saved, err := store.CreatePolicy(s.store, p, actor(r))
		writeMutation(w, saved, err, http.StatusCreated)
		return
	}
	// Editing an attached policy changes what its principals hold, so it is
	// held to the same anti-escalation rule as attaching it.
	if len(store.AttachmentsForPolicy(s.store, id)) > 0 {
		if escalations, decision := s.escalations(caller, []policy.Policy{p}); len(escalations) > 0 {
			writeEscalation(w, escalations, decision)
			return
		}
	}
	saved, err := store.UpdatePolicy(s.store, id, p, req.Version, actor(r))
	writeMutation(w, saved, err, http.StatusOK)
}

func (s *Server) iamDeletePolicy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if policy.ReservedID(id) {
		writeError(w, http.StatusConflict, "managed_policy", "Built-in role baselines cannot be deleted.")
		return
	}
	if decision := s.authorize(r, policy.PolicyWrite, policy.PolicyResource(id)); !decision.Allowed {
		writeDenied(w, decision)
		return
	}
	if err := store.DeletePolicy(s.store, id, actor(r)); err != nil {
		writeMutation(w, struct{}{}, err, http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) iamPolicyRevisions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if decision := s.authorize(r, policy.PolicyRead, policy.PolicyResource(id)); !decision.Allowed {
		writeDenied(w, decision)
		return
	}
	items := store.PolicyRevisions(s.store, id)
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

func (s *Server) iamPolicyRevision(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if decision := s.authorize(r, policy.PolicyRead, policy.PolicyResource(id)); !decision.Allowed {
		writeDenied(w, decision)
		return
	}
	version, err := strconv.Atoi(r.PathValue("version"))
	if err != nil || version < 1 {
		writeFieldIssues(w, policy.Issues{{Field: "version", Message: "must be a positive integer"}})
		return
	}
	revision, err := store.GetPolicyRevision(s.store, id, version)
	writeMutation(w, revision, err, http.StatusOK)
}

// ---- attachments -------------------------------------------------------------------

type attachRequest struct {
	PolicyID      string `json:"policy_id"`
	PrincipalType string `json:"principal_type"`
	PrincipalID   string `json:"principal_id"`
}

func (s *Server) iamAttachments(w http.ResponseWriter, r *http.Request) {
	if decision := s.authorize(r, policy.PolicyRead, policy.PolicyResource("*")); !decision.Allowed {
		writeDenied(w, decision)
		return
	}
	query := r.URL.Query()
	items := []store.PolicyAttachment{}
	for _, item := range store.Attachments(s.store) {
		if (query.Get("policy_id") == "" || item.PolicyID == query.Get("policy_id")) &&
			(query.Get("principal_type") == "" || item.PrincipalType == query.Get("principal_type")) &&
			(query.Get("principal_id") == "" || item.PrincipalID == query.Get("principal_id")) {
			items = append(items, item)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

func (s *Server) iamAttach(w http.ResponseWriter, r *http.Request) {
	var req attachRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	req.PolicyID, req.PrincipalID = strings.TrimSpace(req.PolicyID), strings.TrimSpace(req.PrincipalID)
	var issues policy.Issues
	var target policy.Policy
	if _, managed := builtIn(req.PolicyID); managed {
		issues = append(issues, policy.Issue{Field: "policy_id", Message: "built-in role baselines apply through roles; change the user's role instead"})
	} else if p, err := store.GetDoc[policy.Policy](s.store, store.PolicyKind, req.PolicyID); err != nil {
		issues = append(issues, policy.Issue{Field: "policy_id", Message: "policy not found"})
	} else {
		target = p
	}
	switch req.PrincipalType {
	case store.PrincipalUser:
		if _, err := s.store.AccessFor(req.PrincipalID); err != nil {
			issues = append(issues, policy.Issue{Field: "principal_id", Message: "no provisioned user has this subject; provision the user first"})
		}
	case store.PrincipalGroup:
		if _, err := store.GetDoc[store.Group](s.store, store.GroupKind, req.PrincipalID); err != nil {
			issues = append(issues, policy.Issue{Field: "principal_id", Message: "group not found"})
		}
	default:
		issues = append(issues, policy.Issue{Field: "principal_type", Message: "must be user or group"})
	}
	if len(issues) > 0 {
		writeFieldIssues(w, issues)
		return
	}
	caller := s.authorizerFor(r)
	if decision := caller.gate(r, policy.PolicyAttach, policy.PolicyResource(target.ID)); !decision.Allowed {
		writeDenied(w, decision)
		return
	}
	if escalations, decision := s.escalations(caller, []policy.Policy{target}); len(escalations) > 0 {
		writeEscalation(w, escalations, decision)
		return
	}
	// Self-lockout guard: a deny attached to yourself must leave you able
	// to detach it again.
	appliesToActor := (req.PrincipalType == store.PrincipalUser && req.PrincipalID == caller.principal.Subject) ||
		(req.PrincipalType == store.PrincipalGroup && slices.Contains(caller.groups, req.PrincipalID))
	if appliesToActor {
		after := *caller
		after.bindings = append(slices.Clone(caller.bindings), policy.Binding{Policy: target, Source: req.PrincipalType + ":" + req.PrincipalID})
		if decision := after.check(policy.PolicyAttach, policy.PolicyResource(target.ID)); !decision.Allowed {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "self_lockout", "message": "Attaching this policy would deny you policy:Attach on it, so you could never detach it. Ask another administrator.", "decision": decision})
			return
		}
	}
	attachment, err := store.Attach(s.store, target.ID, req.PrincipalType, req.PrincipalID, actor(r))
	writeMutation(w, attachment, err, http.StatusCreated)
}

func (s *Server) iamDetach(w http.ResponseWriter, r *http.Request) {
	attachment, err := store.GetDoc[store.PolicyAttachment](s.store, store.PolicyAttachmentKind, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "attachment not found")
		return
	}
	if decision := s.authorize(r, policy.PolicyAttach, policy.PolicyResource(attachment.PolicyID)); !decision.Allowed {
		writeDenied(w, decision)
		return
	}
	if err := store.Detach(s.store, attachment.ID, actor(r)); err != nil {
		writeMutation(w, struct{}{}, err, http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// escalations lists every (action, resource) the policies grant that the
// actor does not already hold. Wildcard actions are expanded through the
// catalog and conditions ignored, so the check is conservative.
func (s *Server) escalations(holder *authorizer, policies []policy.Policy) (policy.Issues, policy.Decision) {
	var issues policy.Issues
	var first policy.Decision
	for _, p := range policies {
		for _, grant := range policy.Grants(p) {
			decision := holder.check(grant.Action, grant.Resource)
			if decision.Allowed {
				continue
			}
			if len(issues) == 0 {
				first = decision
			}
			issues = append(issues, policy.Issue{Field: "policy " + p.ID, Message: fmt.Sprintf("grants %s on %s, which you do not hold (%s)", grant.Action, grant.Resource, decision.DecidedBy)})
		}
	}
	return issues, first
}

func writeEscalation(w http.ResponseWriter, issues policy.Issues, decision policy.Decision) {
	writeJSON(w, http.StatusForbidden, map[string]any{
		"error":    "privilege_escalation",
		"message":  fmt.Sprintf("You can only grant access you already hold: %d grant(s) exceed your own access. First: %s", len(issues), issues[0].Message),
		"details":  issues,
		"decision": decision,
	})
}

// ---- last administrator protection -------------------------------------------------

// errLastAdmin reports a change that would leave no enabled administrator.
var errLastAdmin = errors.New("this is the last enabled administrator; promote another administrator first")

// wouldRemoveLastAdmin reports whether demoting, suspending or deleting
// target leaves no enabled administrator. The acting administrator and,
// in local sign-in mode, the unprovisioned bootstrap administrator count.
func (s *Server) wouldRemoveLastAdmin(r *http.Request, target string) bool {
	current, err := s.store.AccessFor(target)
	if err != nil || current.Role != auth.RoleAdmin || current.Disabled {
		return false
	}
	caller := principal(r)
	if caller.Subject != target && slices.Contains(caller.Roles, auth.RoleAdmin) && !caller.Disabled {
		return false
	}
	for _, access := range s.store.UserAccess() {
		if access.Subject != target && access.Role == auth.RoleAdmin && !access.Disabled {
			return false
		}
	}
	if localAccountsEnabled() && target != bootstrapUsername() {
		if _, err := s.store.AccessFor(bootstrapUsername()); err != nil {
			if role := os.Getenv("MLAIOPS_LOCAL_ROLE"); role == "" || role == auth.RoleAdmin {
				return false
			}
		}
	}
	return true
}
