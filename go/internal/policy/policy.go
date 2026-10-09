// Package policy is Kionga's IAM-style authorization evaluator. It is pure:
// no store, no HTTP, no clock except what the caller passes in, so every
// decision is reproducible from its inputs and testable as a truth table.
//
// Semantics, in order:
//
//  1. Default deny. Nothing is allowed unless a statement allows it.
//  2. An explicit deny in any applicable policy wins over every allow.
//  3. Otherwise an allow from any applicable policy (a role baseline or an
//     attached policy) allows the request.
//
// Role baselines (admin, operator, engineer, viewer, user, service) are
// built-in managed policies that reproduce the platform's role behaviour, so
// nothing changes for a principal with no attached policies.
package policy

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// Effect is the outcome a statement produces when it matches.
type Effect string

const (
	Allow Effect = "allow"
	Deny  Effect = "deny"
)

// HourWindow is a half-open UTC hour range [Start, End). Start > End wraps
// midnight, so {22, 6} covers 22:00 to 05:59 UTC.
type HourWindow struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// Conditions narrow when a statement applies. Every condition that is set
// must hold (logical AND); inside one condition the listed values are
// alternatives (logical OR). A statement whose conditions do not hold is
// skipped entirely, for allow and deny alike.
type Conditions struct {
	// IPCIDR lists networks the request's source address must fall in.
	IPCIDR []string `json:"ip_cidr,omitempty"`
	// UTCHours restricts the statement to a time-of-day window.
	UTCHours *HourWindow `json:"utc_hours,omitempty"`
	// ResourceTags must all equal the target resource's tags.
	ResourceTags map[string]string `json:"resource_tags,omitempty"`
	// ResourceProfile lists the request.resource_profile values allowed.
	ResourceProfile []string `json:"resource_profile,omitempty"`
}

func (c *Conditions) empty() bool {
	return c == nil || (len(c.IPCIDR) == 0 && c.UTCHours == nil && len(c.ResourceTags) == 0 && len(c.ResourceProfile) == 0)
}

// Statement grants or denies actions on resources.
type Statement struct {
	Sid        string      `json:"sid"`
	Effect     Effect      `json:"effect"`
	Actions    []string    `json:"actions"`
	Resources  []string    `json:"resources"`
	Conditions *Conditions `json:"conditions,omitempty"`
}

// Policy is a named, versioned set of statements. Managed policies are the
// built-in role baselines; they cannot be edited or attached.
type Policy struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Version     int         `json:"version"`
	Statements  []Statement `json:"statements"`
	Managed     bool        `json:"managed,omitempty"`
	CreatedAt   time.Time   `json:"created_at,omitzero"`
	UpdatedAt   time.Time   `json:"updated_at,omitzero"`
	CreatedBy   string      `json:"created_by,omitempty"`
	UpdatedBy   string      `json:"updated_by,omitempty"`
}

// Binding is a policy that applies to a principal and why: "role:admin",
// "user:alice" or "group:ml-team".
type Binding struct {
	Policy Policy `json:"policy"`
	Source string `json:"source"`
}

// Context carries the request attributes conditions can test.
type Context struct {
	SourceIP        string            `json:"source_ip,omitempty"`
	Time            time.Time         `json:"time,omitzero"`
	ResourceTags    map[string]string `json:"resource_tags,omitempty"`
	ResourceProfile string            `json:"resource_profile,omitempty"`
}

// Request is one authorization question.
type Request struct {
	Action   string  `json:"action"`
	Resource string  `json:"resource"`
	Context  Context `json:"context"`
}

// StatementRef identifies a statement that matched a request.
type StatementRef struct {
	PolicyID   string `json:"policy_id"`
	PolicyName string `json:"policy_name"`
	Version    int    `json:"version"`
	Source     string `json:"source"`
	Sid        string `json:"sid"`
	Effect     Effect `json:"effect"`
}

// String renders "policy-id@v3#Sid".
func (s StatementRef) String() string {
	return fmt.Sprintf("%s@v%d#%s", s.PolicyID, s.Version, s.Sid)
}

// PolicyRef identifies a policy that was evaluated.
type PolicyRef struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version int    `json:"version"`
	Source  string `json:"source"`
	Managed bool   `json:"managed,omitempty"`
}

// Decision is the result plus the explain trace: which statements matched
// and which policies were consulted.
type Decision struct {
	Allowed           bool           `json:"allowed"`
	Action            string         `json:"action"`
	Resource          string         `json:"resource"`
	Reason            string         `json:"reason"`
	DecidedBy         string         `json:"decided_by"`
	MatchedStatements []StatementRef `json:"matched_statements"`
	EvaluatedPolicies []PolicyRef    `json:"evaluated_policies"`
}

// DefaultDeny is DecidedBy when no statement allowed the request.
const DefaultDeny = "default-deny"

// Evaluate decides one request against every applicable policy.
func Evaluate(bindings []Binding, req Request) Decision {
	decision := Decision{Action: req.Action, Resource: req.Resource, MatchedStatements: []StatementRef{}, EvaluatedPolicies: []PolicyRef{}}
	var firstAllow, firstDeny *StatementRef
	for _, binding := range bindings {
		p := binding.Policy
		decision.EvaluatedPolicies = append(decision.EvaluatedPolicies, PolicyRef{ID: p.ID, Name: p.Name, Version: p.Version, Source: binding.Source, Managed: p.Managed})
		for i, statement := range p.Statements {
			if !statementMatches(statement, req) {
				continue
			}
			ref := StatementRef{PolicyID: p.ID, PolicyName: p.Name, Version: p.Version, Source: binding.Source, Sid: sidOf(statement, i), Effect: normalizeEffect(statement.Effect)}
			decision.MatchedStatements = append(decision.MatchedStatements, ref)
			last := &decision.MatchedStatements[len(decision.MatchedStatements)-1]
			if ref.Effect == Deny && firstDeny == nil {
				firstDeny = last
			}
			if ref.Effect == Allow && firstAllow == nil {
				firstAllow = last
			}
		}
	}
	switch {
	case firstDeny != nil:
		ref := *firstDeny
		decision.DecidedBy = ref.String()
		decision.Reason = fmt.Sprintf("Explicit deny: statement %s in policy %q (%s) denies %s on %s. An explicit deny overrides every allow.", ref.Sid, ref.PolicyName, ref.Source, req.Action, req.Resource)
	case firstAllow != nil:
		ref := *firstAllow
		decision.Allowed = true
		decision.DecidedBy = ref.String()
		decision.Reason = fmt.Sprintf("Allowed by statement %s in policy %q (%s).", ref.Sid, ref.PolicyName, ref.Source)
	default:
		decision.DecidedBy = DefaultDeny
		decision.Reason = fmt.Sprintf("Default deny: no statement in the %d applicable policies allows %s on %s.", len(bindings), req.Action, req.Resource)
	}
	return decision
}

func sidOf(statement Statement, index int) string {
	if statement.Sid != "" {
		return statement.Sid
	}
	return fmt.Sprintf("Statement%d", index+1)
}

func normalizeEffect(effect Effect) Effect {
	return Effect(strings.ToLower(strings.TrimSpace(string(effect))))
}

func statementMatches(statement Statement, req Request) bool {
	effect := normalizeEffect(statement.Effect)
	if effect != Allow && effect != Deny {
		return false
	}
	if !slices.ContainsFunc(statement.Actions, func(pattern string) bool { return MatchAction(pattern, req.Action) }) {
		return false
	}
	if !slices.ContainsFunc(statement.Resources, func(pattern string) bool { return MatchResource(pattern, req.Resource) }) {
		return false
	}
	return conditionsHold(statement.Conditions, req.Context)
}

// MatchAction reports whether an action pattern covers an action. Patterns
// are "*", "<namespace>:*" or an exact action; comparison ignores case.
func MatchAction(pattern, action string) bool {
	pattern, action = strings.ToLower(strings.TrimSpace(pattern)), strings.ToLower(strings.TrimSpace(action))
	if pattern == "*" {
		return true
	}
	if namespace, ok := strings.CutSuffix(pattern, ":*"); ok {
		return strings.HasPrefix(action, namespace+":")
	}
	return pattern == action
}

// ResourcePrefix starts every Kionga resource name.
const ResourcePrefix = "kionga:"

// MatchResource reports whether a resource pattern covers a resource.
// Resources are "kionga:" followed by "/"-separated segments. In a pattern a
// "*" segment matches exactly one segment, except as the final segment where
// it matches one or more remaining segments. The pattern "*" matches any
// resource. Comparison is exact (resource IDs are case-sensitive).
func MatchResource(pattern, resource string) bool {
	if pattern == "*" {
		return true
	}
	if !strings.HasPrefix(pattern, ResourcePrefix) || !strings.HasPrefix(resource, ResourcePrefix) {
		return false
	}
	ps := strings.Split(strings.TrimPrefix(pattern, ResourcePrefix), "/")
	rs := strings.Split(strings.TrimPrefix(resource, ResourcePrefix), "/")
	for i, segment := range ps {
		if i == len(ps)-1 && segment == "*" {
			return len(rs) >= len(ps)
		}
		if i >= len(rs) || (segment != "*" && segment != rs[i]) {
			return false
		}
	}
	return len(ps) == len(rs)
}

func conditionsHold(c *Conditions, ctx Context) bool {
	if c.empty() {
		return true
	}
	if len(c.IPCIDR) > 0 {
		ip := net.ParseIP(strings.TrimSpace(ctx.SourceIP))
		if ip == nil {
			return false
		}
		inside := false
		for _, cidr := range c.IPCIDR {
			if _, network, err := net.ParseCIDR(strings.TrimSpace(cidr)); err == nil && network.Contains(ip) {
				inside = true
				break
			}
		}
		if !inside {
			return false
		}
	}
	if c.UTCHours != nil {
		if ctx.Time.IsZero() {
			return false
		}
		hour := ctx.Time.UTC().Hour()
		start, end := c.UTCHours.Start, c.UTCHours.End
		if start < end {
			if hour < start || hour >= end {
				return false
			}
		} else if hour < start && hour >= end {
			return false
		}
	}
	for key, want := range c.ResourceTags {
		if got, ok := ctx.ResourceTags[key]; !ok || got != want {
			return false
		}
	}
	if len(c.ResourceProfile) > 0 && !slices.Contains(c.ResourceProfile, ctx.ResourceProfile) {
		return false
	}
	return true
}

// ---- resource names ---------------------------------------------------------

// segment escapes one resource segment; "*" stays a wildcard.
func segment(value string) string {
	if value == "*" {
		return value
	}
	return url.PathEscape(value)
}

// ProjectResource names a project: kionga:project/<id>.
func ProjectResource(projectID string) string {
	return ResourcePrefix + "project/" + segment(projectID)
}

// PipelineResource names a pipeline (definition) inside a project:
// kionga:project/<project>/pipeline/<pipeline>. Use "*" as the pipeline for
// "any pipeline in this project", such as creating a new definition.
func PipelineResource(projectID, pipelineID string) string {
	return ProjectResource(projectID) + "/pipeline/" + segment(pipelineID)
}

// WorkspaceResource names a workspace kind, optionally inside a project:
// kionga:workspace/<kind> or kionga:project/<project>/workspace/<kind>.
func WorkspaceResource(projectID, kind string) string {
	if projectID == "" {
		return ResourcePrefix + "workspace/" + segment(kind)
	}
	return ProjectResource(projectID) + "/workspace/" + segment(kind)
}

// FeatureResource names a feature view: kionga:feature/<name>.
func FeatureResource(name string) string { return ResourcePrefix + "feature/" + segment(name) }

// UserResource names an identity: kionga:user/<subject>.
func UserResource(subject string) string { return ResourcePrefix + "user/" + segment(subject) }

// GroupResource names a group: kionga:group/<id>.
func GroupResource(id string) string { return ResourcePrefix + "group/" + segment(id) }

// PolicyResource names a policy: kionga:policy/<id>.
func PolicyResource(id string) string { return ResourcePrefix + "policy/" + segment(id) }

// BlogResource names a blog post: kionga:blog/<id>.
func BlogResource(id string) string { return ResourcePrefix + "blog/" + segment(id) }

// ConnectionResource names an infrastructure connection: kionga:connection/<id>.
func ConnectionResource(id string) string { return ResourcePrefix + "connection/" + segment(id) }

// resourceTypes are the first segment of every valid resource name.
var resourceTypes = []string{"project", "workspace", "feature", "user", "group", "policy", "blog", "connection", "*"}

// ---- action catalog -----------------------------------------------------------

// Action names. The catalog below documents each one.
const (
	ProjectRead                = "project:Read"
	ProjectCreate              = "project:Create"
	PipelineRead               = "pipeline:Read"
	PipelineWrite              = "pipeline:Write"
	PipelineRun                = "pipeline:Run"
	PipelineOverrideParameters = "pipeline:OverrideParameters"
	PipelineOverrideResources  = "pipeline:OverrideResources"
	PipelineOverrideImage      = "pipeline:OverrideImage"
	LogsRead                   = "logs:Read"
	WorkspaceOpen              = "workspace:Open"
	FeatureRead                = "feature:Read"
	FeatureWrite               = "feature:Write"
	SecretRead                 = "secret:Read"
	InfraProvision             = "infra:Provision"
	BlogWrite                  = "blog:Write"
	BlogPublish                = "blog:Publish"
	PolicyRead                 = "policy:Read"
	PolicyWrite                = "policy:Write"
	PolicyAttach               = "policy:Attach"
	UserManage                 = "user:Manage"
)

// ActionInfo documents one action.
type ActionInfo struct {
	Action      string `json:"action"`
	Description string `json:"description"`
	Resource    string `json:"resource"`
	Enforced    string `json:"enforced"`
}

var catalog = []ActionInfo{
	{ProjectRead, "See a project and its metadata.", "kionga:project/<project>", "GET /api/v1/projects, GET /api/v1/projects/{id}"},
	{ProjectCreate, "Create a new project (quotas still apply).", "kionga:project/*", "POST /api/v1/projects"},
	{PipelineRead, "See pipeline definitions, revisions and runs.", "kionga:project/<project>/pipeline/<pipeline>", "pipeline definition and run reads, /api/v1/events digest"},
	{PipelineWrite, "Create or change pipeline definitions, schedules and rollbacks; report run steps.", "kionga:project/<project>/pipeline/<pipeline>", "definition saves, rollback, trigger pause/resume, run step reports"},
	{PipelineRun, "Submit, cancel or retry pipeline runs.", "kionga:project/<project>/pipeline/<pipeline>", "POST /api/v1/pipelines/submit, runs/{id}/cancel, runs/{id}/retry"},
	{PipelineOverrideParameters, "Override parameters for one run.", "kionga:project/<project>/pipeline/<pipeline>", "submit with overrides.parameters"},
	{PipelineOverrideResources, "Override node CPU, memory or GPU for one run.", "kionga:project/<project>/pipeline/<pipeline>", "submit with overrides.nodes.*.resources"},
	{PipelineOverrideImage, "Override a node's container image for one run.", "kionga:project/<project>/pipeline/<pipeline>", "submit with overrides.nodes.*.image"},
	{LogsRead, "Read run logs.", "kionga:project/<project>/pipeline/<pipeline>", "logs are removed from run reads when denied"},
	{WorkspaceOpen, "Launch JupyterLab or the browser IDE.", "kionga:workspace/<kind> or kionga:project/<project>/workspace/<kind>", "POST /api/v1/workspaces/{kind}/launch"},
	{FeatureRead, "See feature views.", "kionga:feature/<name>", "GET /api/v1/features"},
	{FeatureWrite, "Apply feature views and report materializations.", "kionga:feature/<name>", "POST /api/v1/features"},
	{SecretRead, "Read connection secrets. Reserved: the gateway never returns secrets today.", "kionga:connection/<id>", "not yet enforced"},
	{InfraProvision, "Create, test and activate infrastructure connections.", "kionga:connection/<id>", "POST /api/v1/connections, connections/{id}/test, connections/{id}/activate"},
	{BlogWrite, "Create, edit and delete engineering blog drafts.", "kionga:blog/<id>", "POST/PUT/DELETE /api/v1/admin/blogs"},
	{BlogPublish, "Publish a blog post.", "kionga:blog/<id>", "saving a post with status published"},
	{PolicyRead, "Read policies, groups and attachments; explain another principal's access.", "kionga:policy/<id>, kionga:user/<subject>", "GET /api/v1/admin/iam/*, GET /api/v1/iam/explain for other principals"},
	{PolicyWrite, "Create, edit and delete policies and groups.", "kionga:policy/<id>, kionga:group/<id>", "POST/PUT/DELETE /api/v1/admin/iam/policies, /groups"},
	{PolicyAttach, "Attach or detach a policy. The actor must already hold every action the policy grants.", "kionga:policy/<id>", "POST/DELETE /api/v1/admin/iam/attachments"},
	{UserManage, "Provision, change, suspend or revoke user access and passwords; review access requests.", "kionga:user/<subject>", "PUT/DELETE /api/v1/admin/users/{subject}, local passwords, access request review"},
}

// Catalog returns the documented action catalog.
func Catalog() []ActionInfo { return slices.Clone(catalog) }

// Namespaces returns the action namespaces ("project", "pipeline", ...).
func Namespaces() []string {
	seen := []string{}
	for _, item := range catalog {
		namespace, _, _ := strings.Cut(item.Action, ":")
		if !slices.Contains(seen, namespace) {
			seen = append(seen, namespace)
		}
	}
	return seen
}

// KnownAction reports whether action names a catalog action exactly (case
// insensitive).
func KnownAction(action string) bool {
	return slices.ContainsFunc(catalog, func(item ActionInfo) bool { return strings.EqualFold(item.Action, action) })
}

// CanonicalAction returns the catalog spelling of an action.
func CanonicalAction(action string) (string, bool) {
	for _, item := range catalog {
		if strings.EqualFold(item.Action, strings.TrimSpace(action)) {
			return item.Action, true
		}
	}
	return "", false
}

// ExpandActions returns every catalog action covered by any of patterns.
func ExpandActions(patterns []string) []string {
	out := []string{}
	for _, item := range catalog {
		if slices.ContainsFunc(patterns, func(pattern string) bool { return MatchAction(pattern, item.Action) }) {
			out = append(out, item.Action)
		}
	}
	return out
}

// ---- validation ---------------------------------------------------------------

// Issue ties a validation failure to the field that caused it.
type Issue struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Issues is a list of validation failures. It is an error.
type Issues []Issue

func (issues Issues) Error() string {
	parts := make([]string, len(issues))
	for i, issue := range issues {
		parts[i] = issue.Field + ": " + issue.Message
	}
	return strings.Join(parts, "; ")
}

var (
	idPattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)
	sidPattern      = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	segmentPattern  = regexp.MustCompile(`^(\*|[A-Za-z0-9._@~%+=-]+)$`)
	tagKeyPattern   = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,62}$`)
	reservedPrefix  = "kionga-"
	maxStatements   = 50
	maxListElements = 100
)

// ValidID reports whether id is a valid policy or group identifier:
// lowercase letters, digits and hyphens, 2-63 characters.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// Slug derives an identifier from a display name.
func Slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if len(slug) > 63 {
		slug = strings.Trim(slug[:63], "-")
	}
	return slug
}

// ValidResource reports whether a resource pattern is well formed.
func ValidResource(resource string) error {
	if resource == "*" {
		return nil
	}
	rest, ok := strings.CutPrefix(resource, ResourcePrefix)
	if !ok {
		return fmt.Errorf("must be * or start with %q", ResourcePrefix)
	}
	segments := strings.Split(rest, "/")
	if !slices.Contains(resourceTypes, segments[0]) {
		return fmt.Errorf("unknown resource type %q; use one of %s", segments[0], strings.Join(resourceTypes[:len(resourceTypes)-1], ", "))
	}
	for _, s := range segments {
		if !segmentPattern.MatchString(s) {
			return fmt.Errorf("segment %q is empty or contains characters other than letters, digits and ._@~%%+=-", s)
		}
	}
	return nil
}

// ValidAction reports whether an action pattern is "*", "<namespace>:*" or
// a catalog action.
func ValidAction(action string) error {
	action = strings.TrimSpace(action)
	if action == "*" || KnownAction(action) {
		return nil
	}
	if namespace, ok := strings.CutSuffix(action, ":*"); ok {
		if slices.Contains(Namespaces(), strings.ToLower(namespace)) {
			return nil
		}
		return fmt.Errorf("unknown action namespace %q; use one of %s", namespace, strings.Join(Namespaces(), ", "))
	}
	return fmt.Errorf("unknown action %q; see GET /api/v1/iam/catalog", action)
}

// Normalize trims and canonicalizes a policy in place and returns every
// problem found. knownProfiles, when non-empty, restricts resource_profile
// condition values. A valid policy has a name, 1-50 statements, and each
// statement has a unique sid, an effect, known actions, well-formed
// resources and parseable conditions.
func Normalize(p *Policy, knownProfiles []string) Issues {
	var issues Issues
	add := func(field, format string, args ...any) {
		issues = append(issues, Issue{Field: field, Message: fmt.Sprintf(format, args...)})
	}
	p.Name = strings.TrimSpace(p.Name)
	p.Description = strings.TrimSpace(p.Description)
	if len(p.Name) < 3 || len(p.Name) > 80 {
		add("name", "must be 3-80 characters")
	}
	if len(p.Description) > 500 {
		add("description", "must be at most 500 characters")
	}
	if len(p.Statements) == 0 {
		add("statements", "at least one statement is required")
	}
	if len(p.Statements) > maxStatements {
		add("statements", "at most %d statements are allowed", maxStatements)
	}
	sids := map[string]bool{}
	for i := range p.Statements {
		s := &p.Statements[i]
		at := fmt.Sprintf("statements[%d]", i)
		s.Sid = strings.TrimSpace(s.Sid)
		if s.Sid == "" {
			s.Sid = fmt.Sprintf("Statement%d", i+1)
		}
		if !sidPattern.MatchString(s.Sid) {
			add(at+".sid", "use 1-64 letters, digits, '_' or '-'")
		} else if sids[s.Sid] {
			add(at+".sid", "duplicate sid %q", s.Sid)
		}
		sids[s.Sid] = true
		s.Effect = normalizeEffect(s.Effect)
		if s.Effect != Allow && s.Effect != Deny {
			add(at+".effect", "must be allow or deny")
		}
		s.Actions = cleanList(s.Actions)
		if len(s.Actions) == 0 {
			add(at+".actions", "at least one action is required")
		}
		if len(s.Actions) > maxListElements {
			add(at+".actions", "at most %d actions are allowed", maxListElements)
		}
		for j, action := range s.Actions {
			if err := ValidAction(action); err != nil {
				add(fmt.Sprintf("%s.actions[%d]", at, j), "%s", err.Error())
			} else if canonical, ok := CanonicalAction(action); ok {
				s.Actions[j] = canonical
			}
		}
		s.Resources = cleanList(s.Resources)
		if len(s.Resources) == 0 {
			add(at+".resources", "at least one resource is required")
		}
		if len(s.Resources) > maxListElements {
			add(at+".resources", "at most %d resources are allowed", maxListElements)
		}
		for j, resource := range s.Resources {
			if err := ValidResource(resource); err != nil {
				add(fmt.Sprintf("%s.resources[%d]", at, j), "%s", err.Error())
			}
		}
		if s.Conditions.empty() {
			s.Conditions = nil
			continue
		}
		c := s.Conditions
		c.IPCIDR = cleanList(c.IPCIDR)
		for j, cidr := range c.IPCIDR {
			if _, _, err := net.ParseCIDR(cidr); err != nil {
				add(fmt.Sprintf("%s.conditions.ip_cidr[%d]", at, j), "%q is not a CIDR such as 10.0.0.0/8", cidr)
			}
		}
		if c.UTCHours != nil {
			if c.UTCHours.Start < 0 || c.UTCHours.Start > 23 {
				add(at+".conditions.utc_hours.start", "must be an hour from 0 to 23")
			}
			if c.UTCHours.End < 0 || c.UTCHours.End > 24 {
				add(at+".conditions.utc_hours.end", "must be an hour from 0 to 24")
			}
			if c.UTCHours.Start == c.UTCHours.End || (c.UTCHours.Start == 0 && c.UTCHours.End == 24) {
				add(at+".conditions.utc_hours", "the window must not be empty or cover the whole day; remove the condition instead")
			}
		}
		keys := make([]string, 0, len(c.ResourceTags))
		for key := range c.ResourceTags {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if !tagKeyPattern.MatchString(key) {
				add(at+".conditions.resource_tags", "tag key %q must be lowercase letters, digits, '_', '.' or '-'", key)
			}
		}
		c.ResourceProfile = cleanList(c.ResourceProfile)
		for j, profile := range c.ResourceProfile {
			if len(knownProfiles) > 0 && !slices.Contains(knownProfiles, profile) {
				add(fmt.Sprintf("%s.conditions.resource_profile[%d]", at, j), "unknown resource profile %q; use one of %s", profile, strings.Join(knownProfiles, ", "))
			}
		}
	}
	return issues
}

// ReservedID reports whether id belongs to a built-in managed policy.
func ReservedID(id string) bool { return strings.HasPrefix(id, reservedPrefix) }

func cleanList(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !slices.Contains(out, value) {
			out = append(out, value)
		}
	}
	return out
}

// Grants lists the (action, resource) pairs a policy's allow statements
// grant, with wildcard actions expanded through the catalog. Conditions are
// ignored, which makes anti-escalation checks conservative.
func Grants(p Policy) []Request {
	out := []Request{}
	for _, statement := range p.Statements {
		if normalizeEffect(statement.Effect) != Allow {
			continue
		}
		for _, action := range ExpandActions(statement.Actions) {
			for _, resource := range statement.Resources {
				out = append(out, Request{Action: action, Resource: resource})
			}
		}
	}
	return out
}
