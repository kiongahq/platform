package api

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Applications are user services deployed through Kionga and reachable at
// <name>.<applications base domain>. They exist only on Kubernetes: the
// gateway records the application and creates a KiongaApplication custom
// resource that the operator reconciles.

// ApplicationSource builds the image from a Git repository instead of
// running a prebuilt one.
type ApplicationSource struct {
	Git        string `json:"git"`
	Ref        string `json:"ref,omitempty"`
	ContextDir string `json:"context_dir,omitempty"`
	Dockerfile string `json:"dockerfile,omitempty"`
}

// WorkloadResources are per-replica requests (and equal limits).
type WorkloadResources struct {
	CPU    string `json:"cpu,omitempty"`
	Memory string `json:"memory,omitempty"`
}

// ReplicaRange bounds autoscaling. Min 0 allows scale to zero.
type ReplicaRange struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

// WorkloadScaling picks the autoscaling trigger. Metric "requests" scales on
// concurrent HTTP requests per replica (KEDA HTTP add-on); "cpu" scales on
// CPU utilisation percent.
type WorkloadScaling struct {
	Metric string `json:"metric"`
	Target int    `json:"target"`
}

// WorkloadCondition mirrors a Kubernetes status condition.
type WorkloadCondition struct {
	Type               string `json:"type"`
	Status             string `json:"status"`
	Reason             string `json:"reason,omitempty"`
	Message            string `json:"message,omitempty"`
	LastTransitionTime string `json:"last_transition_time,omitempty"`
}

type Application struct {
	Name          string              `json:"name"`
	ProjectID     string              `json:"project_id"`
	Image         string              `json:"image,omitempty"`
	Source        *ApplicationSource  `json:"source,omitempty"`
	Port          int                 `json:"port"`
	Env           map[string]string   `json:"env,omitempty"`
	SecretRefs    []string            `json:"secret_refs,omitempty"`
	Resources     WorkloadResources   `json:"resources"`
	Replicas      ReplicaRange        `json:"replicas"`
	Scaling       WorkloadScaling     `json:"scaling"`
	HealthPath    string              `json:"health_path"`
	Visibility    string              `json:"visibility"`
	OwnerSubject  string              `json:"owner_subject,omitempty"`
	URL           string              `json:"url"`
	Status        string              `json:"status"`
	ReadyReplicas int                 `json:"ready_replicas"`
	Message       string              `json:"message,omitempty"`
	Conditions    []WorkloadCondition `json:"conditions,omitempty"`
	// Generation is the resource's metadata.generation; ObservedGeneration
	// is the generation the operator last reconciled (status.observedGeneration).
	// Status describes the current spec only when they are equal; until then
	// it is pending.
	Generation         int64          `json:"generation"`
	ObservedGeneration int64          `json:"observed_generation"`
	LastBuild          map[string]any `json:"last_build,omitempty"`
	RedeployedAt       *time.Time     `json:"redeployed_at,omitempty"`
	CreatedAt          time.Time      `json:"created_at"`
	UpdatedAt          time.Time      `json:"updated_at"`
}

// ApplicationRequest creates (POST, name required) or replaces (PUT, name
// from the path) an application. Exactly one of image or source is set.
type ApplicationRequest struct {
	Name       string             `json:"name"`
	ProjectID  string             `json:"project_id"`
	Image      string             `json:"image,omitempty"`
	Source     *ApplicationSource `json:"source,omitempty"`
	Port       int                `json:"port,omitempty"`
	Env        map[string]string  `json:"env,omitempty"`
	SecretRefs []string           `json:"secret_refs,omitempty"`
	Resources  WorkloadResources  `json:"resources,omitempty"`
	Replicas   *ReplicaRange      `json:"replicas,omitempty"`
	Scaling    *WorkloadScaling   `json:"scaling,omitempty"`
	HealthPath string             `json:"health_path,omitempty"`
	Visibility string             `json:"visibility,omitempty"`
}

// FieldIssue names one invalid request field.
type FieldIssue struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// FieldIssues is a validation failure listing every invalid field.
type FieldIssues []FieldIssue

func (issues FieldIssues) Error() string {
	parts := make([]string, 0, len(issues))
	for _, issue := range issues {
		parts = append(parts, issue.Field+": "+issue.Message)
	}
	return strings.Join(parts, "; ")
}

var (
	// 45 characters leaves room for the operator's -build-<hash>, -route
	// and -sso suffixes inside a 63-character DNS label.
	dnsLabel         = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,43}[a-z0-9])?$`)
	quantity         = regexp.MustCompile(`^[1-9][0-9]*(m|Mi|Gi)?$`)
	envName          = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	secretName       = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)
	gitRef           = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)
	relativeFilePath = regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._/-]{0,254}$`)
	healthPath       = regexp.MustCompile(`^/[A-Za-z0-9._~!$&'()*+,;=:@/-]{0,254}$`)
)

// reservedApplicationNames are host labels the install itself uses, or that
// would be confusing as a user service.
var reservedApplicationNames = []string{
	"admin", "api", "app", "apps", "auth", "console", "dex", "docs", "gateway", "grafana", "ide", "kionga",
	"langflow", "login", "mail", "mlflow", "prefect", "prometheus", "sso", "static", "status", "workbench", "workspace", "workspaces", "www",
}

// reservedApplicationPrefixes are the workspace host families
// (<kind>-<32 hex>.<workspace base domain>); an application must never be
// able to claim a workspace origin when both share one base domain.
var reservedApplicationPrefixes = []string{"workbench-", "ide-", "langflow-", "kionga-"}

// ValidateApplicationName checks the DNS label and the reserved host names.
func ValidateApplicationName(name string) error {
	if !dnsLabel.MatchString(name) {
		return errors.New("must be a lowercase DNS label: letters, digits and hyphens, starting with a letter, at most 45 characters")
	}
	if slices.Contains(reservedApplicationNames, name) {
		return fmt.Errorf("%q is reserved by the platform", name)
	}
	for _, prefix := range reservedApplicationPrefixes {
		if strings.HasPrefix(name, prefix) {
			return fmt.Errorf("names starting with %q are reserved for platform hosts", prefix)
		}
	}
	return nil
}

// Scaling defaults: KEDA HTTP add-on's default concurrency target, and the
// usual CPU utilisation target.
const (
	DefaultRequestsTarget = 100
	DefaultCPUTarget      = 80
	MaxWorkloadReplicas   = 100
	// MaxWorkloadNameLength matches the operator: room for its -build-<hash>,
	// -route and -sso suffixes inside a 63-character DNS label.
	MaxWorkloadNameLength = 45
)

// NormalizeScaling fills defaults and validates metric and target. An empty
// metric means requests.
func NormalizeScaling(scaling *WorkloadScaling) error {
	scaling.Metric = strings.TrimSpace(strings.ToLower(scaling.Metric))
	if scaling.Metric == "" {
		scaling.Metric = "requests"
	}
	switch scaling.Metric {
	case "requests":
		if scaling.Target == 0 {
			scaling.Target = DefaultRequestsTarget
		}
		if scaling.Target < 1 || scaling.Target > 10000 {
			return errors.New("requests target must be between 1 and 10000 concurrent requests per replica")
		}
	case "cpu":
		if scaling.Target == 0 {
			scaling.Target = DefaultCPUTarget
		}
		if scaling.Target < 1 || scaling.Target > 100 {
			return errors.New("cpu target must be a utilisation percentage between 1 and 100")
		}
	default:
		return errors.New("metric must be requests or cpu")
	}
	return nil
}

// NormalizeApplicationRequest trims, defaults and validates a request and
// returns every issue at once. It is pure: the same request always yields
// the same result.
func NormalizeApplicationRequest(req *ApplicationRequest) error {
	var issues FieldIssues
	add := func(field, message string) { issues = append(issues, FieldIssue{Field: field, Message: message}) }
	req.Name = strings.TrimSpace(req.Name)
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	req.Image = strings.TrimSpace(req.Image)
	if err := ValidateApplicationName(req.Name); err != nil {
		add("name", err.Error())
	}
	if req.ProjectID == "" {
		add("project_id", "is required")
	}
	if req.Source != nil {
		req.Source.Git = strings.TrimSpace(req.Source.Git)
		req.Source.Ref = strings.TrimSpace(req.Source.Ref)
		req.Source.ContextDir = strings.Trim(strings.TrimSpace(req.Source.ContextDir), "/")
		req.Source.Dockerfile = strings.TrimSpace(req.Source.Dockerfile)
		if req.Source.Git == "" && req.Source.Ref == "" && req.Source.ContextDir == "" && req.Source.Dockerfile == "" {
			req.Source = nil
		}
	}
	switch {
	case req.Image != "" && req.Source != nil:
		add("image", "set either image or source, not both")
	case req.Image == "" && req.Source == nil:
		add("image", "an image or a git source is required")
	case req.Image != "":
		if len(req.Image) > 512 || strings.IndexFunc(req.Image, func(char rune) bool { return unicode.IsSpace(char) || unicode.IsControl(char) }) >= 0 {
			add("image", "must be an OCI image reference without whitespace")
		}
	default:
		if !validGitURL(req.Source.Git) {
			add("source.git", "must be a public https:// Git URL without credentials; builds carry no Git credentials")
		}
		if req.Source.Ref != "" && (!gitRef.MatchString(req.Source.Ref) || strings.Contains(req.Source.Ref, "..")) {
			add("source.ref", "must be a branch, tag or commit")
		}
		if req.Source.ContextDir != "" && (!relativeFilePath.MatchString(req.Source.ContextDir) || strings.Contains(req.Source.ContextDir, "..")) {
			add("source.context_dir", "must be a relative path inside the repository")
		}
		if req.Source.Dockerfile == "" {
			req.Source.Dockerfile = "Dockerfile"
		}
		if !relativeFilePath.MatchString(req.Source.Dockerfile) || strings.Contains(req.Source.Dockerfile, "..") {
			add("source.dockerfile", "must be a relative path inside the context")
		}
	}
	if req.Port == 0 {
		req.Port = 8080
	}
	if req.Port < 1 || req.Port > 65535 {
		add("port", "must be between 1 and 65535")
	}
	if len(req.Env) > 100 {
		add("env", "at most 100 variables")
	}
	for name := range req.Env {
		if !envName.MatchString(name) {
			add("env", fmt.Sprintf("%q is not a valid variable name", name))
		}
		if strings.HasPrefix(name, "KIONGA_") {
			add("env", fmt.Sprintf("%q uses the reserved KIONGA_ prefix", name))
		}
	}
	seen := map[string]bool{}
	refs := make([]string, 0, len(req.SecretRefs))
	for _, ref := range req.SecretRefs {
		ref = strings.TrimSpace(ref)
		if ref == "" || seen[ref] {
			continue
		}
		seen[ref] = true
		if !secretName.MatchString(ref) {
			add("secret_refs", fmt.Sprintf("%q is not a valid Secret name", ref))
		}
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	req.SecretRefs = refs
	req.Resources.CPU = strings.TrimSpace(req.Resources.CPU)
	req.Resources.Memory = strings.TrimSpace(req.Resources.Memory)
	if req.Resources.CPU == "" {
		req.Resources.CPU = "250m"
	}
	if req.Resources.Memory == "" {
		req.Resources.Memory = "256Mi"
	}
	if !quantity.MatchString(req.Resources.CPU) {
		add("resources.cpu", "must be a quantity such as 500m or 2")
	}
	if !quantity.MatchString(req.Resources.Memory) {
		add("resources.memory", "must be a quantity such as 512Mi or 2Gi")
	}
	if req.Replicas == nil {
		req.Replicas = &ReplicaRange{Min: 1, Max: 3}
	}
	if req.Replicas.Min < 0 {
		add("replicas.min", "cannot be negative")
	}
	if req.Replicas.Max < 1 || req.Replicas.Max > MaxWorkloadReplicas {
		add("replicas.max", fmt.Sprintf("must be between 1 and %d", MaxWorkloadReplicas))
	}
	if req.Replicas.Max < req.Replicas.Min {
		add("replicas.max", "must be at least replicas.min")
	}
	if req.Scaling == nil {
		req.Scaling = &WorkloadScaling{}
	}
	if err := NormalizeScaling(req.Scaling); err != nil {
		add("scaling", err.Error())
	}
	if req.Scaling.Metric == "cpu" && req.Replicas.Min == 0 {
		add("replicas.min", "scale to zero needs the requests metric; CPU scaling keeps at least one replica")
	}
	req.HealthPath = strings.TrimSpace(req.HealthPath)
	if req.HealthPath == "" {
		req.HealthPath = "/"
	}
	if !healthPath.MatchString(req.HealthPath) {
		add("health_path", "must be an absolute URL path such as /healthz")
	}
	req.Visibility = strings.TrimSpace(strings.ToLower(req.Visibility))
	if req.Visibility == "" {
		req.Visibility = "sso"
	}
	if !ValidVisibility(req.Visibility) {
		add("visibility", "must be public, sso or internal")
	}
	if len(issues) > 0 {
		return issues
	}
	return nil
}

// validGitURL accepts what the operator builds: a public https URL, no
// credentials, no whitespace or fragment (^https://[^\s#@]+$).
func validGitURL(value string) bool {
	if len(value) > 512 || !strings.HasPrefix(value, "https://") {
		return false
	}
	rest := strings.TrimPrefix(value, "https://")
	host, _, _ := strings.Cut(rest, "/")
	return host != "" && strings.IndexFunc(rest, func(char rune) bool {
		return unicode.IsSpace(char) || unicode.IsControl(char) || char == '#' || char == '@'
	}) < 0
}

// ValidVisibility: public (anyone at the host), sso (signed-in users, through
// the gateway) or internal (no host; in-cluster callers only).
func ValidVisibility(value string) bool {
	return value == "public" || value == "sso" || value == "internal"
}

// ApplicationURL is the public address of an application.
func ApplicationURL(name, baseDomain string) string {
	baseDomain = strings.Trim(strings.TrimSpace(strings.ToLower(baseDomain)), ".")
	if baseDomain == "" {
		return ""
	}
	return "https://" + name + "." + baseDomain
}

// ModelDeploymentDNSName names the KiongaModelDeployment for one model name
// inside one project. The digest keeps equally named models in different
// projects apart.
func ModelDeploymentDNSName(projectID, modelName string) string {
	return digestedDNSName("model-", projectID+"/"+modelName, modelName)
}

func digestedDNSName(prefix, identity, readable string) string {
	stem := strings.Trim(nonDNSCharacter.ReplaceAllString(strings.ToLower(strings.TrimSpace(readable)), "-"), "-")
	if stem == "" {
		stem = "resource"
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(identity)))[:10]
	maximumStem := 63 - len(prefix) - len(digest) - 1
	if len(stem) > maximumStem {
		stem = strings.Trim(stem[:maximumStem], "-")
	}
	if stem == "" {
		stem = "resource"
	}
	return prefix + stem + "-" + digest
}
